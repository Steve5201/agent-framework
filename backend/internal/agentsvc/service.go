// service.go —— agent-service 业务域实现（P2-42/P2-43/P2-44/P2-47）。
//
// 职责：会话 CRUD + 智能体对话。对话走完整闭环：
//
//	属主校验 → 同会话并发锁 → 加载历史（P2-44）→ framework.Run/RunStream
//	→ 本轮新增消息落库（用户/中间工具对/最终回答）
//
// 上游调用：不直连厂商，统一走 llm-gateway（Provider 由 cmd 注入，
// 通常为指向 llm-gateway 的 OpenAICompatible，密钥收敛在网关一侧）。
package agentsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-backend/internal/config"
	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/rag/sandboxclient"
	"github.com/Steve5201/agent-backend/internal/tools/kb"
	"github.com/Steve5201/agent-backend/internal/tools/mcp"
	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// Service agent-service 业务服务。
type Service struct {
	repo         Repository     // 会话/消息持久化
	provider     llm.Provider   // 上游模型（llm-gateway）
	registryMu   sync.RWMutex   // 保护 registry（管理端热加载时替换）
	registry     *tool.Registry // 工具注册表（会话内共享）
	log          *zap.Logger
	model        string // 默认模型
	systemPrompt string // 系统提示词
	maxRounds    int    // 单次对话最大推理轮数
	maxMessages  int    // 短期记忆窗口

	// sessionLocks 同会话并发锁（P2-47）：Chat 期间锁定整个会话，
	// 防止两个并发请求的"读历史→写入"交错，保证消息 seq 顺序正确。
	// key = sessionID；value = *sync.Mutex。
	sessionLocks sync.Map

	// autoApproveTools 工具审批策略：true = L2/L3 工具自动放行
	// （个人本地使用场景，见 approveToolCall）。
	autoApproveTools bool
	// filesBaseURL 本地媒体 URL 基址（如 http://localhost:8182），
	// 非空时注入渲染协议，让模型输出 <base>/files/<相对路径> 供前端渲染。
	filesBaseURL string
	// domainView 多智能体域资源视图（可选，见 Config.DomainView）。
	domainView DomainViewer

	// workRoot 用户工作区根（模块二·聊天上传文档落盘）。
	workRoot string
	// chatSandbox 聊天文档解析沙盒（pdf/docx/pptx 委托；nil = 仅原生解析）。
	chatSandbox *sandboxclient.Client
	// vision 图片视觉解析（路线 A·描述中转；未配置 VISION_MODEL 时为 NoopVision
	// 占位，图片仅渲染不解析。环境变量装配见 vision.go）。
	vision VisionProvider

	// pendingMu 保护 pending 挂起表（阶段3·外部工具回填）。
	// 结构：sessionID → tool_call_id → 挂起项。StreamChat 结束时整表删除。
	pendingMu sync.Mutex
	pending   map[int64]map[string]*pendingToolCall

	// docLimits 文档生成（render_document）限制（P4-L）；零值 = 内置默认值。
	docLimits config.DocConfig

	// 聊天上传文档限制（模块二，P4-L 收口 env）。
	maxChatDocSize        int // 单文档大小上限（字节）
	maxChatDocsPerSession int // 每会话文档数量上限
	maxChatDocInjectRunes int // 解析工具单次返回正文上限（read_document 缺省截断）

	// 编排子任务韧性（P4-L 收口 env）：超时与失败重试。
	orchSubtaskTimeout time.Duration // 单任务超时（0 = 不限制；默认 30 分钟）
	orchSubtaskRetries int           // 可重试错误的整任务重试次数
}

// Config Service 构造配置。
type Config struct {
	Repo         Repository
	Provider     llm.Provider
	Registry     *tool.Registry
	Log          *zap.Logger
	Model        string
	SystemPrompt string
	MaxRounds    int
	MaxMessages  int

	// AutoApproveTools L2/L3 工具是否自动放行（默认 false=需用户确认）。
	// 个人本地部署置 true：无需审批 UI 即可使用 file_ops/code_executor。
	AutoApproveTools bool
	// FilesBaseURL 本地媒体 URL 基址；空 = 不在提示词中注入 /files 协议。
	FilesBaseURL string
	// DomainView 多智能体域资源视图（可选）：非空时 ListTools/ListResources
	// 支持按 agent_id 返回对应智能体域的资源/工具清单（配置区跟随切换）。
	// 由 cmd/agent 注入按域扫描 skill/mcp 目录的只读实现。
	DomainView DomainViewer
	// WorkRoot 用户工作区根（模块二·聊天上传文档落盘）；空 = 进程工作目录
	// （容器内 /app 与沙盒 /work 为同一共享卷，见 deploy/docker-compose.yml）。
	WorkRoot string
	// ChatSandboxURL 聊天文档解析沙盒地址（pdf/docx/pptx）；空 = 不装配沙盒
	// 解析（此时仅支持 md/txt/html/xlsx）。
	ChatSandboxURL string
	// ChatSandboxUserID 解析沙盒用户 ID（缺省 1，与 rag 摄取一致）。
	ChatSandboxUserID int64
	// Vision 图片视觉解析实现（可选）：nil 时按环境变量装配（NewVisionFromEnv），
	// 未配置 VISION_MODEL 则降级为 NoopVision（图片仅渲染不解析）。
	Vision VisionProvider
	// DocLimits 文档生成（render_document）限制（P4-L）；零值 = 内置默认值。
	DocLimits config.DocConfig
	// ChatDocMaxSizeMB 聊天上传单文档大小上限 MB（缺省 20）。
	ChatDocMaxSizeMB int
	// ChatDocsPerSession 每会话文档数量上限（缺省 20）。
	ChatDocsPerSession int
	// ChatDocInjectRunes 解析工具单次返回正文上限（缺省 8000）。
	ChatDocInjectRunes int
	// OrchSubtaskTimeoutSec 编排子任务单次执行超时秒数（缺省 1800，0 = 不限制）。
	// 编排子任务常带工具多轮往返，单独放宽编排级超时避免长任务被误杀；
	// 单个角色可再以角色级超时覆盖（见 runSubTask）。
	OrchSubtaskTimeoutSec int
	// OrchSubtaskRetries 编排子任务失败自动重试次数（缺省 1，0 = 不重试）。
	// 仅对可重试错误（5xx/429/网络/超时）整任务重试；业务错误不重试。
	OrchSubtaskRetries int
}

// DomainViewer 按智能体域返回资源/工具清单（多智能体切换时配置区跟随）。
// 由 cmd/agent 注入：按 agent_id 扫描对应 skill/mcp 目录（只读，不建连接）。
// 全局能力（capability）与内置工具不在此列——它们对所有域一致，
// 由 Service 基于实例注册表补充。
type DomainViewer interface {
	// Skills 返回该域已启用的技能资源清单（不含全局能力）。
	Skills(agentID string) []ResourceInfo
	// McpTools 返回该域已启用 MCP 的工具清单（工具名 mcp_<server>_<tool>）。
	McpTools(agentID string) []ToolInfo
	// Defaults 返回该域默认会话配置（agent_defaults.json 解析结果；
	// 缺文件/无默认 = 零值，见 defaults.go）。会话创建无显式配置时继承。
	Defaults(agentID string) AgentDefaults
}

// NewService 创建 Service；必填项缺失立即报错（尽早失败原则）。
func NewService(cfg Config) (*Service, error) {
	if cfg.Repo == nil {
		return nil, fmt.Errorf("agentsvc: Repo 不能为空")
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agentsvc: Provider 不能为空")
	}
	if cfg.Registry == nil {
		return nil, fmt.Errorf("agentsvc: Registry 不能为空")
	}
	if cfg.Log == nil {
		return nil, fmt.Errorf("agentsvc: Log 不能为空")
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 8
	}
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = 20
	}
	// 聊天上传限制缺省回退（P4-L）：未配置时沿用内置默认，保证单测/未接线场景
	// 行为与旧硬编码一致。
	if cfg.ChatDocMaxSizeMB <= 0 {
		cfg.ChatDocMaxSizeMB = defaultChatDocSize >> 20
	}
	if cfg.ChatDocsPerSession <= 0 {
		cfg.ChatDocsPerSession = defaultChatDocsPerSession
	}
	if cfg.ChatDocInjectRunes <= 0 {
		cfg.ChatDocInjectRunes = defaultChatDocInjectRunes
	}
	// 编排子任务韧性缺省回退（P4-L）：未配置时沿用内置默认。
	// 硬限制宽松化：单任务 30 分钟（优先保证任务成功，配合提示词软限制控制节奏；
	// 本地大模型响应慢，300s 对复杂长任务（多轮检索/长文撰写）过紧易误杀）。
	orchTimeout := 30 * time.Minute
	if cfg.OrchSubtaskTimeoutSec > 0 {
		orchTimeout = time.Duration(cfg.OrchSubtaskTimeoutSec) * time.Second
	}
	orchRetries := cfg.OrchSubtaskRetries
	if orchRetries < 0 {
		orchRetries = 0
	}
	var chatSandbox *sandboxclient.Client
	if cfg.ChatSandboxURL != "" {
		uid := cfg.ChatSandboxUserID
		if uid <= 0 {
			uid = 1
		}
		// 沙盒侧以 /work 视角读写共享卷；解析脚本在 sandbox 容器内执行。
		chatSandbox = sandboxclient.New(cfg.ChatSandboxURL, "/work", uid, cfg.Log)
	}
	// 视觉解析：缺省按环境变量装配（未配置 VISION_MODEL 降级 Noop，向后兼容）。
	if cfg.Vision == nil {
		cfg.Vision = NewVisionFromEnv(cfg.Log)
	}
	svc := &Service{
		repo:                  cfg.Repo,
		provider:              cfg.Provider,
		registry:              cfg.Registry,
		log:                   cfg.Log,
		model:                 cfg.Model,
		systemPrompt:          cfg.SystemPrompt,
		maxRounds:             cfg.MaxRounds,
		maxMessages:           cfg.MaxMessages,
		autoApproveTools:      cfg.AutoApproveTools,
		filesBaseURL:          cfg.FilesBaseURL,
		domainView:            cfg.DomainView,
		workRoot:              cfg.WorkRoot,
		chatSandbox:           chatSandbox,
		vision:                cfg.Vision,
		pending:               make(map[int64]map[string]*pendingToolCall),
		docLimits:             cfg.DocLimits,
		maxChatDocSize:        cfg.ChatDocMaxSizeMB << 20,
		maxChatDocsPerSession: cfg.ChatDocsPerSession,
		maxChatDocInjectRunes: cfg.ChatDocInjectRunes,
		orchSubtaskTimeout:    orchTimeout,
		orchSubtaskRetries:    orchRetries,
	}
	// 视觉工具化（需求 8）：describe_image 绑定本实例（需 vision/workRoot），
	// 注册进当前注册表；热替换（ReplaceRegistry）时同样重新注册，保证任何
	// 路径下智能体都具备视觉追问能力。文档解析（需求 P2）：read_document
	// 绑定本实例（需 chatSandbox/workRoot），一并注册。
	if err := svc.ensureVisionToolRegistered(cfg.Registry); err != nil {
		return nil, err
	}
	if err := svc.ensureDocToolRegistered(cfg.Registry); err != nil {
		return nil, err
	}
	if err := svc.ensureRenderToolRegistered(cfg.Registry); err != nil {
		return nil, err
	}
	if err := svc.ensureRenderHTMLToolRegistered(cfg.Registry); err != nil {
		return nil, err
	}
	return svc, nil
}

