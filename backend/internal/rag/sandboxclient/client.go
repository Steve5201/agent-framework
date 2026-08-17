// Package sandboxclient rag 侧沙盒解析客户端（P3-A3b）。
//
// 文档解析委托链路（与 sandboxsvc profile 模式契约对应）：
//
//	rag（本客户端）                        sandbox-service /v1/exec
//	 1. ensureWorkspace（POST code:"true"）→ 主进程创建 /work/users/<uid> 工作区
//	 2. 写 input 到 <WorkRoot>/users/<uid>/ingest/<docID>/input.<ext>
//	 3. POST {user_id, profile:"parse_pdf|docx|pptx", args:[input,out,media]}
//	                                       → 降权进程执行预置解析脚本
//	 4. 读回 out.json（统一产物契约）       ← 脚本原子写 out.json
//	 5. 清理 ingest 临时目录（媒体留在 rag-media/ 供检索引用/前端渲染）
//
// 权限模型（复用沙盒"组协作"设计）：rag 以 app 组身份写入用户工作区
// （users/<uid> 属组 = app 组）；解析进程以降权 uid 读取 input、写 out.json；
// 媒体文件落 rag-media/ 供 agent /files 等静态端点按 other 权限读取。
package sandboxclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// defaultMaxDocBytes 单篇文档字节默认上限（摄取入队校验；可经 Client.MaxDocBytes
// 覆盖，见 RAG_MAX_DOC_MB 环境变量）。
const defaultMaxDocBytes = 20 << 20

// MediaDirName 媒体公共只读区目录名（位于共享卷根，不落在 users/<uid> 私有目录）。
// 所有用户可经 agent /files 读取（对话渲染知识库媒体），删除知识库/文档时按
// docID 联动清理（见 rag/media_cleanup.go）。
const MediaDirName = "rag-media"

// MediaFile 解析产物的媒体文件（对应 scripts/parsers/common.py media 条目）。
type MediaFile struct {
	Type string `json:"type"` // image | video | audio | other
	Path string `json:"path"` // 相对沙盒工作区的持久路径（rag-media/<docID>/<file>）
	Alt  string `json:"alt"`  // 图片占位 alt
}

// ParseResult 沙盒解析产物（统一 JSON 契约，与解析脚本对齐）。
type ParseResult struct {
	Title    string      `json:"title"`
	Markdown string      `json:"markdown"`
	Media    []MediaFile `json:"media"`
	ScanOnly bool        `json:"scan_only"`
	Warnings []string    `json:"warnings"`
}

// Client 沙盒解析客户端。
type Client struct {
	// BaseURL sandbox-service HTTP 地址（如 http://sandbox:8087）。
	BaseURL string
	// WorkRoot 共享卷容器内根目录（与 sandbox 同路径，默认 /work）。
	WorkRoot string
	// UserID 解析沙盒固定使用的用户 id（>0）。
	UserID int64
	// HTTP HTTP 客户端（默认超时 130s，覆盖解析 profile 120s 上限 + 网络余量）。
	HTTP *http.Client
	// MaxDocBytes 单篇文档字节上限（RAG_MAX_DOC_MB 注入；0 = 默认 20MB）。
	// 摄取入队校验：超过在写入共享卷前直接拒绝。
	MaxDocBytes int64
	// Log 日志实例。
	Log *zap.Logger
}

// docBytesLimit 返回单篇文档字节上限；未配置（0）时回退默认 20MB。
func (c *Client) docBytesLimit() int64 {
	if c.MaxDocBytes <= 0 {
		return defaultMaxDocBytes
	}
	return c.MaxDocBytes
}

