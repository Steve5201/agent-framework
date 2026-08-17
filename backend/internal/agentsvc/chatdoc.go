// chatdoc.go —— 聊天上传文档/图片（模块二）。
//
// 目标：用户在聊天中上传受支持的文档/图片 → 原文件落盘用户工作区、注入一条
// 提示词消息进会话历史，智能体根据会话是否配置对应能力（识图 describe_image /
// 文档解析 read_document）自行决定是否调用工具解析内容后回复。
//
// 流程设计（需求 P2·模型自主调用工具）：上传只做「落盘 + 注入提示词」，系统
// 不再自动解析内容——解析成本、延迟与错误全部交由模型按需触发：
//   - 图片：原图二进制落盘，注入 [图片] <文件名>（已保存至工作区 <路径>）；
//   - 文档：原文件二进制落盘，注入 [文档] <文件名>（已保存至工作区 <路径>）；
//   - 空文件：落盘空文件，注入「文件内容为空」提示（解析器无法处理空输入）。
//
// 解析复用 rag ingest 管线（ingest.Parser，read_document 工具内）：md/txt/
// html/xlsx 原生纯 Go 解析；pdf/docx/pptx 委托沙盒（sandboxclient）。零重复实现。
//
// 安全与成本护栏：类型白名单、大小 ≤50MB、每会话文档数上限。
package agentsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"go.uber.org/zap"
)

const (
	// defaultChatDocSize 单文档大小默认上限（50MB）。
	// 可用 AGENT_CHAT_DOC_MAX_SIZE_MB 覆盖（经 Service.maxChatDocSize 生效）。
	defaultChatDocSize = 50 << 20
	// defaultChatDocInjectRunes 解析工具单次返回正文默认上限（read_document 缺省
	// 截断）。可用 AGENT_CHAT_DOC_INJECT_RUNES 覆盖（Service.maxChatDocInjectRunes）。
	defaultChatDocInjectRunes = 8000
	// defaultChatDocsPerSession 每会话文档数量默认上限（防滥用）。
	// 可用 AGENT_CHAT_DOCS_PER_SESSION 覆盖（Service.maxChatDocsPerSession）。
	defaultChatDocsPerSession = 20
	// chatDocMarker 文档注入消息前缀（UI 特殊渲染 / 数量统计定位用）。
	chatDocMarker = "[文档]"
	// chatImageMarker 图片注入消息前缀（UI 渲染图片）。
	chatImageMarker = "[图片]"
	// chatDocKindDoc / chatDocKindImage 上传结果类型标记（前端渲染分流）。
	chatDocKindDoc   = "doc"
	chatDocKindImage = "image"
)

// chatDocTypes 支持的文件类型白名单（文档走 read_document 工具 ingest 解析
// 管线；图片走 describe_image 工具视觉解析——均由模型按需调用，见 tools.go）。
var chatDocTypes = map[string]string{
	".md":       "md",
	".markdown": "md",
	".txt":      "txt",
	".html":     "html",
	".htm":      "html",
	".xlsx":     "xlsx",
	".pdf":      "pdf",
	".docx":     "docx",
	".pptx":     "pptx",
	// 图片（视觉解析预留）：
	".png":  "image",
	".jpg":  "image",
	".jpeg": "image",
	".gif":  "image",
	".webp": "image",
	".bmp":  "image",
	".svg":  "image",
}