// ---------------------------------------------------------------------------
// 工具注册表访问与热替换（管理端 skill/MCP 热加载）
// ---------------------------------------------------------------------------

// getRegistry 并发安全读取当前工具注册表。
func (s *Service) getRegistry() *tool.Registry {
	s.registryMu.RLock()
	defer s.registryMu.RUnlock()
	return s.registry
}

// ReplaceRegistry 热替换工具注册表（管理端保存 skill/MCP 配置后由监听器调用）。
// 进行中的会话已持有旧注册表引用（注册表只读共享），不受影响；
// 新会话/新请求读到新表。替换全程加锁，保证 Get/Schemas 读到一致快照。
// 视觉工具 describe_image / 文档解析工具 read_document 绑定本实例：
// 先注册进新表再替换，热加载后不丢失。
func (s *Service) ReplaceRegistry(reg *tool.Registry) {
	if err := s.ensureVisionToolRegistered(reg); err != nil {
		s.log.Error("register describe_image tool failed", zap.Error(err))
	}
	if err := s.ensureDocToolRegistered(reg); err != nil {
		s.log.Error("register read_document tool failed", zap.Error(err))
	}
	if err := s.ensureRenderToolRegistered(reg); err != nil {
		s.log.Error("register render_document tool failed", zap.Error(err))
	}
	if err := s.ensureRenderHTMLToolRegistered(reg); err != nil {
		s.log.Error("register render_html tool failed", zap.Error(err))
	}
	s.registryMu.Lock()
	s.registry = reg
	s.registryMu.Unlock()
	s.log.Info("tool registry replaced (hot reload)",
		zap.Int("tool_count", len(reg.Schemas())))
}

// ensureVisionToolRegistered 把 describe_image 视觉工具注册进给定注册表
// （工具绑定本 Service 实例：执行时经 vision/workRoot 解析图片）。
// 已存在同名工具（不应发生）时返回错误，调用方决定是否阻断。
func (s *Service) ensureVisionToolRegistered(reg *tool.Registry) error {
	if reg == nil {
		return errors.New("agentsvc: 注册表不能为空")
	}
	return reg.Register(describeImageTool{svc: s})
}

// ensureDocToolRegistered 把 read_document 文档解析工具注册进给定注册表
// （工具绑定本 Service 实例：执行时经 chatSandbox/workRoot 解析文档）。
// 已存在同名工具（不应发生）时返回错误，调用方决定是否阻断。
func (s *Service) ensureDocToolRegistered(reg *tool.Registry) error {
	if reg == nil {
		return errors.New("agentsvc: 注册表不能为空")
	}
	return reg.Register(readDocumentTool{svc: s})
}

// ensureRenderToolRegistered 把 render_document 文档生成工具注册进给定注册表
// （工具绑定本 Service 实例：执行时经 chatSandbox/workRoot 渲染文档）。
// 已存在同名工具（不应发生）时返回错误，调用方决定是否阻断。
func (s *Service) ensureRenderToolRegistered(reg *tool.Registry) error {
	if reg == nil {
		return errors.New("agentsvc: 注册表不能为空")
	}
	return reg.Register(renderDocumentTool{svc: s})
}

// ensureRenderHTMLToolRegistered 把 render_html 网页文档生成工具注册进给定
// 注册表（工具绑定本 Service 实例：HTML 落盘不依赖沙盒，PDF 需 chatSandbox）。
// 已存在同名工具（不应发生）时返回错误，调用方决定是否阻断。
func (s *Service) ensureRenderHTMLToolRegistered(reg *tool.Registry) error {
	if reg == nil {
		return errors.New("agentsvc: 注册表不能为空")
	}
	return reg.Register(renderHTMLTool{svc: s})
}

// ---------------------------------------------------------------------------
// 会话 CRUD（P2-41 业务层）
// ---------------------------------------------------------------------------

// CreateSession 创建会话。title 为空时自动命名"新对话"。
// agentID 标识会话所属智能体域：” = 管理端域；'<id>' = 对应智能体域（校验格式）。
//
// 快照固化语义：创建时把管理端默认配置（agent_defaults.json）一次性合并进
// 会话 config 并落库，此后该会话始终以这份快照为准——管理端后续修改默认
// 配置只影响新会话，旧会话不受影响。管理端域（agentID==""）或无默认配置时
// 会话 config 保持空（装配时回退服务实例默认）。
func (s *Service) CreateSession(ctx context.Context, userID int64, agentID, title string) (*Session, error) {
	if title == "" {
		title = "新对话"
	}
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}
	sess, err := s.repo.CreateSession(ctx, userID, agentID, title)
	if err != nil {
		return nil, err
	}
	if agentID != "" && s.domainView != nil {
		def := s.domainView.Defaults(agentID)
		if !def.IsEmpty() {
			cfg := cleanConfig(ApplyDefaults(sess.Config, def))
			if err := s.validateConfig(cfg); err != nil {
				// 默认配置引用已失效资源（如注册表热替换后工具消失）：不阻断
				// 创建，回退为空配置并告警，管理员可自行修复默认配置。
				s.log.Warn("domain defaults invalid, snapshot skipped",
					zap.String("agent_id", agentID),
					zap.Error(err))
			} else if err := s.repo.UpdateSessionConfig(ctx, sess.ID, cfg); err != nil {
				return nil, err
			} else {
				sess.Config = cfg
			}
		}
	}
	s.log.Info("session created",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sess.ID),
		zap.String("agent_id", sess.AgentID),
		zap.String("title", sess.Title),
	)
	return sess, nil
}

// ListSessions 分页列出用户在某智能体域的会话。
// agentID：” = 管理端域；'*' = 全部域；其它 = 精确匹配智能体域。
// 参数宽松处理：page<1 → 1；pageSize<1 → 20；>100 → 100。
func (s *Service) ListSessions(ctx context.Context, userID int64, agentID string, page, pageSize int) ([]*Session, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	sessions, total, err := s.repo.ListSessions(ctx, userID, agentID, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	return sessions, total, nil
}

// MergeGuestSessions 把游客命名空间（负 user_id）下的会话合并给真实账号。
// 登录成功后由前端携带原游客 ID 调用；游客身份自身不可作为合并目标。
func (s *Service) MergeGuestSessions(ctx context.Context, userID int64, guestID string) (int, error) {
	guestUID := auth.GuestUserID(guestID)
	if guestUID == 0 {
		return 0, apperr.New(apperr.CodeInvalidArgument, "游客 ID 格式非法")
	}
	if userID < 0 {
		return 0, apperr.New(apperr.CodePermissionDenied, "游客身份不能合并游客会话")
	}
	n, err := s.repo.MergeGuestSessions(ctx, guestUID, userID)
	if err != nil {
		return 0, err
	}
	s.log.Info("guest sessions merged",
		zap.Int64("guest_user_id", guestUID),
		zap.Int64("target_user_id", userID),
		zap.Int("migrated", n),
	)
	return n, nil
}

// AdminSessionStats 管理端会话统计（数据管理模块）。
// 由 gateway adminsvc 经鉴权后转发（内网可信）；本方法只做参数校验。
// days 窗口 1..90（调用方默认 30），超界直接拒绝。
func (s *Service) AdminSessionStats(ctx context.Context, days int) (*SessionStats, error) {
	if days < 1 || days > 90 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "统计窗口 days 须在 1..90 之间")
	}
	st, err := s.repo.SessionStats(ctx, days)
	if err != nil {
		return nil, err
	}
	s.log.Info("admin session stats",
		zap.Int("days", days),
		zap.Int64("total_sessions", st.Total),
	)
	return st, nil
}

// agentIDPattern 智能体域 ID 白名单（与 authsvc 一致）：字母数字 + 中划线，
// ≤64 字符（URL 路径参数友好）。空串（”）表示管理端域，单独放行。
var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// validateAgentID 校验会话所属智能体域：”（管理端域）直接放行，
// 其余必须匹配白名单格式。
func validateAgentID(agentID string) error {
	if agentID == "" {
		return nil
	}
	if !agentIDPattern.MatchString(agentID) {
		return apperr.New(apperr.CodeInvalidArgument, "智能体域 ID 格式非法")
	}
	return nil
}

// UpdateSessionConfig 更新会话配置（工具权限 / 思考模式，属主校验）。
// 快照语义：全量替换用户可配字段并落库；管理员级字段（max_rounds/
// max_messages/max_thinking_rounds）保留当前快照原值——普通用户配置区
// 不展示、不可改。管理端默认配置只影响新建会话，不在此处参与合并。
// 配置字段先规范化与校验，非法直接拒绝。
func (s *Service) UpdateSessionConfig(ctx context.Context, userID, sessionID int64, cfg SessionConfig) (*Session, error) {
	sess, err := s.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	// 落库前规范化（kb_ids/mcp_servers trim/去重），再校验。
	cfg = cleanConfig(cfg)
	if err := s.validateConfig(cfg); err != nil {
		return nil, err
	}
	// 管理员级字段不可由用户改动：从当前快照继承原值（服务端权威）。
	cfg.MaxRounds = sess.Config.MaxRounds
	cfg.MaxMessages = sess.Config.MaxMessages
	cfg.MaxThinkingRounds = sess.Config.MaxThinkingRounds
	if err := s.repo.UpdateSessionConfig(ctx, sessionID, cfg); err != nil {
		return nil, err
	}
	return s.repo.GetSession(ctx, sessionID)
}