// New 构造客户端（WorkRoot 缺省 /work，HTTP 超时 130s）。
func New(baseURL string, workRoot string, userID int64, log *zap.Logger) *Client {
	if workRoot == "" {
		workRoot = "/work"
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Client{
		BaseURL:  baseURL,
		WorkRoot: workRoot,
		UserID:   userID,
		HTTP:     &http.Client{Timeout: 130 * time.Second},
		Log:      log,
	}
}

// Parse 解析单篇文档：写入共享卷 → 触发沙盒 profile → 读回产物。
// fileType ∈ {pdf, docx, pptx}。失败时清理 ingest 临时目录（不污染共享卷）。
func (c *Client) Parse(ctx context.Context, fileType string, data []byte, docID string) (*ParseResult, error) {
	if c.BaseURL == "" {
		return nil, errors.New("沙盒解析未配置（RAG_SANDBOX_URL 为空）")
	}
	if c.UserID <= 0 {
		return nil, fmt.Errorf("解析沙盒 user_id 非法: %d", c.UserID)
	}
	if len(data) == 0 {
		return nil, errors.New("文档内容为空")
	}
	limit := c.docBytesLimit()
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("文档超过大小上限 %dMB", limit>>20)
	}
	switch fileType {
	case "pdf", "docx", "pptx":
	default:
		return nil, fmt.Errorf("不支持的沙盒解析格式: %s", fileType)
	}

	// 1. 确保用户工作区存在（sandbox 主进程创建 users/<uid>，属主=派生uid/组=app组）。
	if err := c.ensureWorkspace(ctx); err != nil {
		return nil, err
	}

	// 2. 写输入文件到 ingest 临时目录。
	ingestDir := filepath.Join(c.WorkRoot, "users", strconv.FormatInt(c.UserID, 10), "ingest", docID)
	// 媒体写入公共只读区（P3-A8）：<WorkRoot>/rag-media/<docID>/。
	// 不放进用户私有目录 users/<uid>/rag-media —— 私有目录属主为派生 uid 且
	// other=0，其它用户经 agent /files 读取会 403，导致对话界面无法渲染 KB 媒体。
	// 公共区目录 0777 + 文件 0644，所有用户可读；删除知识库/文档时按 docID 清理。
	mediaDir := filepath.Join(c.WorkRoot, MediaDirName, docID)
	if err := os.MkdirAll(ingestDir, 0o755); err != nil {
		return nil, fmt.Errorf("沙盒解析: 创建 ingest 目录失败: %w", err)
	}
	// 媒体目录由解析脚本创建（目录 0777）；rag 不预建避免属主不符。
	// 公共区父目录 rag-media/ 由 sandbox 主进程（root）在执行前确保存在且 0777
	// （见 sandboxsvc executor.prepareProfileDirs），降权解析进程才有权在其中建目录。
	inputPath := filepath.Join(ingestDir, "input."+fileType)
	if err := os.WriteFile(inputPath, data, 0o644); err != nil {
		_ = os.RemoveAll(ingestDir)
		return nil, fmt.Errorf("沙盒解析: 写入输入文件失败: %w", err)
	}
	defer func() {
		// 解析结束后清理 ingest 临时目录（媒体留在 rag-media/ 供后续引用）。
		// 清理为 best-effort：失败仅告警，不阻断摄取。
		if err := os.RemoveAll(ingestDir); err != nil {
			c.Log.Warn("沙盒解析: 清理 ingest 临时目录失败", zap.String("dir", ingestDir), zap.Error(err))
		}
	}()

	// 3. 触发沙盒 profile 解析（参数用容器内绝对路径，rag 与 sandbox 共享 /work）。
	outPath := filepath.Join(ingestDir, "out.json")
	if _, err := c.execProfile(ctx, c.UserID, "parse_"+fileType, []string{inputPath, outPath, mediaDir}); err != nil {
		return nil, err
	}

	// 4. 读回统一产物。
	result, err := readResult(outPath)
	if err != nil {
		return nil, err
	}
	c.Log.Info("沙盒解析完成",
		zap.String("doc", docID), zap.String("type", fileType),
		zap.Int("media", len(result.Media)), zap.Bool("scan_only", result.ScanOnly))
	return result, nil
}