// ChatDocResult 上传文档的处理结果（响应给前端）。
type ChatDocResult struct {
	FileName string `json:"file_name"`
	// Kind 上传类型：doc（文档）| image（图片）。
	Kind string `json:"kind"`
	// Url 图片渲染地址（相对服务器根，如 /files/users/...；仅 image 类型有）。
	Url         string   `json:"url,omitempty"`
	RelPath     string   `json:"rel_path"`     // 相对工作区根的展示路径（/ 分隔）
	Segments    int      `json:"segments"`     // 兼容字段：系统不再自动解析，恒为 0
	InjectedLen int      `json:"injected_len"` // 兼容字段：系统不再注入正文，恒为 0
	Media       []string `json:"media,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// effectiveWorkRoot 返回用户工作区根；配置为空时回退进程工作目录
// （容器内 /app = 沙盒 /work，见 docker-compose 共享卷）。
func (s *Service) effectiveWorkRoot() string {
	if s.workRoot != "" {
		return s.workRoot
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// UploadChatDocument 上传文档供智能体理解（模块二）。
// 属主校验同会话；解析、落盘、注入三步任一失败即返回错误，不产生半成品。
func (s *Service) UploadChatDocument(ctx context.Context, userID, sessionID int64, fileName string, data []byte) (*ChatDocResult, error) {
	fileName = strings.TrimSpace(filepath.Base(fileName))
	// Base 只取末级组件，防御路径穿越；"." / ".." 特殊条目单独拒绝。
	if fileName == "" || fileName == "." || fileName == ".." || fileName == string(filepath.Separator) {
		return nil, apperr.New(apperr.CodeInvalidArgument, "文件名非法")
	}
	// 空文件（0 字节）不做全局拒绝：空文档走 uploadEmptyDoc（注入"内容为空"
	// 提示），空图片正常落盘（Describe 失败自动降级）。大小上限仍生效。
	if len(data) > s.maxChatDocSize {
		return nil, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("文档大小超出上限（≤%dMB）", s.maxChatDocSize>>20))
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	fileType, ok := chatDocTypes[ext]
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidArgument, "不支持的文件类型（支持 md/txt/html/xlsx/pdf/docx/pptx 及常见图片）")
	}

	// 属主校验（含会话域与生效配置）。
	if _, err := s.getOwnedSession(ctx, userID, sessionID); err != nil {
		return nil, err
	}

	// 每会话文档数量上限。
	n, err := s.chatDocCount(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if n >= s.maxChatDocsPerSession {
		return nil, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("本会话文档数量已达上限（≤%d 份）", s.maxChatDocsPerSession))
	}

	// 图片：只落盘 + 注入提示词，系统不自动调用视觉解析。智能体在会话配置了
	// 「识图」能力时拥有 describe_image 工具，由模型自行决定是否解析后回复
	// （需求 P2·模型自主调用工具）。
	if fileType == "image" {
		return s.uploadChatImage(ctx, userID, sessionID, fileName, data)
	}

	// 空文档（0 字节）：不进解析管线（解析器无法处理空输入），落盘空文件 +
	// 注入"文件内容为空"提示，让模型知道"文件是空的"，而不是解析失败报错（需求 4）。
	if len(data) == 0 {
		return s.uploadEmptyDoc(ctx, userID, sessionID, fileName)
	}

	// 文档：原样落盘用户工作区 users/<uid>/chat-files/<sid>/<file>（与 file_ops
	// 共享同一工作区根，模型可经 file_ops 读取；前端经 /files 静态端点下载）。
	// 不做系统自动解析——智能体在会话配置了「文档解析」能力时拥有 read_document
	// 工具，由模型自行决定是否读取正文后回复（需求 P2·模型自主调用工具）。
	rel := filepath.Join("users", strconv.FormatInt(userID, 10),
		"chat-files", strconv.FormatInt(sessionID, 10), fileName)
	full := filepath.Join(s.effectiveWorkRoot(), rel)
	if err := ensureGroupWritableDir(filepath.Dir(full)); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "创建工作区目录失败", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "写入用户工作区失败", err)
	}

	// 注入提示词（新轮次）进会话历史，后续轮次可持续追问。
	// 注意：注入消息里的路径统一用"全局相对路径"（含 users/<uid>/ 前缀），
	// 与 /files 渲染协议、file_ops 展示路径、read_document 解析路径保持同一约定，
	// 避免模型对前缀来源产生混淆（曾把 users/62/chat-files/… 错拼成 /files/chat-files/…）。
	content := fmt.Sprintf("%s %s（已保存至工作区 %s）",
		chatDocMarker, fileName, filepath.ToSlash(rel))
	if err := s.injectDocMessage(ctx, sessionID, content); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "写入会话历史失败", err)
	}

	result := &ChatDocResult{
		FileName: fileName,
		Kind:     chatDocKindDoc,
		RelPath:  filepath.ToSlash(rel),
	}
	s.log.Info("chat document uploaded",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.String("file", fileName),
	)
	return result, nil
}

// uploadChatImage 图片上传（识图作为智能体能力）。
//
// 流程：原图落盘用户工作区 users/<uid>/chat-files/<sid>/<file>（前端经 /files
// 渲染）→ 注入 [图片] 标记消息。系统不自动调用视觉解析；智能体在会话配置了
// 「识图」能力时拥有 describe_image 工具，由模型自行决定是否解析图片后回复
// （需求 P2·模型自主调用工具）。Describe 失败/未启用不影响上传与渲染。
func (s *Service) uploadChatImage(ctx context.Context, userID, sessionID int64, fileName string, data []byte) (*ChatDocResult, error) {
	rel := filepath.Join("users", strconv.FormatInt(userID, 10),
		"chat-files", strconv.FormatInt(sessionID, 10), fileName)
	full := filepath.Join(s.effectiveWorkRoot(), rel)
	if err := ensureGroupWritableDir(filepath.Dir(full)); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "创建工作区目录失败", err)
	}
	// 原图二进制原样落盘，不经过任何解析管线。
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "写入用户工作区失败", err)
	}

	// 注入消息路径统一用全局相对（含 users/<uid>/ 前缀），与 /files 渲染协议、
	// file_ops 展示路径、describe_image 解析路径同一约定（同 chatdoc 文档注入）。
	content := fmt.Sprintf("%s %s（已保存至工作区 %s）",
		chatImageMarker, fileName, filepath.ToSlash(rel))
	if err := s.injectDocMessage(ctx, sessionID, content); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "写入会话历史失败", err)
	}

	s.log.Info("chat image uploaded",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.String("file", fileName),
	)
	return &ChatDocResult{
		FileName: fileName,
		Kind:     chatDocKindImage,
		Url:      "/files/" + filepath.ToSlash(rel),
		RelPath:  filepath.ToSlash(rel),
	}, nil
}

// uploadEmptyDoc 处理空文档（0 字节）：不进入解析管线（避免解析器报错 /
// "无有效正文"拒绝），直接落盘空文件并注入"文件内容为空"提示，让模型
// 知道文件是空的而不是解析失败（需求 4）。
func (s *Service) uploadEmptyDoc(ctx context.Context, userID, sessionID int64, fileName string) (*ChatDocResult, error) {
	rel := filepath.Join("users", strconv.FormatInt(userID, 10),
		"chat-files", strconv.FormatInt(sessionID, 10), fileName)
	full := filepath.Join(s.effectiveWorkRoot(), rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "创建工作区目录失败", err)
	}
	if err := os.WriteFile(full, nil, 0o644); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "写入用户工作区失败", err)
	}
	content := fmt.Sprintf("%s %s（文件内容为空，无解析内容）", chatDocMarker, fileName)
	if err := s.injectDocMessage(ctx, sessionID, content); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "写入会话历史失败", err)
	}
	s.log.Info("chat empty document uploaded",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.String("file", fileName),
	)
	return &ChatDocResult{
		FileName: fileName,
		Kind:     chatDocKindDoc,
		RelPath:  filepath.ToSlash(rel),
		Segments: 0,
	}, nil
}

// imageMimeFor 由扩展名映射图片 MIME（视觉 data URL 需要）。
var imageMimeByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".svg":  "image/svg+xml",
}

// imageMimeFor 返回文件对应的 MIME；未知扩展回退 octet-stream。
func imageMimeFor(fileName string) string {
	if m, ok := imageMimeByExt[strings.ToLower(filepath.Ext(fileName))]; ok {
		return m
	}
	return "application/octet-stream"
}

// injectDocMessage 把文档内容作为新轮次的 user 消息写入历史。
func (s *Service) injectDocMessage(ctx context.Context, sessionID int64, content string) error {
	round, err := s.repo.MaxRoundNo(ctx, sessionID)
	if err != nil {
		return err
	}
	return s.repo.AppendMessages(ctx, sessionID, []*Message{
		{Role: "user", Content: content, RoundNo: round + 1},
	})
}

// chatDocCount 统计会话中已注入的文档消息数量（配额校验用）。
func (s *Service) chatDocCount(ctx context.Context, sessionID int64) (int, error) {
	msgs, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && (strings.HasPrefix(m.Content, chatDocMarker) || strings.HasPrefix(m.Content, chatImageMarker)) {
			n++
		}
	}
	return n, nil
}

// truncateRunes 按字符数截断文本，超长追加提示（指向工作区文件）。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("\n……（文档内容过长，仅截取前 %d 字符，完整内容见工作区文件）", n)
}