// validateConfig 校验会话配置：资源（能力/技能）与工具名必须有效、思考强度合法。
// 资源优先：enabled_resources 翻译出的每个工具都必须已注册；enabled_tools 兼容旧数据。
func (s *Service) validateConfig(cfg SessionConfig) error {
	if len(cfg.EnabledResources) > 0 {
		for _, name := range resourceToTools(cfg.EnabledResources) {
			if _, err := s.getRegistry().Get(name); err != nil {
				return apperr.Wrap(apperr.CodeInvalidArgument, fmt.Sprintf("资源对应的工具 %q 未注册，无法启用", name), err)
			}
		}
	}
	for _, name := range cfg.EnabledTools {
		if _, err := s.getRegistry().Get(name); err != nil {
			return apperr.Wrap(apperr.CodeInvalidArgument, fmt.Sprintf("工具 %q 未注册，无法启用", name), err)
		}
	}
	if cfg.Thinking != nil {
		e := cfg.Thinking.ReasoningEffort
		if e != "" && e != "low" && e != "high" && e != "max" {
			return apperr.New(apperr.CodeInvalidArgument, "reasoning_effort 仅支持 low/high/max")
		}
	}
	// kb_ids：仅做数量上限校验（归属由 rag 侧强校验，越出当前智能体域一律 404，
	// 此处不做格式绑定）；trim/去重见 cleanConfig（落库前规范化）。
	if len(cfg.KBIDs) > maxSessionKBIDs {
		return apperr.New(apperr.CodeInvalidArgument, "知识库数量超出上限（≤100）")
	}
	// mcp_servers：管理员会话级限定。仅校验"server 已启用且连接成功"
	// （注册表有对应 mcp_<server>_ 工具 = 可用性检测）；空 = 全部生效。
	if len(cfg.MCPServers) > 0 {
		available := s.mcpServerNames()
		for _, srv := range cfg.MCPServers {
			if !available[srv] && !available[mcp.SanitizeName(srv)] {
				return apperr.New(apperr.CodeInvalidArgument, fmt.Sprintf("MCP 连接 %q 未启用，无法限定", srv))
			}
		}
	}
	// model：仅做基本合法性（首尾空白/长度），归属由 llm-gateway 模型注册表
	// 强校验——未知模型在请求时返回明确错误，不在此处绑定本地注册表快照。
	if m := strings.TrimSpace(cfg.Model); m != cfg.Model {
		return apperr.New(apperr.CodeInvalidArgument, "模型名不能包含首尾空白")
	}
	if len(cfg.Model) > 128 {
		return apperr.New(apperr.CodeInvalidArgument, "模型名过长（≤128 字符）")
	}
	// mode：运行模式仅允许 single / orchestrate（空 = single）。
	if m := cfg.Mode; m != "" && m != "single" && m != "orchestrate" {
		return apperr.New(apperr.CodeInvalidArgument, "mode 仅支持 single 或 orchestrate")
	}
	// orchestrate_plan：编排方案仅允许 fixed / dynamic（空 = fixed）。
	if p := cfg.OrchestratePlan; p != "" && p != "fixed" && p != "dynamic" {
		return apperr.New(apperr.CodeInvalidArgument, "orchestrate_plan 仅支持 fixed 或 dynamic")
	}
	return nil
}

// maxSessionKBIDs 会话级知识库数量上限（防超长配置）。
const maxSessionKBIDs = 100

// cleanConfig 规范化会话配置（落库前调用）：kb_ids / mcp_servers trim 空白项 + 去重，
// model 名 trim 首尾空白。返回清理后的副本；无列表需清理时零开销。
func cleanConfig(cfg SessionConfig) SessionConfig {
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.Mode = strings.TrimSpace(cfg.Mode)
	cfg.OrchestratePlan = strings.TrimSpace(cfg.OrchestratePlan)
	if len(cfg.KBIDs) == 0 && len(cfg.MCPServers) == 0 {
		return cfg
	}
	seen := make(map[string]struct{}, len(cfg.KBIDs))
	kept := make([]string, 0, len(cfg.KBIDs))
	for _, id := range cfg.KBIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		kept = append(kept, id)
	}
	cfg.KBIDs = kept

	mseen := make(map[string]struct{}, len(cfg.MCPServers))
	mkept := make([]string, 0, len(cfg.MCPServers))
	for _, id := range cfg.MCPServers {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := mseen[id]; dup {
			continue
		}
		mseen[id] = struct{}{}
		mkept = append(mkept, id)
	}
	cfg.MCPServers = mkept
	return cfg
}

// isZeroSessionConfig 判断会话配置是否为全零值（未设置任何字段）。
// 用于分支继承：源会话无配置时无需落库，避免无意义写入。
func isZeroSessionConfig(c SessionConfig) bool {
	return len(c.EnabledTools) == 0 && len(c.EnabledResources) == 0 && !c.EnabledResourcesSet &&
		c.Thinking == nil && len(c.KBIDs) == 0 && !c.KBIDsSet &&
		len(c.MCPServers) == 0 && !c.MCPServersSet &&
		c.MaxRounds == 0 && c.MaxMessages == 0 && c.MaxThinkingRounds == 0 &&
		c.Model == "" && !c.EnabledCapabilitiesSet && !c.EnabledSkillsSet &&
		c.Mode == ""
}

// ListTools 列出工具清单（名称 + 描述），供配置 UI 使用。
// agentID 为空 → 返回本实例注册表全量（向后兼容）；
// agentID 非空且注入 DomainViewer → 返回"该域装配视图"：
// 全局内置工具（实例注册表剔除 skill/mcp）+ 该域 MCP 工具。
func (s *Service) ListTools(agentID string) []ToolInfo {
	if agentID != "" && s.domainView != nil {
		return s.domainTools(agentID)
	}
	return s.instanceTools()
}

// instanceTools 当前实例注册表全量工具清单。
func (s *Service) instanceTools() []ToolInfo {
	schemas := s.getRegistry().Schemas()
	out := make([]ToolInfo, 0, len(schemas))
	for _, ts := range schemas {
		out = append(out, ToolInfo{Name: ts.Name, Description: ts.Description, External: ts.External})
	}
	return out
}

// domainTools 按域返回工具清单：内置工具 + 该域 MCP 工具。
// 内置工具对所有域一致（取自实例注册表，剔除域内资源 skill_/mcp_ 前缀）。
func (s *Service) domainTools(agentID string) []ToolInfo {
	out := make([]ToolInfo, 0, 8)
	for _, ts := range s.getRegistry().Schemas() {
		if strings.HasPrefix(ts.Name, skillToolPrefix) || strings.HasPrefix(ts.Name, mcpToolPrefix) {
			continue // 域内资源，由 DomainViewer 按域返回
		}
		out = append(out, ToolInfo{Name: ts.Name, Description: ts.Description, External: ts.External})
	}
	if s.domainView != nil {
		out = append(out, s.domainView.McpTools(agentID)...)
	}
	return out
}

// ListResources 列出普通用户可见的资源清单（能力 + 技能）。
// agentID 为空 → 本实例注册表技能（向后兼容）；
// agentID 非空且注入 DomainViewer → 全局能力 + 该域技能。
// 阶段1·权限分层：只暴露 id/名称/说明，不含任何工具名与技能代码。
func (s *Service) ListResources(agentID string) []ResourceInfo {
	out := make([]ResourceInfo, 0, len(defaultCapabilities)+8)
	for _, c := range defaultCapabilities {
		out = append(out, ResourceInfo{ID: c.id, Name: c.name, Description: c.description, Type: "capability"})
	}
	var skills []ResourceInfo
	if agentID != "" && s.domainView != nil {
		skills = s.domainView.Skills(agentID)
	} else {
		// 默认：本实例注册表里 skill_ 前缀工具即技能。
		for _, ts := range s.getRegistry().Schemas() {
			if strings.HasPrefix(ts.Name, skillToolPrefix) {
				id := strings.TrimPrefix(ts.Name, skillToolPrefix)
				// 展示名恢复为技能原始名（如中文名），而非净化+哈希后的工具名后缀。
				display := friendlySkillName(ts.Description)
				if display == "" {
					display = id
				}
				skills = append(skills, ResourceInfo{ID: id, Name: display, Description: ts.Description, Type: "skill"})
			}
		}
	}
	out = append(out, skills...)
	return out
}

