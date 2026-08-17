package adminsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/tools/mcp"
)

// mcpNameRe MCP server 名合法字符（与技能名同规则：字母/数字/下划线/连字符）。
var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,49}$`)

// ---------------------------------------------------------------------------
// 存储层
// ---------------------------------------------------------------------------

// McpStore 文件态 MCP server 配置存储：整份配置 = 一个 JSON 文件
// （与 agent 的 AGENT_MCP_CONFIG_FILE 指向同一文件，agent 监听它热加载）。
type McpStore struct {
	file       string
	serversDir string     // 上传的"本地 MCP 代码"存放目录
	maxBytes   int64      // zip 上传/解压单文件大小上限（字节，P4-L env 可配）
	mu         sync.Mutex // 读改写全程串行，防止并发覆盖丢失
}

// newMcpStore 创建 MCP 配置存储。
func newMcpStore(file, serversDir string) *McpStore {
	return &McpStore{file: file, serversDir: serversDir, maxBytes: int64(defaultSkillUploadMaxMB) << 20}
}

// For 返回指定智能体域的子存储（多租户隔离）：
//   - 配置：<McpConfigFile 目录>/<agentID>/<McpConfigFile 文件名>
//   - 代码：<serversDir>/<agentID>/
//
// agentID 须已通过 agentScopeFor 的白名单校验（防目录穿越）；空值视同默认域。
func (s *McpStore) For(agentID string) *McpStore {
	if agentID == "" {
		agentID = defaultAgentID
	}
	return &McpStore{
		file:       filepath.Join(filepath.Dir(s.file), agentID, filepath.Base(s.file)),
		serversDir: filepath.Join(s.serversDir, agentID),
		maxBytes:   s.maxBytes,
	}
}

// List 读取全部 MCP server 配置。文件不存在或为空 = 空列表；
// 文件内容损坏（非法 JSON）返回明确错误（需要人工修复或重建）。
func (s *McpStore) List(_ context.Context) ([]mcp.ServerConfig, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, apperr.Wrap(apperr.CodeInternal, "读取 MCP 配置文件失败", err)
	}
	cfgs, err := mcp.ParseServersJSON(data)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "MCP 配置文件损坏: "+err.Error(), err)
	}
	sort.Slice(cfgs, func(i, j int) bool { return cfgs[i].Name < cfgs[j].Name })
	return cfgs, nil
}

// Get 获取单个 server 配置；不存在返回 404。
func (s *McpStore) Get(_ context.Context, name string) (mcp.ServerConfig, error) {
	if !mcpNameRe.MatchString(name) {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument, "MCP server 名不合法")
	}
	cfgs, err := s.List(context.Background())
	if err != nil {
		return mcp.ServerConfig{}, err
	}
	for _, c := range cfgs {
		if c.Name == name {
			return c, nil
		}
	}
	return mcp.ServerConfig{}, apperr.New(apperr.CodeNotFound, "MCP server 不存在")
}

// Create 新增 MCP server（名字冲突返回 409）。
func (s *McpStore) Create(_ context.Context, cfg mcp.ServerConfig) (mcp.ServerConfig, error) {
	if err := validateMcpConfig(&cfg); err != nil {
		return mcp.ServerConfig{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfgs, err := s.readAll()
	if err != nil {
		return mcp.ServerConfig{}, err
	}
	for i := range cfgs {
		if cfgs[i].Name == cfg.Name {
			return mcp.ServerConfig{}, apperr.New(apperr.CodeAlreadyExists, "MCP server 已存在")
		}
	}
	if err := s.save(append(cfgs, cfg)); err != nil {
		return mcp.ServerConfig{}, err
	}
	return cfg, nil
}

// Update 全量替换某个 server 的配置；不存在返回 404。
// server 名以路径为准（name 不允许变更，避免工具名前缀漂移）。
func (s *McpStore) Update(_ context.Context, name string, cfg mcp.ServerConfig) (mcp.ServerConfig, error) {
	if !mcpNameRe.MatchString(name) {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument, "MCP server 名不合法")
	}
	if cfg.Name != "" && cfg.Name != name {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument,
			"server name 不允许修改（删除后重建）")
	}
	cfg.Name = name
	if err := validateMcpConfig(&cfg); err != nil {
		return mcp.ServerConfig{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfgs, err := s.readAll()
	if err != nil {
		return mcp.ServerConfig{}, err
	}
	idx := -1
	for i := range cfgs {
		if cfgs[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeNotFound, "MCP server 不存在")
	}
	cfgs[idx] = cfg
	if err := s.save(cfgs); err != nil {
		return mcp.ServerConfig{}, err
	}
	return cfg, nil
}

// Delete 删除某个 MCP server；不存在返回 404。
func (s *McpStore) Delete(_ context.Context, name string) error {
	if !mcpNameRe.MatchString(name) {
		return apperr.New(apperr.CodeInvalidArgument, "MCP server 名不合法")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfgs, err := s.readAll()
	if err != nil {
		return err
	}
	kept := cfgs[:0]
	found := false
	for _, c := range cfgs {
		if c.Name == name {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return apperr.New(apperr.CodeNotFound, "MCP server 不存在")
	}
	return s.save(kept)
}

// SetEnabled 启用/禁用某个 MCP server（enabled=false 时 agent 不注册其工具）。
//
// 启用是"真实动作"：会实际连接 server 并 tools/list 发现工具——连接失败返回错误
// （不启用），成功则把发现的工具名与调用方返回的 cfg 一起持久化。
func (s *McpStore) SetEnabled(ctx context.Context, name string, enabled bool) (mcp.ServerConfig, error) {
	if !mcpNameRe.MatchString(name) {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument, "MCP server 名不合法")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfgs, err := s.readAll()
	if err != nil {
		return mcp.ServerConfig{}, err
	}
	idx := -1
	for i := range cfgs {
		if cfgs[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeNotFound, "MCP server 不存在")
	}
	e := enabled
	cfgs[idx].Enabled = &e
	if enabled {
		// 启用必须真实连通：连接/发现失败 → 不启用（保持禁用）并返回错误。
		if _, derr := s.discoverLocked(ctx, &cfgs[idx]); derr != nil {
			disabled := false
			cfgs[idx].Enabled = &disabled // 未连通 → 明确禁用，避免"默认启用"歧义
			cfgs[idx].DiscoveryError = derr.Error()
			cfgs[idx].DiscoveredTools = nil
			_ = s.save(cfgs) // 尽力持久化失败状态（失败也不阻塞返回错误）
			return mcp.ServerConfig{}, apperr.Wrap(apperr.CodeInvalidArgument,
				"启用失败：无法连接 MCP server（"+derr.Error()+"）", derr)
		}
		cfgs[idx].DiscoveryError = ""
	}
	if err := s.save(cfgs); err != nil {
		return mcp.ServerConfig{}, err
	}
	return cfgs[idx], nil
}

// TestConnection 测试连接 MCP server：实际连接并发现工具，结果（工具列表/错误）
// 持久化到配置并返回。不改变启用状态。
func (s *McpStore) TestConnection(ctx context.Context, name string) (mcp.ServerConfig, []mcp.ToolInfo, error) {
	if !mcpNameRe.MatchString(name) {
		return mcp.ServerConfig{}, nil, apperr.New(apperr.CodeInvalidArgument, "MCP server 名不合法")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfgs, err := s.readAll()
	if err != nil {
		return mcp.ServerConfig{}, nil, err
	}
	idx := -1
	for i := range cfgs {
		if cfgs[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return mcp.ServerConfig{}, nil, apperr.New(apperr.CodeNotFound, "MCP server 不存在")
	}
	tools, derr := s.discoverLocked(ctx, &cfgs[idx])
	if derr != nil {
		cfgs[idx].DiscoveryError = derr.Error()
		cfgs[idx].DiscoveredTools = nil
	} else {
		cfgs[idx].DiscoveryError = ""
		cfgs[idx].DiscoveredTools = tools
	}
	if err := s.save(cfgs); err != nil {
		return mcp.ServerConfig{}, nil, err
	}
	if derr != nil {
		return cfgs[idx], tools, apperr.Wrap(apperr.CodeInvalidArgument, "连接失败： "+derr.Error(), derr)
	}
	return cfgs[idx], tools, nil
}

// discoverLocked 实际连接 server 并发现工具（写操作期间持有锁时调用）。
// 连接失败返回错误（DiscoverTools 内部已关闭连接）。
func (s *McpStore) discoverLocked(ctx context.Context, cfg *mcp.ServerConfig) ([]mcp.ToolInfo, error) {
	probe := *cfg // 拷贝，避免把兜底超时写回持久化配置
	if probe.TimeoutSeconds <= 0 {
		probe.TimeoutSeconds = 30
	}
	dc, cancel := context.WithTimeout(ctx, time.Duration(probe.TimeoutSeconds)*time.Second)
	defer cancel()
	return mcp.DiscoverTools(dc, &probe)
}

// readAll 读取当前配置（不含排序，供写操作使用）。
// 文件不存在 = 空列表。
func (s *McpStore) readAll() ([]mcp.ServerConfig, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, apperr.Wrap(apperr.CodeInternal, "读取 MCP 配置文件失败", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	cfgs, err := mcp.ParseServersJSON(data)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "MCP 配置文件损坏: "+err.Error(), err)
	}
	return cfgs, nil
}

// save 把配置整体写回文件（JSON 数组，原子替换；并发安全由调用方持锁保证）。
func (s *McpStore) save(cfgs []mcp.ServerConfig) error {
	if cfgs == nil {
		cfgs = []mcp.ServerConfig{}
	}
	data, err := json.MarshalIndent(cfgs, "", "  ")
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "MCP 配置序列化失败", err)
	}
	if dir := filepath.Dir(s.file); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return apperr.Wrap(apperr.CodeInternal, "创建 MCP 配置目录失败", err)
		}
	}
	if err := atomicWrite(s.file, data); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "写入 MCP 配置文件失败", err)
	}
	return nil
}

// validateMcpConfig 走标准解析校验（缺省补全 + 必填项/枚举校验），
// 并把归一化后的配置写回（如 transport 缺省 → stdio）。
func validateMcpConfig(cfg *mcp.ServerConfig) error {
	if !mcpNameRe.MatchString(cfg.Name) {
		return apperr.New(apperr.CodeInvalidArgument,
			"server name 仅支持字母/数字/下划线/连字符，且首字符须为字母或数字（1~50 字符）")
	}
	raw, err := json.Marshal([]mcp.ServerConfig{*cfg})
	if err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "MCP 配置序列化失败")
	}
	list, err := mcp.ParseServersJSON(raw)
	if err != nil {
		return apperr.New(apperr.CodeInvalidArgument, err.Error())
	}
	*cfg = list[0]
	return nil
}

// atomicWrite 原子写文件：先写临时文件再改名。
// Linux 上 os.Rename 直接覆盖（原子）；Windows 上目标存在时先删后改。
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows：目标已存在时 rename 会失败，先删目标再重试。
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			_ = os.Remove(tmp)
			return err2
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 模块层（路由 + HTTP handlers）
// ---------------------------------------------------------------------------

// mcpModule MCP 管理模块。
type mcpModule struct{ s *Service }

func newMcpModule(s *Service) Module { return mcpModule{s: s} }

func (m mcpModule) Key() string  { return "mcp" }
func (m mcpModule) Name() string { return "MCP 管理" }
func (m mcpModule) Description() string {
	return "配置外部 MCP Server（stdio/http），保存后 agent 热加载生效"
}
func (m mcpModule) Implemented() bool { return true }

func (m mcpModule) Register(mux *http.ServeMux, _ *Service) {
	mux.HandleFunc("GET /v1/admin/mcp-servers", m.s.handleListMcp)
	mux.HandleFunc("POST /v1/admin/mcp-servers", m.s.handleCreateMcp)
	mux.HandleFunc("POST /v1/admin/mcp-servers/test", m.s.handleTestMcpConfig)
	mux.HandleFunc("POST /v1/admin/mcp-servers/upload", m.s.handleUploadMcp)
	mux.HandleFunc("GET /v1/admin/mcp-servers/{name}", m.s.handleGetMcp)
	mux.HandleFunc("PUT /v1/admin/mcp-servers/{name}", m.s.handleUpdateMcp)
	mux.HandleFunc("PATCH /v1/admin/mcp-servers/{name}/enabled", m.s.handleSetMcpEnabled)
	mux.HandleFunc("POST /v1/admin/mcp-servers/{name}/test", m.s.handleTestMcp)
	mux.HandleFunc("DELETE /v1/admin/mcp-servers/{name}", m.s.handleDeleteMcp)
}

func (s *Service) handleListMcp(w http.ResponseWriter, r *http.Request) {
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cfgs, err := s.mcp.For(agent).List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": cfgs, "agent_id": agent})
}

func (s *Service) handleCreateMcp(w http.ResponseWriter, r *http.Request) {
	var cfg mcp.ServerConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, r, err)
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	created, err := s.mcp.For(agent).Create(r.Context(), cfg)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("mcp server created", zap.String("agent", agent), zap.String("server", created.Name))
	writeJSON(w, http.StatusCreated, map[string]any{"server": created, "agent_id": agent})
}

func (s *Service) handleGetMcp(w http.ResponseWriter, r *http.Request) {
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cfg, err := s.mcp.For(agent).Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server": cfg, "agent_id": agent})
}

func (s *Service) handleUpdateMcp(w http.ResponseWriter, r *http.Request) {
	var cfg mcp.ServerConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, r, err)
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	updated, err := s.mcp.For(agent).Update(r.Context(), r.PathValue("name"), cfg)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("mcp server updated", zap.String("agent", agent), zap.String("server", updated.Name))
	writeJSON(w, http.StatusOK, map[string]any{"server": updated, "agent_id": agent})
}

func (s *Service) handleDeleteMcp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.mcp.For(agent).Delete(r.Context(), name); err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("mcp server deleted", zap.String("agent", agent), zap.String("server", name))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSetMcpEnabled(w http.ResponseWriter, r *http.Request) {
	var req setEnabledReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	name := r.PathValue("name")
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cfg, err := s.mcp.For(agent).SetEnabled(r.Context(), name, req.Enabled)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("mcp server enabled changed", zap.String("agent", agent), zap.String("server", name), zap.Bool("enabled", req.Enabled))
	writeJSON(w, http.StatusOK, map[string]any{"server": cfg, "agent_id": agent})
}

// handleTestMcpConfig 测试一段"尚未保存"的 server 配置（表单/JSON 模式点"测试连接"用）。
// 请求体 = ServerConfig；返回 {tools, error}。不持久化、不涉及资源域。
func (s *Service) handleTestMcpConfig(w http.ResponseWriter, r *http.Request) {
	var cfg mcp.ServerConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, r, err)
		return
	}
	// 走标准校验（补全 transport 等缺省值）。
	if err := validateMcpConfig(&cfg); err != nil {
		writeError(w, r, err)
		return
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	dc, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	tools, derr := mcp.DiscoverTools(dc, &cfg)
	if derr != nil {
		s.logInfo("mcp test(unsaved) failed", zap.String("server", cfg.Name), zap.Error(derr))
		writeJSON(w, http.StatusOK, map[string]any{"tools": []string{}, "error": derr.Error()})
		return
	}
	s.logInfo("mcp test(unsaved) ok", zap.String("server", cfg.Name), zap.Int("tools", len(tools)))
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools, "error": ""})
}

// handleTestMcp 测试连接 MCP server：实际连接并发现工具。
// 返回 { server, tools, error }——连接失败返回 400（错误已在 server.discovery_error 持久化）。
func (s *Service) handleTestMcp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cfg, tools, err := s.mcp.For(agent).TestConnection(r.Context(), name)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if cfg.DiscoveryError != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"server": cfg,
			"tools":  tools,
			"error":  cfg.DiscoveryError,
		})
		return
	}
	s.logInfo("mcp server tested", zap.String("agent", agent), zap.String("server", name), zap.Int("tools", len(tools)))
	writeJSON(w, http.StatusOK, map[string]any{"server": cfg, "tools": tools, "error": ""})
}

// handleUploadMcp 上传"本地 MCP 代码"zip 包并注册为 stdio MCP server。
// multipart：file(zip) + name(可选，默认取 zip 文件名) + entry(可选入口文件)。
// 资源域经 agent_id 查询参数指定（超管）；agent_admin/admin 由后端锁定自身归属。
func (s *Service) handleUploadMcp(w http.ResponseWriter, r *http.Request) {
	limit := int64(s.skillMaxBytes)
	r.Body = http.MaxBytesReader(w, r.Body, limit+64<<10)
	if err := r.ParseMultipartForm(limit); err != nil {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument,
			"上传请求不合法（zip 过大或非 multipart 格式）: "+err.Error()))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	entry := strings.TrimSpace(r.FormValue("entry"))
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少文件字段 file"))
		return
	}
	defer file.Close()
	if header.Size > limit {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("zip 超过 %d 字节上限", limit)))
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		writeError(w, r, apperr.Wrap(apperr.CodeInternal, "读取上传文件失败", err))
		return
	}
	// 同名 server 覆盖需显式确认（?overwrite=true），否则返回 409 由前端提示。
	overwrite := r.URL.Query().Get("overwrite") == "true"
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cfg, err := s.mcp.For(agent).UploadLocal(r.Context(), name, entry, header.Filename, data, overwrite)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("local mcp uploaded", zap.String("agent", agent), zap.String("server", cfg.Name), zap.String("entry", header.Filename))
	if s.log != nil {
		s.log.Debug("local mcp structure", zap.String("agent", agent), zap.String("server", cfg.Name),
			zap.String("command", cfg.Command), zap.String("args", strings.Join(cfg.Args, " ")),
			zap.String("cwd", cfg.Cwd))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"server": cfg, "agent_id": agent})
}

// ---------------------------------------------------------------------------
// 本地 MCP 代码上传（把开发好的 MCP 上传到服务器本地运行）
// ---------------------------------------------------------------------------

// UploadLocal 上传并解压本地 MCP 代码包：解压到 serversDir/<name>/，
// 自动定位入口文件（main.py / server.py / mcp_server.py / app.py / index.js 等），
// 注册为 stdio MCP server 配置（command=解释器, args=[入口], cwd=代码目录）。
// 同名已存在时：未显式 overwrite 则返回 409（需前端确认后带 overwrite=true 重试），
// 否则覆盖代码与配置。上传成功后可经"启用/测试连接"验证。
func (s *McpStore) UploadLocal(_ context.Context, name, entry, fileName string, zipData []byte, overwrite bool) (mcp.ServerConfig, error) {
	if len(zipData) == 0 {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument, "上传的 zip 为空")
	}
	if int64(len(zipData)) > s.maxBytes {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument, fmt.Sprintf("zip 超过 %d 字节上限", s.maxBytes))
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(fileName), ".zip")
	}
	if !mcpNameRe.MatchString(name) {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument,
			"server 名仅支持字母/数字/下划线/连字符，且首字符须为字母或数字（1~50 字符）")
	}

	// 同名冲突预检：未显式 overwrite 时拒绝覆盖（此时尚未解压/写盘，无副作用）。
	// 必须在锁内检查，避免与并发写冲突。
	s.mu.Lock()
	defer s.mu.Unlock()
	if !overwrite {
		if cfgs, err := s.readAll(); err != nil {
			return mcp.ServerConfig{}, err
		} else {
			for i := range cfgs {
				if cfgs[i].Name == name {
					return mcp.ServerConfig{}, apperr.New(apperr.CodeAlreadyExists,
						fmt.Sprintf("MCP server「%s」已存在。继续上传将覆盖其代码与配置，是否覆盖？", name))
				}
			}
		}
	}

	if err := os.MkdirAll(s.serversDir, 0o755); err != nil {
		return mcp.ServerConfig{}, apperr.Wrap(apperr.CodeInternal, "创建 MCP 代码目录失败", err)
	}
	tmp, err := os.MkdirTemp(s.serversDir, ".upload-")
	if err != nil {
		return mcp.ServerConfig{}, apperr.Wrap(apperr.CodeInternal, "创建上传临时目录失败", err)
	}
	defer func() { _ = removeDirAll(tmp) }()

	if err := unzipSafe(tmp, zipData, s.maxBytes); err != nil {
		return mcp.ServerConfig{}, err
	}
	codeRoot, relEntry, err := mcpLocateEntry(tmp, entry)
	if err != nil {
		return mcp.ServerConfig{}, err
	}
	interp, ok := interpreterFor(relEntry)
	if !ok {
		return mcp.ServerConfig{}, apperr.New(apperr.CodeInvalidArgument,
			"无法识别 MCP 入口文件：请在 zip 内含 main.py/server.py/mcp_server.py/app.py/index.js 等，或在 entry 字段指定")
	}

	dir := filepath.Join(s.serversDir, name)
	if _, err := os.Stat(dir); err == nil {
		if err := removeDirAll(dir); err != nil {
			return mcp.ServerConfig{}, apperr.Wrap(apperr.CodeInternal, "清理旧 MCP 代码目录失败", err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return mcp.ServerConfig{}, apperr.Wrap(apperr.CodeInternal, "创建 MCP 代码目录失败", err)
	}
	if err := moveDirContents(codeRoot, dir); err != nil {
		return mcp.ServerConfig{}, apperr.Wrap(apperr.CodeInternal, "写入 MCP 代码失败", err)
	}
	// cwd 优先用相对路径（上传代码都在服务器/docker 内）：相对工作目录解析，
	// 与 agent/gateway 进程的 WORKDIR（docker=/app，本地=backend/）保持一致，
	// 保证子进程能读到代码；无法相对化时才退回绝对路径。
	cwd := filepath.Join(s.serversDir, name)
	if wd, werr := os.Getwd(); werr == nil {
		if rel, rerr := filepath.Rel(wd, cwd); rerr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			cwd = rel
		}
	}
	cfg := mcp.ServerConfig{
		Name:              name,
		Transport:         mcp.TransportStdio,
		Command:           interp,
		Args:              []string{relEntry},
		Cwd:               filepath.ToSlash(cwd),
		DefaultPermission: "L2",
	}

	// 读取配置并写入/覆盖当前 server（锁已在函数开头持有，覆盖写是原子替换）。
	cfgs, err := s.readAll()
	if err != nil {
		return mcp.ServerConfig{}, err
	}
	replaced := false
	for i := range cfgs {
		if cfgs[i].Name == name {
			cfgs[i] = cfg
			replaced = true
			break
		}
	}
	if !replaced {
		cfgs = append(cfgs, cfg)
	}
	if err := s.save(cfgs); err != nil {
		return mcp.ServerConfig{}, err
	}
	return cfg, nil
}

// mcpLocateEntry 定位 MCP 入口文件，返回 (代码根目录, 相对代码根的入口路径)。
// explicit 非空时按指定入口（防穿越）；否则在解压树内取"最浅"的候选入口文件。
// 入口路径一律取文件名（相对代码根）——代码根内容整体迁入 serversDir/<name> 后，
// 以 cwd=serversDir/<name> 启动时入口就在当前目录。
func mcpLocateEntry(tmp, explicit string) (string, string, error) {
	if explicit != "" {
		// 与 unzipSafe 一致：先归一正斜杠，避免客户端传入的 Windows 风格
		// 反斜杠路径在 Linux 上被当成单一文件名。
		clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(explicit, "\\", "/")))
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			return "", "", apperr.New(apperr.CodeInvalidArgument, "入口文件路径不合法")
		}
		full := filepath.Join(tmp, clean)
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			return "", "", apperr.New(apperr.CodeInvalidArgument, fmt.Sprintf("入口文件不存在：%s", explicit))
		}
		return filepath.Dir(full), filepath.Base(clean), nil
	}
	candidates := []string{
		"main.py", "server.py", "mcp_server.py", "app.py", "entrypoint.py",
		"index.js", "server.js", "mcp_server.js", "entrypoint.js", "app.js",
	}
	var best struct {
		dir      string
		name     string
		depth    int
		priority int
	}
	best.depth = 1 << 30
	best.priority = 1 << 30
	err := filepath.WalkDir(tmp, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		for i, c := range candidates {
			if d.Name() == c {
				depth := strings.Count(filepath.ToSlash(p), "/")
				if depth < best.depth || (depth == best.depth && i < best.priority) {
					best = struct {
						dir      string
						name     string
						depth    int
						priority int
					}{filepath.Dir(p), c, depth, i}
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		return "", "", apperr.Wrap(apperr.CodeInternal, "扫描 MCP 代码目录失败", err)
	}
	if best.name == "" {
		return "", "", apperr.New(apperr.CodeInvalidArgument,
			"zip 内未找到 MCP 入口文件（main.py/server.py/mcp_server.py/app.py/index.js 等）")
	}
	return best.dir, best.name, nil
}

// interpreterFor 根据入口文件后缀决定运行解释器。
func interpreterFor(entry string) (string, bool) {
	switch strings.ToLower(filepath.Ext(entry)) {
	case ".py":
		return "python3", true
	case ".js", ".mjs", ".cjs":
		return "node", true
	case ".sh":
		return "sh", true
	}
	return "", false
}