// ExecProfile 执行一次预置沙盒脚本（profile 模式，P4-I 文档渲染等），
// 沙盒执行身份为 c.UserID（固定沙盒用户，与 rag 摄取一致）。
// args 为容器内绝对路径（agent 与 sandbox 共享 /work 卷，路径一致）。
// 与 Parse 共享 ensureWorkspace + execProfile 链路；仅校验参数、不读写产物
// （产物内容/权限由调用方负责，渲染脚本自身保证 0644 可读）。
func (c *Client) ExecProfile(ctx context.Context, profile string, args []string) error {
	return c.ExecProfileAs(ctx, c.UserID, profile, args)
}

// ExecProfileAs 执行预置沙盒脚本，但沙盒执行身份为指定 userID（按调用方业务
// 用户隔离工作区与派生 uid）。用于渲染产物落在真实用户私有目录的场景
// （users/<uid>/chat-docs/…）：只有以该用户的派生 uid 执行，渲染进程才有权
// 穿越并写入其 2770 私有目录（other=0，跨用户不可达）。
func (c *Client) ExecProfileAs(ctx context.Context, userID int64, profile string, args []string) error {
	if c.BaseURL == "" {
		return errors.New("沙盒渲染未配置（RAG_SANDBOX_URL 为空）")
	}
	if userID <= 0 {
		return fmt.Errorf("渲染沙盒 user_id 非法: %d", userID)
	}
	if len(args) == 0 {
		return errors.New("沙盒渲染: 缺少脚本参数")
	}
	if err := c.ensureWorkspaceFor(ctx, userID); err != nil {
		return err
	}
	if _, err := c.execProfile(ctx, userID, profile, args); err != nil {
		return err
	}
	return nil
}

// ensureWorkspace 触发 sandbox 主进程创建 c.UserID 的工作区（最小 shell 执行）。
func (c *Client) ensureWorkspace(ctx context.Context) error {
	return c.ensureWorkspaceFor(ctx, c.UserID)
}

// ensureWorkspaceFor 以指定 userID 触发 sandbox 主进程创建用户工作区。
func (c *Client) ensureWorkspaceFor(ctx context.Context, userID int64) error {
	body, _ := json.Marshal(map[string]any{
		"user_id":  userID,
		"language": "shell",
		"code":     "true",
	})
	resp, err := c.post(ctx, body)
	if err != nil {
		return fmt.Errorf("沙盒解析: 工作区初始化失败: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("沙盒解析: 工作区初始化被拒绝: %s", resp.Error)
	}
	return nil
}

// execProfile 以指定 userID 执行一次预置解析脚本。
func (c *Client) execProfile(ctx context.Context, userID int64, profile string, args []string) (*execResponse, error) {
	body, _ := json.Marshal(map[string]any{
		"user_id":         userID,
		"profile":         profile,
		"args":            args,
		"timeout_seconds": 110, // 接近 profile 上限 120s，比普通执行上限宽裕
	})
	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("沙盒解析: %s", resp.Error)
	}
	if resp.TimedOut {
		return nil, errors.New("沙盒解析: 解析脚本执行超时")
	}
	if resp.ExitCode != 0 {
		return nil, fmt.Errorf("沙盒解析: 脚本退出码 %d（%s）", resp.ExitCode, resp.Stderr)
	}
	return resp, nil
}

// execResponse sandbox /v1/exec 响应（与 sandboxsvc.ExecResult 字段一致）。
type execResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Error      string `json:"error,omitempty"`
}

func (c *Client) post(ctx context.Context, body []byte) (*execResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.BaseURL, "/")+"/v1/exec", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("沙盒服务不可达（%s）: %v", c.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("沙盒服务异常: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out execResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("沙盒响应解析失败: %v", err)
	}
	return &out, nil
}

// readResult 读取解析脚本产出的统一 JSON。
func readResult(outPath string) (*ParseResult, error) {
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("沙盒解析: 读取解析结果失败: %w", err)
	}
	var result ParseResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("沙盒解析: 产物 JSON 解析失败: %v", err)
	}
	if result.Markdown == "" && !result.ScanOnly && len(result.Media) == 0 {
		return nil, errors.New("沙盒解析: 产物为空")
	}
	return &result, nil
}