// friendlySkillName 从技能工具描述中提取友好展示名（描述格式固定为
// "技能【<原始名>】：…"）。技能工具的工具名是净化+哈希后的 ASCII
// （如 skill_数据分析_abcd1234），对用户不友好；这里恢复为原始中文名。
// 解析失败返回空串（调用方回退为 id）。
func friendlySkillName(desc string) string {
	const open, closeDelim = "技能【", "】"
	i := strings.Index(desc, open)
	if i < 0 {
		return ""
	}
	rest := desc[i+len(open):]
	j := strings.Index(rest, closeDelim)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// GetSession 获取会话详情（属主校验：非本人按不存在处理，防枚举）。
func (s *Service) GetSession(ctx context.Context, userID, sessionID int64) (*Session, error) {
	return s.getOwnedSession(ctx, userID, sessionID)
}

// DeleteSession 软删会话（属主校验；已删除幂等成功）。
func (s *Service) DeleteSession(ctx context.Context, userID, sessionID int64) error {
	// 属主校验：非本人 → 按不存在处理（防枚举）。
	// 注意：这里用"仅属主校验"而非 getOwnedSession——
	// 已软删的会话仍是本人数据，重复删除应幂等成功而非报"不存在"。
	if _, err := s.getSessionForWrite(ctx, userID, sessionID); err != nil {
		return err
	}
	if err := s.repo.DeleteSession(ctx, sessionID); err != nil {
		return err
	}
	s.log.Info("session deleted", zap.Int64("user_id", userID), zap.Int64("session_id", sessionID))
	return nil
}

// ListMessages 列出会话全部消息（seq 升序，属主校验）。
// 供前端"历史回看"与"重开浏览器恢复上下文"使用（P2-G）。
func (s *Service) ListMessages(ctx context.Context, userID, sessionID int64) ([]*Message, error) {
	if _, err := s.getOwnedSession(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	msgs, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// DeleteMessage 删除"一轮完整对话"（属主校验；目标消息不存在幂等成功）。
// 轮 = 该消息所在轮（最近的 user 消息）起的整段：user 提问 + assistant
// 回答（含工具调用对）都删除，用户可整段清理"无意义、污染上下文"的对话。
// 删除后会话若无任何消息 → repository 自动软删会话（空会话不保留）。
func (s *Service) DeleteMessage(ctx context.Context, userID, sessionID, messageID int64) error {
	if _, err := s.getOwnedSession(ctx, userID, sessionID); err != nil {
		return err
	}
	if err := s.repo.DeleteRound(ctx, sessionID, messageID); err != nil {
		return err
	}
	s.log.Info("round deleted",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.Int64("message_id", messageID),
	)
	return nil
}

// RenameSession 重命名会话（属主校验）。标题 1~100 字符（按字符计数）。
func (s *Service) RenameSession(ctx context.Context, userID, sessionID int64, title string) (*Session, error) {
	title = strings.TrimSpace(title)
	if n := utf8.RuneCountInString(title); n < 1 || n > 100 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "标题长度须为 1~100 字符")
	}
	sess, err := s.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateSessionTitle(ctx, sessionID, title); err != nil {
		return nil, err
	}
	sess.Title = title
	s.log.Info("session renamed", zap.Int64("session_id", sessionID), zap.String("title", title))
	return sess, nil
}

// ---------------------------------------------------------------------------
// 对话（P2-42 非流式 / P2-43 流式）
// ---------------------------------------------------------------------------

// ChatResult 一次非流式对话的结果（消息 + 统计，供传输层回填契约字段）。
type ChatResult struct {
	Message   *schema.Message // 最终 assistant 回答
	Rounds    int             // 消息循环轮数（多步任务 > 1）
	ToolCalls int             // 实际执行的工具次数
	Usage     llm.Usage       // 累计 token 用量
}

// normalizeChatInput 规范化用户输入：空 content（纯文件上传场景，用户只传
// 文件不输入文字）替换为占位提示，让模型基于上下文中的 [文档]/[图片] 注入
// 消息回复，而不是拒绝请求或对空消息困惑（需求 6）。非空输入原样返回。
func normalizeChatInput(content string) string {
	if strings.TrimSpace(content) == "" {
		return "（用户仅上传了文件，未输入文字；请阅读上下文中的文件内容并给出回复）"
	}
	return content
}

// Chat 非流式对话：加载历史 → framework.Run → 本轮新增消息落库。
func (s *Service) Chat(ctx context.Context, userID, sessionID int64, content string) (*ChatResult, error) {
	// 纯文件场景（用户只传文件不输入文字）：空 content 规范化为占位提示，
	// 让模型基于上下文中的 [文档]/[图片] 注入消息回复，而不是拒绝请求。
	content = normalizeChatInput(content)
	sess, err := s.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}

	// 同会话串行：避免并发请求的历史交错与 seq 乱序（P2-47）。
	lock := s.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	// 阶段3：会话结束（正常/超时/断连）时清理该会话全部挂起项，防泄漏。
	defer s.clearSessionPending(sessionID)

	// 多智能体编排模式：走编排流程（非流式，无进度下发）。
	if sess.Config.Mode == "orchestrate" {
		return s.chatOrchestrate(ctx, sess, userID, content)
	}

	ag, err := s.newAgentWithHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// Run 前的记忆快照：persistRound 用它与 Run 后做差集，定位"本轮新增"。
	before := ag.History()

	// 把 user_id / role / agent_id 经 context 注入上游请求头（X-User-Id /
	// X-User-Role / X-Agent-Id）：llm-gateway 按用户做速率限制与 token 配额
	// 统计（管理员经角色豁免默认配额），并按智能体域聚合用量（P2-AI，
	// 调用方注入，provider 是共享单例）。同时注入会话级配置
	// （kb_ids 默认检索范围，见 runCtx）。agent_id 空（管理端域）时仅注入 user_id。
	runCtx0 := runCtx(withUserHeaders(ctx, userID), sess.Config)
	if sess.AgentID != "" {
		runCtx0 = llm.WithHeader(runCtx0, "X-Agent-Id", sess.AgentID)
	}
	result, err := ag.Run(runCtx0, content)
	if err != nil {
		// 本轮执行失败（超时/限流/工具失败后续答失败/空回复/轮数超限）：
		// 已产生的部分消息先落库再报告——否则用户看到"上一轮对话凭空消失"
		// 且无法复盘（P4-I）。仍返回原错误给前端提示。
		err = s.persistPartialOnError(ctx, sessionID, before, ag, err)
		// 空回复等错误：区分"是否已执行工具"给用户明确提示（见 mapEmptyReplyError）。
		return nil, mapEmptyReplyError(err, before, ag)
	}
	s.log.Info("chat completed",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.Int("rounds", result.Rounds),
		zap.Int("tool_calls", result.ToolCalls),
		zap.Int("total_tokens", result.Usage.TotalTokens),
	)

	newMsgs, err := s.persistRound(ctx, sessionID, before, ag)
	if err != nil {
		return nil, err
	}
	// 首轮对话后自动命名：会话标题仍为"新对话"时，取第一条用户消息生成。
	s.autoRenameIfDefault(ctx, sess, newMsgs)
	// 最后一条必为最终 assistant 回答（Run 以无工具调用的回答收尾）。
	last := newMsgs[len(newMsgs)-1].ToSchema()
	return &ChatResult{
		Message:   &last,
		Rounds:    result.Rounds,
		ToolCalls: result.ToolCalls,
		Usage:     result.Usage,
	}, nil
}

// StreamChat 流式对话：contentFn 接收每个文本增量（打字机效果）。
// 与 Chat 同链路（历史恢复 + 并发锁 + 落库），仅推理入口换 RunStream。
// 等价于 StreamChatEvents(ctx, userID, sessionID, content,
// &agent.StreamObserver{OnContent: contentFn})。
func (s *Service) StreamChat(ctx context.Context, userID, sessionID int64, content string, contentFn func(string)) (*agent.Result, error) {
	var obs *agent.StreamObserver
	if contentFn != nil {
		obs = &agent.StreamObserver{OnContent: contentFn}
	}
	return s.StreamChatEvents(ctx, userID, sessionID, content, obs)
}

// StreamChatEvents 流式对话：除文本增量外，还把思考/工具执行过程事件
// 实时通知给 obs（可空），供前端渲染"思考过程"气泡。实现与 StreamChat
// 完全一致，仅多出事件分发。
func (s *Service) StreamChatEvents(ctx context.Context, userID, sessionID int64, content string, obs *agent.StreamObserver) (*agent.Result, error) {
	// 纯文件场景：空 content 规范化为占位提示（见 normalizeChatInput），
	// 用户只上传文件不输入文字也能触发一轮回复（需求 6）。
	content = normalizeChatInput(content)
	sess, err := s.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}

	lock := s.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	// 阶段3：会话结束（正常/超时/断连）时清理该会话全部挂起项，防泄漏。
	defer s.clearSessionPending(sessionID)

	// 多智能体编排模式：走编排流程（子任务独立 Session + 进度实时下发）。
	if sess.Config.Mode == "orchestrate" {
		return s.streamOrchestrate(ctx, sess, userID, content, obs)
	}

	ag, err := s.newAgentWithHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// 同上：Run 前快照，用于差分本轮新增消息。
	before := ag.History()

	// 与 Chat 相同：经 context 注入 X-User-Id / X-User-Role / X-Agent-Id
	// 供上游网关限流/配额统计与按智能体域用量聚合（agent_id 空则不注入
	// X-Agent-Id）。
	// 同时注入会话级配置（kb_ids 默认检索范围，见 runCtx）。
	runCtx0 := runCtx(withUserHeaders(ctx, userID), sess.Config)
	if sess.AgentID != "" {
		runCtx0 = llm.WithHeader(runCtx0, "X-Agent-Id", sess.AgentID)
	}
	result, err := ag.RunStreamWithObserver(runCtx0, content, obs)
	if err != nil {
		// 本轮执行失败：已产生的部分消息先落库再报告（与 Chat 一致，P4-I），
		// 避免"上一轮对话凭空消失"且可从历史复盘失败前的思考/工具过程。
		err = s.persistPartialOnError(ctx, sessionID, before, ag, err)
		s.log.Warn("stream chat 失败",
			zap.Int64("user_id", userID),
			zap.Int64("session_id", sessionID),
			zap.Error(err))
		return nil, mapEmptyReplyError(err, before, ag)
	}
	s.log.Info("stream chat completed",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.Int("rounds", result.Rounds),
		zap.Int("tool_calls", result.ToolCalls),
		zap.Int("total_tokens", result.Usage.TotalTokens),
	)

	newMsgs, err := s.persistRound(ctx, sessionID, before, ag)
	if err != nil {
		return nil, err
	}
	// 首轮对话后自动命名（与 Chat 一致）。
	s.autoRenameIfDefault(ctx, sess, newMsgs)
	return result, nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// getOwnedSession 属主校验：会话存在、属于该用户且未删除才返回。
//
// 安全设计：非本人/已删除统一返回 CodeNotFound（而非 PermissionDenied），
// 避免通过报错差异枚举他人会话 ID（防枚举）。proto 契约同样约定
// "非本人返回 NOT_FOUND"。
func (s *Service) getOwnedSession(ctx context.Context, userID, sessionID int64) (*Session, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err // repository 已把 ErrNoRows 映射为 CodeNotFound
	}
	if sess.UserID != userID || !sess.Active() {
		return nil, apperr.New(apperr.CodeNotFound, "会话不存在")
	}
	return sess, nil
}

// getSessionForWrite 写操作属主校验：仅校验"存在且属于该用户"，
// 不检查 Active——供 DeleteSession 等幂等写操作使用。
func (s *Service) getSessionForWrite(ctx context.Context, userID, sessionID int64) (*Session, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess.UserID != userID {
		return nil, apperr.New(apperr.CodeNotFound, "会话不存在")
	}
	return sess, nil
}

