// Package adminsvc 实现管理端（admin panel）的"文件态配置平面"。
//
// 定位：管理端是配置即文件的管理层——技能直接落盘到技能目录
// （<skills>/<name>/SKILL.md），MCP server 列表写入 JSON 配置文件；
// agent 通过 fsnotify 监听上述路径热加载，保存即生效、免重启。
//
// 模块化设计：每个功能模块实现 Module 接口后注册到 NewService。
// 新增模块 = 新增一个实现 + 注册一行，只增不改，不影响已有模块；
// 未实现模块用 PlaceholderModule 占位，前端可据此渲染"规划中"状态。
//
// 鉴权说明：本包不校验"是否管理员"（由 gateway 的 RequireAdmin 中间件在
// /v1/admin/* 路由上强制校验，解析 JWT 的 role 声明）。但阶段3·多租户的
// "资源域"隔离在本包内强制：agentScopeFor 按角色解析资源归属——超管显式
// 指定、智能体管理员锁定自身归属，防越权访问其它智能体组的资源。
package adminsvc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/identity"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// maxBodyBytes 管理端请求体上限（16MB，防超大 body 撑爆内存）。
const maxBodyBytes = 16 << 20

// 管理端上传限制内置默认（P4-L：经 Config.KbUploadMaxMB / SkillUploadMaxMB
// 从 ADMIN_KB_UPLOAD_MAX_MB / ADMIN_SKILL_UPLOAD_MAX_MB 注入，0 = 用默认）。
const (
	defaultKBUploadMaxMB    = 50 // 知识库文档上传单文件上限（MB）
	defaultSkillUploadMaxMB = 50 // 技能/MCP zip 上传上限（MB）
)

// defaultAgentID 默认智能体域（资源归属兜底）。
// 与 authsvc 播种的默认智能体 tutor、rag.DefaultAgentID 保持一致——三者任一
// 变更必须同步（多租户的资源隔离边界依赖该值恒定）。
const defaultAgentID = "tutor"

// agentIDRe 资源域 ID 白名单（与 authsvc 的 agent ID 规则一致）。
// 域 ID 会拼入文件路径（<root>/<agentID>/...），白名单天然排除路径分隔符与
// 父目录引用 → 防目录穿越。
var agentIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Module 管理端功能模块契约。所有模块（含占位）都实现本接口。
type Module interface {
	// Key 模块标识（唯一，同时作为 REST 前缀 /v1/admin/<key>）。
	Key() string
	// Name 模块显示名（侧边栏展示）。
	Name() string
	// Description 模块一句话说明。
	Description() string
	// Implemented 是否已实现；false = 占位模块（前端渲染"规划中"）。
	Implemented() bool
	// Register 注册模块自己的路由；占位模块为空实现。
	Register(mux *http.ServeMux, s *Service)
}

// Config Service 构造配置。
type Config struct {
	// SkillsDir 技能根目录（写技能文件）。空 = 工作目录下 skills/。
	SkillsDir string
	// McpConfigFile MCP server 列表 JSON 文件路径。空 = 工作目录下 mcp_servers.json。
	McpConfigFile string
	// McpServersDir 上传的"本地 MCP 代码"存放目录。空 = 工作目录下 mcp-servers/。
	McpServersDir string
	// Rag rag-service gRPC 客户端（P3-A 知识库模块）。
	// 知识库是"数据库态"数据，经 gRPC 走 rag-service，不落本地文件。
	Rag ragv1.RagServiceClient
	// Auth auth-service gRPC 客户端（用户管理模块：创建/查询用户）。
	Auth authpb.AuthServiceClient
	// Agent agent-service gRPC 客户端（数据管理模块：会话活跃度统计）。
	Agent agentv1.AgentServiceClient
	// LlmGatewayBaseURL llm-gateway HTTP 基址（P2-AI 用量按智能体聚合查询；
	// 空 = 用量接口返回 503 提示未配置，不影响其它管理功能）。
	LlmGatewayBaseURL string
	// LlmAdminToken llm-gateway 模型管理端点令牌（LLM_ADMIN_TOKEN，P3 大模型
	// 管理；与 llm-gateway 共享同一环境变量，空 = 模型管理端点返回 503）。
	LlmAdminToken string
	// LogsDir 操作审计日志根目录（按智能体域分目录落盘）。
	// 空 = 工作目录下 admin-logs/。
	LogsDir string
	// KbUploadMaxMB 知识库文档上传单文件大小上限 MB（ADMIN_KB_UPLOAD_MAX_MB，
	// 默认 20；0 = 内置默认）。
	KbUploadMaxMB int
	// SkillUploadMaxMB 技能/MCP zip 上传大小上限 MB（ADMIN_SKILL_UPLOAD_MAX_MB，
	// 默认 10；0 = 内置默认）。
	SkillUploadMaxMB int
	// Log 日志器（可空，空则静默）。
	Log *zap.Logger
}