// sessionLock 获取（或创建）指定会话的互斥锁。
func (s *Service) sessionLock(sessionID int64) *sync.Mutex {
	v, _ := s.sessionLocks.LoadOrStore(sessionID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// newAgentWithHistory 新建 framework Session，并把 DB 历史预加载进记忆窗口（P2-44）。
//
// 关键点：每次对话都从 DB 全量恢复历史 → WithInitialHistory 逐条灌回
// 短期记忆（超窗自动滚动）。这样模型始终在"完整上下文"上继续，而不是
// 每次新建 Session 失忆重启。
//
// 会话配置（需求 10 工具权限 + 思考模式）在此生效：
//   - EnabledTools 非空 → 只向模型暴露白名单工具（权限收敛）；
//   - Thinking 非空 → 透传思考开关与推理强度（如关闭思考省 token）。
func (s *Service) newAgentWithHistory(ctx context.Context, sessionID int64) (*agent.Session, error) {
	history, err := s.loadHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return s.newAgentWithConfig(ctx, sessionID, history)
}

// newAgentWithConfig 用显式历史构建 framework Session（Regenerate 等
// "上下文截止到某轮"的场景复用）。配置注入逻辑与 newAgentWithHistory 一致。
//
// 会话配置在此生效：
//   - EnabledResources 非空 → 翻译成工具白名单（资源层，用户不接触工具名）；
//   - 否则 EnabledTools 非空 → 直接按工具名白名单（兼容旧数据）；
//   - 都为空 → 全部工具启用；
//   - 每次会话注入工具审计回调（WithToolAuditor，阶段1·审计落库）。
//
// 配置来源：会话 config（创建时已固化管理端默认快照，见 CreateSession），
// 不再运行时合并默认配置——管理端改默认只影响新会话，旧会话用快照原值。
func (s *Service) newAgentWithConfig(ctx context.Context, sessionID int64, history []schema.Message) (*agent.Session, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	mcfg := sess.Config // 快照配置（含管理员级字段）
	reg := s.sessionToolRegistry(mcfg)
	// 模型：会话快照已选（mcfg.Model 非空）优先，否则回退服务实例默认模型。
	// 空模型名由 llm-gateway 模型注册表在请求时落到默认模型（resolveModel），
	// 此处不校验模型是否存在——归属校验在网关一侧强校验。
	model := s.model
	if mcfg.Model != "" {
		model = mcfg.Model
	}
	cfg := schema.AgentConfig{
		Model:             model,
		SystemPrompt:      BuildSystemPromptWithMedia(s.systemPrompt, s.filesBaseURL),
		MaxRounds:         s.effectiveRounds(mcfg.MaxRounds),
		MaxThinkingRounds: mcfg.MaxThinkingRounds,
		Memory:            schema.MemoryConfig{MaxMessages: s.effectiveMessages(mcfg.MaxMessages)},
	}
	applyThinkingConfig(&cfg, mcfg.Thinking)
	opts := []agent.Option{
		agent.WithInitialHistory(history),
		// 阶段1·审计：每次工具执行结束异步落库（写失败仅记日志，不阻塞对话）。
		agent.WithToolAuditor(s.auditObserver(sess.UserID, sessionID)),
		// 上下文压缩（context condensation）：窗口超限时用 LLM 把最旧消息压成
		// 摘要而非直接丢弃（framework CondensingMemory）。注意成本：每轮窗口
		// 溢出触发一次小型 LLM 调用；失败自动降级为普通裁剪，不阻塞对话。
		agent.WithMemoryCondenser(s.makeCondenser(model)),
	}
	if s.autoApproveTools {
		// 本地个人使用：L2/L3 工具自动放行（审计见 auditObserver）。
		opts = append(opts, agent.WithApprovalFunc(s.approveToolCall))
	}
	// 阶段3·外部工具代理：注册挂起/回填执行器。注册表无 External 工具时
	// Dispatch 永不触发（零开销）；有则外部工具调用挂起等待 SubmitToolResult 回填。
	runner := &dispatchRunner{svc: s, userID: sess.UserID, sessionID: sessionID}
	opts = append(opts, agent.WithAsyncRunner(runner))
	ag, err := agent.NewSession(cfg, s.provider, reg, opts...)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "初始化 Agent 会话失败", err)
	}
	runner.ag.Store(ag) // 回填会话指针，Dispatch 触发时可用
	return ag, nil
}

// runCtx 把会话级配置注入对话运行上下文（当前：kb_ids 默认检索范围）。
// framework 的 Run/RunStream 会把 ctx 透传给工具 Execute，kb_search
// 据此在模型未显式限定 kb_ids 时按会话配置的知识库检索。
func runCtx(ctx context.Context, cfg SessionConfig) context.Context {
	if len(cfg.KBIDs) > 0 {
		ctx = kb.WithKBIDs(ctx, cfg.KBIDs)
	}
	return ctx
}

// auditObserver 构造工具审计回调（阶段1·审计）。
// 每次工具执行成功/失败记录一条 audit_tool_calls；用独立超时上下文写库，
// 失败只记日志降级，绝不阻塞会话主循环（审计是旁路，不是主链路）。
func (s *Service) auditObserver(userID, sessionID int64) agent.ToolAuditFunc {
	return func(call schema.ToolCall, result *schema.ToolResult, err error, duration time.Duration) {
		a := &AuditToolCall{
			UserID:     userID,
			SessionID:  sessionID,
			AgentName:  defaultAgentName,
			Tool:       call.Name,
			ToolCallID: call.ID,
			Arguments:  call.Arguments,
			DurationMs: duration.Milliseconds(),
		}
		switch {
		case err != nil:
			a.Result = err.Error()
			a.IsError = true
		case result != nil:
			a.Result = result.Content
			a.IsError = result.IsError
		}
		actx, cancel := context.WithTimeout(context.Background(), auditWriteTimeout)
		defer cancel()
		if werr := s.repo.InsertAuditToolCall(actx, a); werr != nil {
			s.log.Warn("工具审计记录写入失败",
				zap.String("tool", call.Name),
				zap.String("tool_call_id", call.ID),
				zap.Error(werr))
		}
	}
}

// approveToolCall 工具审批策略（本地个人使用）。
//
// 背景：framework 的权限模型规定 L2/L3 工具（file_ops 写 / code_executor）
// 必须经过用户确认（WithApprovalFunc 回调），未确认一律拒绝。本项目的
// 实际形态是个人本地部署（web/desktop 直连自家 agent），弹审批 UI 反而
// 打断使用，因此默认自动放行；每次调用记审计日志，便于追溯。
//
// 多用户/生产部署时：将此策略替换为"前端审批 UI 回调"（framework 钩子
// 已就位，无需改框架代码），或通过 AGENT_AUTO_APPROVE_TOOLS=false 关闭。
func (s *Service) approveToolCall(call schema.ToolCall) bool {
	s.log.Info("工具调用自动放行（本地策略）",
		zap.String("tool", call.Name),
		zap.String("tool_call_id", call.ID),
		zap.ByteString("arguments", call.Arguments))
	return true
}

// buildResourceTools 按能力/技能独立 presence 标记构建工具白名单（新语义）。
// 能力/技能各自独立决定白名单：
//   - 类别标记 true → 白名单 = enabled_resources 中该类别的项（空 = 全不选）；
//   - 类别标记 false → 该类别未设置，注入实例全量工具（全部启用）。
//
// restrictEmpty 仅在"最终白名单为空且由显式全不选导致"时置位——空注册表
// 表达"不启用任何资源"（基础对话），不能回退到全量。两个类别都未设置时
// 由调用方走旧联合语义分支，本函数不会被调用。
func (s *Service) buildResourceTools(cfg SessionConfig, restrictEmpty *bool) []string {
	caps, skills := splitResourceTools(cfg.EnabledResources)
	var tools []string
	if cfg.EnabledCapabilitiesSet {
		tools = append(tools, resourceToTools(caps)...)
		if len(caps) == 0 {
			*restrictEmpty = true
		}
	} else {
		tools = append(tools, allCapabilityTools()...)
	}
	if cfg.EnabledSkillsSet {
		tools = append(tools, resourceToTools(skills)...)
		if len(skills) == 0 {
			*restrictEmpty = true
		}
	} else {
		tools = append(tools, s.allDomainSkillTools()...)
	}
	// 知识库检索工具（P4-K 修复）：开关是"会话勾选了知识库"（KBIDs 非空），
	// 不受能力/技能白名单裁剪——kb_search 不属于任何 capability 资源，能力
	// 白名单生效时（enabled_capabilities_set=true）若不在此并入，用户即使勾选
	// 了知识库也看不到 kb_search（实测：会话 kb_ids 非空但模型只见 web_search/
	// calculator）。RAG 地址未配置时 kb_search 未注册，registryForConfig 会
	// 记日志忽略，天然安全。
	if len(cfg.KBIDs) > 0 {
		tools = append(tools, kbSearchToolName)
	}
	return tools
}

// allDomainSkillTools 当前注册表中全部 skill_ 工具（技能类别"未设置"时的
// 全量白名单——跟随实例全部已注册技能）。
func (s *Service) allDomainSkillTools() []string {
	var out []string
	for _, ts := range s.getRegistry().Schemas() {
		if strings.HasPrefix(ts.Name, skillToolPrefix) {
			out = append(out, ts.Name)
		}
	}
	return out
}

// registryForConfig 按配置过滤工具集（工具权限）。
// enabledTools 为空且非 restrictEmpty = 全部启用（返回共享注册表，零拷贝）；
// restrictEmpty = 显式清空语义（enabled_resources_set=true）：即使白名单为空
// 也新建空注册表——"不启用任何资源"是用户明确的意图，不能回退到全量。
// 白名单里未注册的工具名记日志忽略。
func (s *Service) registryForConfig(enabled []string, restrictEmpty bool) *tool.Registry {
	if len(enabled) == 0 && !restrictEmpty {
		return s.getRegistry()
	}
	reg := tool.NewRegistry()
	for _, name := range enabled {
		t, err := s.getRegistry().Get(name)
		if err != nil {
			s.log.Warn("会话配置了未注册工具，已忽略", zap.String("tool", name))
			continue
		}
		if err := reg.Register(t); err != nil {
			s.log.Warn("工具重复注册，已忽略", zap.String("tool", name), zap.Error(err))
		}
	}
	return reg
}

// sessionToolRegistry 按会话配置装配工具注册表（newAgentWithConfig 与编排子
// 任务 runSubTask 共用同一装配规则，保证"主会话能用什么，子任务也能用什么"）。
//
// 装配链路：资源白名单（能力/技能新语义，见 buildResourceTools）→ 注册表过滤
// → 会话级最终过滤（kb_search 反转语义 + MCP 会话级配置，见 filterSessionTools）。
func (s *Service) sessionToolRegistry(cfg SessionConfig) *tool.Registry {
	// 能力/技能各自独立决定白名单：
	//   - 类别标记 true → 白名单 = enabled_resources 中该类别的项（空 = 全不选）；
	//   - 类别标记 false → 该类别未设置，跟随实例全量（全部启用）。
	// 两个标记都未设置 → 退化为旧的联合白名单语义（enabled_resources_set）。
	legacy := !cfg.EnabledCapabilitiesSet && !cfg.EnabledSkillsSet
	restrictEmpty := false
	tools := cfg.EnabledTools
	if legacy {
		if cfg.EnabledResourcesSet {
			tools = resourceToTools(cfg.EnabledResources)
			restrictEmpty = true
		} else if len(cfg.EnabledResources) > 0 {
			tools = resourceToTools(cfg.EnabledResources)
		}
	} else {
		tools = s.buildResourceTools(cfg, &restrictEmpty)
	}
	reg := s.registryForConfig(tools, restrictEmpty)
	return s.filterSessionTools(reg, cfg)
}

// kbSearchToolName 知识库检索工具名（装配/过滤共用，避免魔法字符串）。
const kbSearchToolName = "kb_search"

// mcpToolPrefix MCP 工具名前缀（与 mcp.Provider 装配命名 mcp_<server>_<tool> 一致）。
const mcpToolPrefix = "mcp_"

// mcpServerOf 解析 mcp_<server>_<tool> 工具名 → server token；非 MCP 工具返回 false。
// server token 即 mcp.SanitizeName(配置名) 的净化结果（ASCII 小写）。
func mcpServerOf(name string) (string, bool) {
	if !strings.HasPrefix(name, mcpToolPrefix) {
		return "", false
	}
	rest := name[len(mcpToolPrefix):]
	i := strings.IndexByte(rest, '_')
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// mcpServerNames 返回当前注册表中已装配的 MCP server 集合。
// 只有"管理端启用 + 连接成功"的 server 才会注册工具（可用性检测的依据）。
func (s *Service) mcpServerNames() map[string]bool {
	set := make(map[string]bool)
	for _, ts := range s.getRegistry().Schemas() {
		if srv, ok := mcpServerOf(ts.Name); ok {
			set[srv] = true
		}
	}
	return set
}

// filterSessionTools 按会话配置对工具集做最终过滤（kb 语义反转 + MCP 会话级限定）。
//
//   - KBIDs 为空 → 移除 kb_search：本会话不使用知识库检索（工具不装配，
//     模型无从调用）；非空 → 保留，检索范围由 runCtx 注入限定。
//   - MCPServers 非空 → 仅保留选中 server 的 mcp_ 工具（管理员会话级配置）；
//     空 → 全部已启用 server 的 MCP 工具生效（普通用户默认行为）。
//
// 无过滤需求时返回原注册表（零拷贝，共享只读快照）。
func (s *Service) filterSessionTools(reg *tool.Registry, cfg SessionConfig) *tool.Registry {
	kbOn := len(cfg.KBIDs) > 0
	// MCP 全不选：MCPServersSet=true + 空列表 = 会话显式不装配任何 MCP 工具
	//（presence 标记区分"全不选"与"未设置→全部启用"两种空语义）。
	mcpNone := cfg.MCPServersSet && len(cfg.MCPServers) == 0
	if kbOn && !mcpNone && len(cfg.MCPServers) == 0 {
		return reg
	}
	mcpSet := make(map[string]bool, len(cfg.MCPServers))
	for _, name := range cfg.MCPServers {
		mcpSet[mcp.SanitizeName(name)] = true
	}
	out := tool.NewRegistry()
	for _, ts := range reg.Schemas() {
		name := ts.Name
		if !kbOn && name == kbSearchToolName {
			continue
		}
		if srv, ok := mcpServerOf(name); ok {
			if mcpNone {
				continue // 显式全不选：丢弃全部 mcp_ 工具
			}
			if len(mcpSet) > 0 && !mcpSet[srv] {
				continue // 白名单过滤：非选中 server 的 mcp_ 工具丢弃
			}
		}
		t, err := reg.Get(name)
		if err != nil {
			s.log.Warn("filterSessionTools: 注册表工具缺失", zap.String("tool", name), zap.Error(err))
			continue
		}
		if err := out.Register(t); err != nil {
			s.log.Warn("filterSessionTools: 工具重复注册，已忽略", zap.String("tool", name), zap.Error(err))
		}
	}
	return out
}

// effectiveRounds 返回单次对话的最大推理轮数：会话快照配置 > 服务实例默认。
func (s *Service) effectiveRounds(cfgMax int) int {
	if cfgMax > 0 {
		return cfgMax
	}
	return s.maxRounds
}

// effectiveMessages 返回短期记忆窗口大小：会话快照配置 > 服务实例默认。
func (s *Service) effectiveMessages(cfgMax int) int {
	if cfgMax > 0 {
		return cfgMax
	}
	return s.maxMessages
}

// applyThinkingConfig 把会话思考配置写入 AgentConfig。
// t 为 nil = 未配置（cfg.Thinking 保持 nil，厂商默认思考开启）；
// t 非 nil = 显式写入（enabled=false 时框架会下发 disabled，真正关闭）。
// P3-A8：实例默认推理强度改为 low —— 思考开启但未显式指定强度时统一回填
// "low"，不再留给厂商默认 high；与前端选项集（只保留 low/high/max 三值）对齐，
// 消除空串在保存/加载间的歧义。
func applyThinkingConfig(cfg *schema.AgentConfig, t *ThinkingConfig) {
	if t == nil {
		return
	}
	effort := t.ReasoningEffort
	if effort == "" {
		effort = "low"
	}
	cfg.Thinking = &schema.ThinkingConfig{Enabled: t.Enabled, ReasoningEffort: effort}
}

// loadHistory 读取会话全部消息并转为 framework 格式。
//
// 健康过滤：跳过"空回复"assistant 消息（content 为空且无工具调用）。
// 这类脏数据一旦入历史，会让上游模型持续返回空回答（会话"卡死"——
// 用户实测：原会话无法对话、其分支正常，正因分支没有这条脏消息）。
// 展示层（ListMessages）保留原样，仅模型上下文过滤，恢复对话即治愈。
func (s *Service) loadHistory(ctx context.Context, sessionID int64) ([]schema.Message, error) {
	msgs, err := s.repo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]schema.Message, 0, len(msgs))
	for _, m := range msgs {
		sm := m.ToSchema()
		// 编排过程摘要（system 角色，见 buildOrchestrationSummary）：仅用于
		// 前端渲染历史编排过程，不参与模型上下文，避免过程状态污染后续对话。
		if sm.Role == schema.RoleSystem {
			continue
		}
		if sm.Role == schema.RoleAssistant && sm.Content == "" && len(sm.ToolCalls) == 0 {
			continue
		}
		out = append(out, sm)
	}
	return out, nil
}

// persistRound 把本轮新增消息落库。
//
// 实现：before 为 Run 前的记忆快照（含历史窗口），ag.History() 为
// Run 后的状态（旧历史 + 本轮新增）。按"指纹计数差分"找出新增部分
// （用户消息/中间 tool 对/最终回答）一次性追加，保证 assistant 带
// tool_calls 的消息与 role=tool 结果消息成对持久化，下次恢复时模型
// 上下文完整（协议硬性要求）。
//
// 轮次分配：本轮的 round_no = 会话当前最大轮次 + 1（首轮为 1），
// 版本恒为 0（初始回答；重新生成产生的多版本见 Regenerate）。
//
// 为什么用"计数"而非"集合去重"？消息内容可能重复（用户连发相同问题、
// 模型重复相似回答）。若按集合判重，重复出现的消息会被误判为旧消息
// 而漏存。计数法：before 中每条指纹占掉一次配额，after 中多出来的
// 次数即本轮新增，且保持 after 的时序。
//
// 同时兼容记忆窗口滚动：ShortTermMemory 超窗时丢弃最旧消息，被丢弃的
// 旧消息只影响配额（不计数），不影响新增定位。
//
// 上下文压缩（P5）：diff 出的 system 摘要消息仅在记忆窗口内生效（供模型
// 感知早期对话），不落库——压缩记录以 buildCondensationMessage 生成的
// __condense_v1__ system 提示消息呈现，与普通消息同一 roundNo 落库，
// 前端据此渲染"已压缩上下文"提示条（哪个节点压缩过，历史回看仍可见）。
func (s *Service) persistRound(ctx context.Context, sessionID int64, before []schema.Message, ag *agent.Session) ([]*Message, error) {
	newSchema := diffNewMessages(before, ag.History())
	newMsgs := make([]*Message, 0, len(newSchema)+2)
	// 新轮次号 = 最大轮次 + 1（首轮 1）。
	maxRound, err := s.repo.MaxRoundNo(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	roundNo := maxRound + 1
	for _, m := range newSchema {
		if m.Role == schema.RoleSystem {
			// 上下文压缩摘要（system）：只服务模型上下文，不落库（见函数注释）。
			continue
		}
		newMsgs = append(newMsgs, &Message{
			Role:       string(m.Role),
			Content:    m.Content,
			Reasoning:  m.Reasoning,
			ToolCallID: m.ToolCallID,
			ToolCalls:  m.ToolCalls,
			RoundNo:    roundNo,
			Version:    0,
		})
	}
	// 上下文压缩记录：本轮发生过压缩则追加提示消息（消费式读取，不重复）。
	for _, info := range ag.DrainCondenseNotices() {
		newMsgs = append(newMsgs, buildCondensationMessage(roundNo, info))
	}
	if len(newMsgs) == 0 {
		return newMsgs, nil
	}
	if err := s.repo.AppendMessages(ctx, sessionID, newMsgs); err != nil {
		return nil, err
	}
	s.log.Debug("messages persisted",
		zap.Int64("session_id", sessionID),
		zap.Int("count", len(newMsgs)),
		zap.Int64("round", roundNo),
	)
	return newMsgs, nil
}

// persistPartialOnError 本轮执行失败（非"正常完成"）时，把已产生的
// 部分消息落库，避免"用户看到上一轮对话凭空消失"。
//
// 覆盖场景（P4-I 扩展，用户实测反馈）：模型空回复 / 上游 504 超时 /
// 工具调用失败后模型续答失败等。此前只在"轮数超限"时落库，其余错误
// 一律丢弃导致刷新后整轮消失——用户在界面已看到思考/工具过程，却无
// 法在历史里复盘，且下一轮无法"接着做"。
//
// 安全性：diffNewMessages 只落本轮新增（含 user 消息）；孤立 tool 由
// framework sanitizeMessages 兜底过滤，空 assistant 由 loadHistory
// 健康过滤剔除，不会污染模型上下文（见 framework/agent/run.go）。
//
// 注意：失败轮次同样计入 round_no（新增一轮），与正常轮一致；版本 0。
func (s *Service) persistPartialOnError(ctx context.Context, sessionID int64, before []schema.Message, ag *agent.Session, err error) error {
	// 用户打断/断开连接时 ctx 已被取消：落库必须脱离取消信号（context.WithoutCancel，
	// Go 1.21+），否则"已生成的部分内容"随连接一起丢失（P4-L 修复：业界标准
	// 是生成中被打断，已产出内容仍须落库可恢复，刷新后能复盘、下一轮能接着做）。
	writeCtx := context.WithoutCancel(ctx)
	newMsgs, perr := s.persistRound(writeCtx, sessionID, before, ag)
	if perr != nil {
		s.log.Warn("persist partial round after error failed",
			zap.Int64("session_id", sessionID),
			zap.Error(perr))
		return err
	}
	s.log.Info("partial round persisted after error",
		zap.Int64("session_id", sessionID),
		zap.Int("saved_msgs", len(newMsgs)),
		zap.Error(err))
	return err
}

// diffNewMessages 指纹计数差分：返回 after 中相对 before 新增的消息（保序）。
func diffNewMessages(before, after []schema.Message) []schema.Message {
	quota := make(map[string]int, len(before))
	for _, m := range before {
		quota[messageFingerprint(m)]++
	}
	newMsgs := make([]schema.Message, 0)
	for _, m := range after {
		f := messageFingerprint(m)
		if quota[f] > 0 {
			quota[f]-- // 属于旧消息（占用一次配额）
			continue
		}
		newMsgs = append(newMsgs, m)
	}
	return newMsgs
}

// autoRenameIfDefault 首轮对话后自动命名：会话标题仍为"新对话"时，
// 用第一条用户消息内容生成有意义的标题（与后端命名风格一致）。
func (s *Service) autoRenameIfDefault(ctx context.Context, sess *Session, newMsgs []*Message) {
	if sess.Title != "新对话" {
		return
	}
	for _, m := range newMsgs {
		if m.Role == string(schema.RoleUser) && m.Content != "" {
			_ = s.repo.UpdateSessionTitle(ctx, sess.ID, autoTitle(m.Content))
			return
		}
	}
}

// autoTitle 用首条用户消息生成会话标题：压平换行、截取前 24 字符。
func autoTitle(content string) string {
	t := strings.TrimSpace(content)
	t = strings.ReplaceAll(t, "\n", " ")
	runes := []rune(t)
	if len(runes) > 24 {
		return string(runes[:24])
	}
	return t
}

// autoBranchTitle 分支会话标题：原标题 +（分支）。
func autoBranchTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" || title == "新对话" {
		return "新对话（分支）"
	}
	return title + "（分支）"
}