// Service 管理端服务：持有各模块存储 + 模块清单。
type Service struct {
	skills        *SkillStore
	mcp           *McpStore
	rag           ragv1.RagServiceClient
	auth          authpb.AuthServiceClient
	agent         agentv1.AgentServiceClient // 数据管理模块：会话统计（agent-service）
	audit         *AuditStore
	modules       []Module
	log           *zap.Logger
	llmURL        string // llm-gateway HTTP 基址（用量聚合查询；空 = 未配置）
	llmAdminToken string // llm-gateway 模型管理端点令牌（空 = 模型管理禁用）
	http          *http.Client
	kbMaxBytes    int // 知识库文档上传单文件上限（字节）
	skillMaxBytes int // 技能/MCP zip 上传上限（字节）
}

// NewService 创建管理端服务。
func NewService(cfg Config) (*Service, error) {
	if cfg.SkillsDir == "" {
		cfg.SkillsDir = "skills"
	}
	if cfg.McpConfigFile == "" {
		cfg.McpConfigFile = "mcp_servers.json"
	}
	if cfg.McpServersDir == "" {
		cfg.McpServersDir = "mcp-servers"
	}
	// 上传限制缺省回退（P4-L 收口 env）：0 = 内置默认。
	if cfg.KbUploadMaxMB <= 0 {
		cfg.KbUploadMaxMB = defaultKBUploadMaxMB
	}
	if cfg.SkillUploadMaxMB <= 0 {
		cfg.SkillUploadMaxMB = defaultSkillUploadMaxMB
	}
	// 技能/MCP zip 共享同一上限（50MB 默认）。
	skillBytes := int64(cfg.SkillUploadMaxMB) << 20
	s := &Service{
		skills:        newSkillStore(cfg.SkillsDir),
		mcp:           newMcpStore(cfg.McpConfigFile, cfg.McpServersDir),
		rag:           cfg.Rag,
		auth:          cfg.Auth,
		agent:         cfg.Agent,
		audit:         newAuditStore(cfg.LogsDir, cfg.Log),
		log:           cfg.Log,
		llmURL:        cfg.LlmGatewayBaseURL,
		llmAdminToken: cfg.LlmAdminToken,
		http:          &http.Client{Timeout: 8 * time.Second},
		kbMaxBytes:    cfg.KbUploadMaxMB << 20,
		skillMaxBytes: cfg.SkillUploadMaxMB << 20,
	}
	// 存储层 zip 上限与 Service 层保持一致（For 派生子存储继承该值）。
	s.skills.maxBytes = skillBytes
	s.mcp.maxBytes = skillBytes
	// 模块清单：按业务层级排序 —— 核心实体（agents）→ 能力配置（skills/kb/mcp）
	// → 账号与基础设施（users/models）→ 观测分析（data/logs）。
	// 新模块在此追加一行即可，不影响既有模块。
	// 阶段3·多租户：智能体管理仅最高超管可见，用户管理/大模型管理仅超管类
	// 角色可见，模块级可见性由 handleListModules 按角色裁剪（ModuleVisible）。
	s.modules = []Module{
		newAgentsModule(s),
		newSkillsModule(s),
		newKBModule(s),
		newMcpModule(s),
		newUsersModule(s),
		newModelsModule(s),
		newDataModule(s),
		logsModule{},
	}
	return s, nil
}

// Modules 返回模块清单（供 GET /v1/admin/modules）。
func (s *Service) Modules() []Module { return s.modules }