// ---------------------------------------------------------------------------
// 重新生成 / 版本切换 / 分支（P2-K）
// ---------------------------------------------------------------------------

// Regenerate 重新生成指定轮的回答（属主校验）。
//
// 语义（与主流智能体一致）：
//  1. 旧版本回答保留（隐藏，可经 SetActiveVersion 切回）——"前面这轮生成
//     不删除，允许用户切换选择本轮内容"；
//  2. 该轮之后的旧分支被截断（隐藏）——后续对话基于新回答继续；
//  3. 新回答以新版本号落库（round_no 不变，version = max+1）。
//
// 生成失败时回滚截断（恢复旧活跃版本与后续轮次），不破坏已有对话。
func (s *Service) Regenerate(ctx context.Context, userID, sessionID, messageID int64) (*ChatResult, int, error) {
	sess, err := s.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, 0, err
	}
	lock := s.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	target, err := s.repo.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return nil, 0, err
	}
	roundNo := target.RoundNo
	// 当前活跃版本号：重新生成失败时用它回滚。
	activeVer, err := s.repo.ActiveRoundVersion(ctx, sessionID, roundNo)
	if err != nil {
		return nil, 0, err
	}
	// 截断旧分支：隐藏该轮回答（保留 user）+ 后续轮次全部消息。
	if err := s.repo.HideRoundAndAfter(ctx, sessionID, roundNo); err != nil {
		return nil, 0, err
	}
	// 新版本号 = 该轮当前最大版本 + 1。
	newVer, err := s.repo.MaxRoundVersion(ctx, sessionID, roundNo)
	if err != nil {
		return nil, 0, err
	}
	newVer++

	// 恢复上下文：到目标轮为止的可见历史（含该轮 user 消息）。
	history, err := s.repo.ListMessagesUptoRound(ctx, sessionID, roundNo)
	if err != nil {
		return nil, 0, err
	}
	schemaMsgs := make([]schema.Message, 0, len(history))
	var userContent string
	for _, m := range history {
		if m.Role == string(schema.RoleUser) {
			userContent = m.Content // 目标轮（可见历史最后一轮）的 user 消息
		}
		schemaMsgs = append(schemaMsgs, m.ToSchema())
	}
	if userContent == "" {
		return nil, 0, apperr.New(apperr.CodeInvalidArgument, "该轮缺少用户消息，无法重新生成")
	}

	ag, err := s.newAgentWithConfig(ctx, sessionID, schemaMsgs)
	if err != nil {
		return nil, 0, err
	}
	before := ag.History()
	runCtx0 := runCtx(withUserHeaders(ctx, userID), sess.Config)
	if sess.AgentID != "" {
		runCtx0 = llm.WithHeader(runCtx0, "X-Agent-Id", sess.AgentID)
	}
	result, err := ag.Run(runCtx0, userContent)
	if err != nil {
		// 生成失败：回滚截断（恢复旧活跃版本 + 后续轮次），保持原对话不变。
		// 注意用 WithoutCancel：用户打断/断连时 ctx 已取消，直接传入会让
		// 回滚 SQL 立即失败——旧回答将永久保持隐藏（P4-M 修复：重新生成
		// 被打断后回答从记录里消失）。
		_ = s.repo.RestoreRoundAndAfter(context.WithoutCancel(ctx), sessionID, roundNo, activeVer)
		return nil, 0, mapRunError(err)
	}

	// 落库新版本：差分出的 user 消息跳过（提问已在 DB），回答/工具对落库。
	newSchema := diffNewMessages(before, ag.History())
	toAppend := make([]*Message, 0, len(newSchema))
	for _, m := range newSchema {
		if m.Role == schema.RoleUser {
			continue // user 消息已存在，不重复落库
		}
		toAppend = append(toAppend, &Message{
			Role:       string(m.Role),
			Content:    m.Content,
			Reasoning:  m.Reasoning,
			ToolCallID: m.ToolCallID,
			ToolCalls:  m.ToolCalls,
			RoundNo:    roundNo,
			Version:    newVer,
		})
	}
	if len(toAppend) > 0 {
		if err := s.repo.AppendMessages(ctx, sessionID, toAppend); err != nil {
			return nil, 0, err
		}
	}

	s.log.Info("message regenerated",
		zap.Int64("session_id", sessionID),
		zap.Int64("round", roundNo),
		zap.Int("version", newVer),
		zap.Int("rounds", result.Rounds),
		zap.Int("tool_calls", result.ToolCalls),
		zap.Int("total_tokens", result.Usage.TotalTokens),
	)
	var last schema.Message
	if len(toAppend) > 0 {
		last = toAppend[len(toAppend)-1].ToSchema()
	}
	return &ChatResult{
		Message:   &last,
		Rounds:    result.Rounds,
		ToolCalls: result.ToolCalls,
		Usage:     result.Usage,
	}, newVer, nil
}

// StreamRegenerate 流式重新生成指定轮的回答（属主校验）。
//
// 与 Regenerate 完全同语义（旧版本保留可切换、后续分支截断、新版本号
// 落库），但正文增量/思考/工具过程/编排进度经 obs 逐事件下发——修复
// "点击重新生成后流式渲染效果消失"（前端此前走非流式 REST 接口）。
//
// 生成失败时回滚截断（恢复旧活跃版本与后续轮次），不破坏已有对话——
// 与 Regenerate 一致，不回滚不落库部分内容。
func (s *Service) StreamRegenerate(ctx context.Context, userID, sessionID, messageID int64, obs *agent.StreamObserver) (*agent.Result, int, error) {
	sess, err := s.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, 0, err
	}
	lock := s.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	target, err := s.repo.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return nil, 0, err
	}
	roundNo := target.RoundNo
	// 当前活跃版本号：重新生成失败时用它回滚。
	activeVer, err := s.repo.ActiveRoundVersion(ctx, sessionID, roundNo)
	if err != nil {
		return nil, 0, err
	}
	// 截断旧分支：隐藏该轮回答（保留 user）+ 后续轮次全部消息。
	if err := s.repo.HideRoundAndAfter(ctx, sessionID, roundNo); err != nil {
		return nil, 0, err
	}
	// 新版本号 = 该轮当前最大版本 + 1。
	newVer, err := s.repo.MaxRoundVersion(ctx, sessionID, roundNo)
	if err != nil {
		return nil, 0, err
	}
	newVer++

	// 恢复上下文：到目标轮为止的可见历史（含该轮 user 消息）。
	// system 角色（编排过程摘要 __orch_v1__）仅供前端历史渲染，
	// 不进入模型上下文——与 loadHistory 的过滤规则保持一致。
	history, err := s.repo.ListMessagesUptoRound(ctx, sessionID, roundNo)
	if err != nil {
		return nil, 0, err
	}
	schemaMsgs := make([]schema.Message, 0, len(history))
	var userContent string
	for _, m := range history {
		if m.Role == string(schema.RoleSystem) {
			continue
		}
		if m.Role == string(schema.RoleUser) {
			userContent = m.Content // 目标轮（可见历史最后一轮）的 user 消息
		}
		schemaMsgs = append(schemaMsgs, m.ToSchema())
	}
	if userContent == "" {
		return nil, 0, apperr.New(apperr.CodeInvalidArgument, "该轮缺少用户消息，无法重新生成")
	}

	// 上游请求头注入 + 会话级配置（与 Regenerate / StreamChatEvents 一致）。
	runCtx0 := runCtx(withUserHeaders(ctx, userID), sess.Config)
	if sess.AgentID != "" {
		runCtx0 = llm.WithHeader(runCtx0, "X-Agent-Id", sess.AgentID)
	}

	// 多智能体编排模式：重跑 DAG，进度 + 最终回答经 obs 下发，按版本语义落库。
	if sess.Config.Mode == "orchestrate" {
		result, ver, err := s.streamRegenerateOrchestrate(ctx, sess, userID, runCtx0, userContent, roundNo, newVer, obs)
		if err != nil {
			// 生成失败：回滚截断，保持原对话不变。
			// WithoutCancel：打断/断连时 ctx 已取消，直接传入会让回滚 SQL 失败，
			// 旧回答永久隐藏（P4-M）。
			_ = s.repo.RestoreRoundAndAfter(context.WithoutCancel(ctx), sessionID, roundNo, activeVer)
			return nil, 0, mapRunError(err)
		}
		return result, ver, nil
	}

	ag, err := s.newAgentWithConfig(ctx, sessionID, schemaMsgs)
	if err != nil {
		return nil, 0, err
	}
	before := ag.History()
	result, err := ag.RunStreamWithObserver(runCtx0, userContent, obs)
	if err != nil {
		// 生成失败：回滚截断（恢复旧活跃版本 + 后续轮次），保持原对话不变。
		// 与 Regenerate 一致，失败过程不落库（未产生可复用的完成轮次）。
		// WithoutCancel：打断/断连时 ctx 已取消，直接传入会让回滚 SQL 失败，
		// 旧回答永久隐藏（P4-M）。
		_ = s.repo.RestoreRoundAndAfter(context.WithoutCancel(ctx), sessionID, roundNo, activeVer)
		return nil, 0, mapRunError(err)
	}

	// 落库新版本：差分出的 user 消息跳过（提问已在 DB），回答/工具对落库。
	newSchema := diffNewMessages(before, ag.History())
	toAppend := make([]*Message, 0, len(newSchema))
	for _, m := range newSchema {
		if m.Role == schema.RoleUser {
			continue // user 消息已存在，不重复落库
		}
		toAppend = append(toAppend, &Message{
			Role:       string(m.Role),
			Content:    m.Content,
			Reasoning:  m.Reasoning,
			ToolCallID: m.ToolCallID,
			ToolCalls:  m.ToolCalls,
			RoundNo:    roundNo,
			Version:    newVer,
		})
	}
	if len(toAppend) > 0 {
		if err := s.repo.AppendMessages(ctx, sessionID, toAppend); err != nil {
			return nil, 0, err
		}
	}

	s.log.Info("message stream regenerated",
		zap.Int64("session_id", sessionID),
		zap.Int64("round", roundNo),
		zap.Int("version", newVer),
		zap.Int("rounds", result.Rounds),
		zap.Int("tool_calls", result.ToolCalls),
		zap.Int("total_tokens", result.Usage.TotalTokens),
	)
	return result, newVer, nil
}

// SetActiveVersion 切换指定轮的活跃版本（属主校验）。
// 隐藏该轮其它版本的回答，并截断（隐藏）该轮之后的全部旧分支消息——
// 用户在多个重生成版本间来回切换时，后续轮次始终以"当前所选版本"为
// 上下文基准（切换后继续对话即基于新版本延伸）。
func (s *Service) SetActiveVersion(ctx context.Context, userID, sessionID, messageID int64, version int) error {
	if version < 0 {
		return apperr.New(apperr.CodeInvalidArgument, "非法的版本号")
	}
	if _, err := s.getOwnedSession(ctx, userID, sessionID); err != nil {
		return err
	}
	lock := s.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	target, err := s.repo.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	if err := s.repo.SetActiveVersion(ctx, sessionID, target.RoundNo, version); err != nil {
		return err
	}
	s.log.Info("active version switched",
		zap.Int64("session_id", sessionID),
		zap.Int64("round", target.RoundNo),
		zap.Int("version", version),
	)
	return nil
}

// CreateBranch 基于当前上下文创建分支会话（属主校验）。
// 复制"到分支点所在轮（含）为止"的可见历史到新会话（轮次重排、版本归零），
// 标题 = 原标题 +（分支）；用户在新会话继续对话即走新分支。
func (s *Service) CreateBranch(ctx context.Context, userID, sessionID, messageID int64) (*Session, error) {
	sess, err := s.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	lock := s.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	target, err := s.repo.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	history, err := s.repo.ListMessagesUptoRound(ctx, sessionID, target.RoundNo)
	if err != nil {
		return nil, err
	}

	branch, err := s.repo.CreateSession(ctx, userID, sess.AgentID, autoBranchTitle(sess.Title))
	if err != nil {
		return nil, err
	}
	// 配置继承：分支沿用源会话配置快照（含管理员级字段），保证分支对话行为
	// 与源会话一致——分支不是"重新开对话"，不应回退实例默认或重拉管理端默认。
	// 校验失败（如注册表热替换后工具已消失）不阻断分支创建，回退空配置并告警。
	if cfg := cleanConfig(sess.Config); !isZeroSessionConfig(cfg) {
		if err := s.validateConfig(cfg); err != nil {
			s.log.Warn("branch config invalid, fallback to empty",
				zap.Int64("source_session", sessionID),
				zap.Int64("branch_session", branch.ID),
				zap.Error(err))
		} else if err := s.repo.UpdateSessionConfig(ctx, branch.ID, cfg); err != nil {
			return nil, err
		} else {
			branch.Config = cfg
		}
	}
	// 复制可见历史（重排轮次：首条归第 1 轮，之后每个 user 消息开新轮）。
	msgs := make([]*Message, 0, len(history))
	round := int64(1)
	for i, m := range history {
		if i > 0 && m.Role == string(schema.RoleUser) {
			round++
		}
		msgs = append(msgs, &Message{
			Role:       m.Role,
			Content:    m.Content,
			Reasoning:  m.Reasoning,
			ToolCallID: m.ToolCallID,
			ToolCalls:  m.ToolCalls,
			RoundNo:    round,
			Version:    0, // 分支从单版本开始
		})
	}
	if err := s.repo.AppendMessages(ctx, branch.ID, msgs); err != nil {
		return nil, err
	}
	s.log.Info("branch created",
		zap.Int64("user_id", userID),
		zap.Int64("source_session", sessionID),
		zap.Int64("branch_session", branch.ID),
		zap.Int("copied_messages", len(msgs)),
	)
	return branch, nil
}

// messageFingerprint 消息指纹：JSON 稳定序列化后作为唯一键。
func messageFingerprint(m schema.Message) string {
	b, _ := json.Marshal(m) // Message 字段均可序列化（ToolCalls.Arguments 为 RawMessage）
	return string(b)
}

// mapRunError 把 framework 运行错误映射到统一错误体系。
//
// framework 侧错误是普通 error（LLM 调用失败/达到最大轮数等）。为了让用户
// 看到具体原因而非误导性的通用提示，这里按底层原因分类映射：
//
//   - 上游 llm-gateway 返回的 HTTP 4xx：模型拒绝请求 / 无权限 / 限流，分别
//     映射为 INVALID_ARGUMENT / PERMISSION_DENIED / RESOURCE_EXHAUSTED，
//     并把上游错误详情透传给用户——避免一律包成 503"上游不可用"掩盖真相
//     （实测曾出现 DeepSeek 400 被报成 [UNAVAILABLE] 的误判）；
//   - 5xx / 网络错误：服务暂时不可用（CodeUnavailable，语义可重试）；
//   - 超时：CodeDeadlineExceeded。
func mapRunError(err error) error {
	if err == nil {
		return nil
	}
	// 已封装为统一错误的原样返回（例如参数校验类）。
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		return err
	}
	// 上游 HTTP 错误：按状态码保留 4xx 语义（模型拒绝 ≠ 服务不可用）。
	var he *llm.HTTPStatusError
	if errors.As(err, &he) {
		switch {
		case he.Status == http.StatusTooManyRequests:
			return apperr.Wrap(apperr.CodeResourceExhausted, "模型服务请求过于频繁，请稍后再试", err)
		case he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden:
			return apperr.Wrap(apperr.CodePermissionDenied, "模型服务拒绝访问，请检查模型服务密钥配置", err)
		case he.Status >= 400 && he.Status < 500:
			msg := fmt.Sprintf("模型服务拒绝请求（HTTP %d）", he.Status)
			if detail := upstreamErrDetail(he.Body); detail != "" {
				msg += "：" + detail
			}
			return apperr.Wrap(apperr.CodeInvalidArgument, msg, err)
		default:
			return apperr.Wrap(apperr.CodeUnavailable, "Agent 运行失败（上游服务可能不可用）", err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.Wrap(apperr.CodeDeadlineExceeded, "模型服务响应超时，请稍后重试", err)
	}
	return apperr.Wrap(apperr.CodeUnavailable, "Agent 运行失败（上游服务可能不可用）", err)
}

// upstreamErrDetail 从上游错误响应体提取可读的失败原因。
// 优先解析 OpenAI error JSON 或网关 {message} 包装；非 JSON 则截断原文。
// 空体/空白返回空串（此时由调用方用"HTTP 状态"兜底）。
func upstreamErrDetail(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Message != "" {
			return e.Message
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…(截断)"
	}
	return s
}

// mapEmptyReplyError 空回复错误的上下文化处理。
//
// framework 对"模型最终回答为空"统一返回 agent.ErrEmptyReply。两种场景
// 对用户的含义截然不同，必须区分提示（用户明确反馈诉求）：
//   - 本轮执行过工具（文件已写入/命令已运行）：工具效果已生效，但模型
//     未对工具结果生成正文总结 → 明确告知"部分过程已保存 + 工具已生效"，
//     引导继续；
//   - 未执行任何工具：纯空回复（模型偶发只出思考）→ 直接提示重试。
//
// 本轮执行失败的部分消息已由 persistPartialOnError 落库（P4-I），刷新后
// 可见已发生的思考/工具过程；前端保留已流式显示的过程，正文显示占位。
func mapEmptyReplyError(err error, before []schema.Message, ag *agent.Session) error {
	if !errors.Is(err, agent.ErrEmptyReply) {
		return mapRunError(err)
	}
	hasTool := false
	for _, m := range diffNewMessages(before, ag.History()) {
		if m.Role == schema.RoleTool {
			hasTool = true
			break
		}
	}
	if hasTool {
		return apperr.New(apperr.CodeInternal,
			"模型已执行工具但未生成正文总结，本轮思考/工具过程已保存（工具的实际效果已生效，如文件已写入磁盘）。可让模型继续处理，或重试。")
	}
	return apperr.New(apperr.CodeInternal,
		"模型未生成任何内容（空回复），已保留本轮的思考/工具过程，请重试。")
}