// RegisterRoutes 注册管理端全部路由（/v1/admin/*）。
// 由 gateway 调用；管理员角色校验由 gateway 中间件完成。
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	// 模块清单（管理端首页/侧边栏渲染"已实现/规划中"状态）。
	mux.HandleFunc("GET /v1/admin/modules", s.handleListModules)
	for _, m := range s.modules {
		m.Register(mux, s)
	}
}

// ModuleVisible 判断模块对某角色是否可见（阶段3·管理员分层）：
//   - agents（智能体管理）/ data（数据管理）/ models（大模型管理）：仅最高
//     超管 super_admin。大模型管理与智能体管理同级：可读写 API Key 等全局
//     基础设施配置，且经 llm-gateway 直接影响请求路由，不向组级管理员开放；
//   - users（用户管理）：super_admin + agent_admin（组内账号管理）；
//   - skills/mcp/kb：所有管理员（资源域内按智能体隔离，见各模块）。
func ModuleVisible(key, role string) bool {
	switch key {
	case "agents", "data", "models":
		return role == "super_admin"
	case "users":
		return role == "super_admin" || role == "agent_admin"
	default:
		return true
	}
}

// handleListModules 列出当前角色可见的管理端模块及其实现状态。
func (s *Service) handleListModules(w http.ResponseWriter, r *http.Request) {
	role := identity.Role(r.Context())
	out := make([]map[string]any, 0, len(s.modules))
	for _, m := range s.modules {
		if !ModuleVisible(m.Key(), role) {
			continue
		}
		out = append(out, map[string]any{
			"key":         m.Key(),
			"name":        m.Name(),
			"description": m.Description(),
			"implemented": m.Implemented(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": out})
}

// adminCtx 从请求 context 提取管理员身份并注入出站 gRPC metadata。
// adminsvc 与 gateway 同进程：gateway 的 RequireAuth 已将 user_id 写入
// context（identity 包），此处仅做提取 + 透传，下游（auth-service）自行校验。
func adminCtx(r *http.Request) (context.Context, bool) {
	uid, ok := identity.UserID(r.Context())
	if !ok {
		return nil, false
	}
	return identity.OutgoingMetadata(r.Context(), uid), true
}

// agentScopeFor 解析当前请求的资源域（阶段3·多租户资源隔离）。
// 所有资源模块（skill/mcp/kb）的 handler 必须先用它解析归属域，再操作资源：
//
//   - super_admin（最高超管，无智能体归属）：取请求显式指定的 agent_id
//     （前端下拉）；缺省回退默认域（tutor，与存量数据一致）。
//   - agent_admin / admin：锁定 JWT 携带的自身归属（identity.AgentID），
//     忽略请求参数——防"声明管 A 智能体、实际操作 B 智能体"的越权。
//   - 其它角色（游客/普通用户，理论上到不了管理端）：拒绝。
//
// 返回的 agentID 已通过白名单校验，可直接拼入文件路径/传给下游 RPC。
func agentScopeFor(r *http.Request, requested string) (string, error) {
	role := identity.Role(r.Context())
	var agent string
	switch role {
	case "super_admin":
		agent = strings.TrimSpace(requested)
		// 超管的全门户标识 '*'（统一标签模型，见 authsvc allAgentID）仅用于
		// 聊天域切换，不是可管理的资源域：这里回退默认域，避免构造请求时
		// 被下方 agentIDRe 白名单拒绝。
		if agent == "*" {
			agent = ""
		}
	case "agent_admin", "admin":
		agent = identity.AgentID(r.Context())
	default:
		return "", apperr.New(apperr.CodePermissionDenied, "无资源管理权限")
	}
	if agent == "" {
		agent = defaultAgentID
	}
	if !agentIDRe.MatchString(agent) {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的智能体 ID（仅限字母/数字/中划线，≤64 字符）")
	}
	return agent, nil
}

// ---------------------------------------------------------------------------
// 通用辅助（响应 / 解析）
// ---------------------------------------------------------------------------

// writeJSON 写出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 写出统一错误体（与 gateway 同构：code/message/request_id）。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, body := apperr.HTTPBody(err)
	if body["request_id"] == "" {
		body["request_id"] = apperr.RequestIDFromContext(r.Context())
	}
	writeJSON(w, status, body)
}

// decodeJSON 读取并解析 JSON 请求体（限长，防滥用）。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "请求体 JSON 解析失败")
	}
	return nil
}
