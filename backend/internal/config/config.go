// Package config 基于 viper 提供统一配置加载（P2-16）。
//
// 加载顺序：默认值 < 环境变量。环境变量自动绑定（key 中的 . 替换为 _，
// 例如 db_password 对应 DB_PASSWORD）。数据库密码与 JWT 密钥必须通过
// 环境变量提供，严禁硬编码进代码或配置文件。
//
// 各服务在 Load 之后按需取用字段：
//   - gateway 等 HTTP 服务：HTTPPort；
//   - auth/agent 等 gRPC 服务：GRPCPort（健康检查走 gRPC health 服务）；
//   - auth-service：JWT 配置（签发双令牌）；
//   - 需要限流的服务：RateLimit 配置。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 服务通用配置。各服务在 Load 之后扩展业务字段。
type Config struct {
	ServiceName string   // 服务名，用于日志、注册等
	Env         string   // dev | prod
	HTTPPort    int      // HTTP 监听端口（gateway 等）
	GRPCPort    int      // gRPC 监听端口（auth/agent/llm 等）
	LogLevel    string   // debug | info | warn | error
	DB          DBConfig // PostgreSQL 连接配置
	JWT         JWTConfig
	RateLimit   RateLimitConfig
	LLM         LLMConfig     // llm-gateway 上游配置
	Agent       AgentConfig   // agent-service 智能体配置
	Gateway     GatewayConfig // gateway 下游服务地址
	Auth        AuthConfig    // auth-service 认证与初始管理员配置
	Admin       AdminConfig   // gateway 管理端（admin panel）配置
	RAG         RAGConfig     // rag-service 检索配置（P3-A）
	Doc         DocConfig     // 文档生成（render_document）限制（P4-L）
}

// AuthConfig auth-service 认证与初始管理员（超级管理员）配置。
type AuthConfig struct {
	// AdminUsername 初始超级管理员用户名（AUTH_ADMIN_USERNAME，默认 admin）。
	// 非空时服务启动会确保该管理员存在（不存在则以 AdminPassword 创建）。
	AdminUsername string
	// AdminPassword 初始超级管理员密码（AUTH_ADMIN_PASSWORD）。
	// 为空时使用内置引导默认值并记 WARN 日志（首次登录后应尽快修改）。
	AdminPassword string
}

// AdminConfig gateway 管理端（admin panel）配置。
// 管理端是"文件态配置平面"：skill 直接落盘到技能目录，MCP server 配置
// 写到 JSON 文件；agent 通过文件监听热加载，无需重启。
type AdminConfig struct {
	// SkillsDir 管理端写入技能文件的目录（ADMIN_SKILLS_DIR，默认空 = 工作目录下 skills/）。
	// 必须与 agent 的 AGENT_SKILLS_DIR 指向同一目录（共享卷/路径）。
	SkillsDir string
	// McpConfigFile 管理端写入 MCP server 列表的 JSON 文件
	// （ADMIN_MCP_CONFIG_FILE，默认空 = 工作目录下 mcp_servers.json）。
	// 必须与 agent 的 AGENT_MCP_CONFIG_FILE 指向同一文件。
	McpConfigFile string
	// McpServersDir 管理端上传的"本地 MCP 代码"存放目录
	// （ADMIN_MCP_SERVERS_DIR，默认空 = 工作目录下 mcp-servers/）。
	// 需与 agent 共享同一目录（agent 按 config 的 cwd 启动这些 stdio 服务）。
	McpServersDir string
	// LogsDir 操作审计日志根目录（ADMIN_LOGS_DIR，默认空 = 工作目录下 admin-logs/）。
	// 按智能体域分目录落盘（<LogsDir>/<agentID>/audit.jsonl），管理端日志模块查询。
	LogsDir string
	// KbUploadMaxMB 知识库文档上传单文件大小上限 MB（ADMIN_KB_UPLOAD_MAX_MB，
	// 默认 50）。
	KbUploadMaxMB int
	// SkillUploadMaxMB 技能/MCP zip 上传大小上限 MB（ADMIN_SKILL_UPLOAD_MAX_MB，
	// 默认 50）。
	SkillUploadMaxMB int
}

// DocConfig 文档生成（render_document）限制配置（P4-L）。
//
// 约定：新增的各类"限制类"参数统一在此登记并走 DOC_* / 对应 *_ 环境变量，
// 严禁在业务代码里散落硬编码上限。默认值 = 既有行为，未设置环境变量即沿用。
type DocConfig struct {
	MaxTitleLen    int // 标题长度上限（DOC_MAX_TITLE_LEN，默认 60）
	MaxSubtitleLen int // 副标题长度上限（DOC_MAX_SUBTITLE_LEN，默认 100）
	MaxHeadingLen  int // 章节标题长度上限（DOC_MAX_HEADING_LEN，默认 100）
	MaxSections    int // 章节数上限（DOC_MAX_SECTIONS，默认 50）
	MaxSectionBody int // 单节正文长度上限（DOC_MAX_SECTION_BODY，默认 8000）
	MaxBlocks      int // 单节富文本块数上限（DOC_MAX_BLOCKS，默认 100）
	MaxTotalBody   int // 全篇正文总长上限（DOC_MAX_TOTAL_BODY，默认 200000）
	MaxParagraph   int // 段落长度上限（DOC_MAX_PARAGRAPH，默认 4000）
	MaxListItems   int // 列表项数上限（DOC_MAX_LIST_ITEMS，默认 50）
	MaxListItemLen int // 列表单项长度上限（DOC_MAX_LIST_ITEM_LEN，默认 1000）
	MaxTableCols   int // 表格列数上限（DOC_MAX_TABLE_COLS，默认 12）
	MaxTableRows   int // 表格行数上限（DOC_MAX_TABLE_ROWS，默认 100）
	MaxTableCell   int // 单元格长度上限（DOC_MAX_TABLE_CELL，默认 500）
	MaxImageSrc    int // 图片 src 路径长度上限（DOC_MAX_IMAGE_SRC，默认 300）
	MaxSVG         int // 内联 SVG 长度上限（DOC_MAX_SVG，默认 20000）
	MaxCaption     int // 图注长度上限（DOC_MAX_CAPTION，默认 200）
	MaxWidth       int // 图片宽度上限 px（DOC_MAX_WIDTH，默认 2000）
	MaxFormula     int // 公式长度上限（DOC_MAX_FORMULA，默认 500）
	MaxCode        int // 代码块长度上限（DOC_MAX_CODE，默认 8000）
	MaxFooter      int // 页脚长度上限（DOC_MAX_FOOTER，默认 200）
	MaxFileName    int // 文件名长度上限（DOC_MAX_FILE_NAME，默认 40）
	// FormulaRender 公式块缺省渲染方式（DOC_FORMULA_RENDER，默认 image）。
	// image = 图片（稳健，matplotlib 渲染）；native = OMML 原生公式（可编辑/可复制，
	// 仅 docx 效果最佳，pptx 需 PPT2010+）。智能体仍可按块用 render 字段覆盖。
	FormulaRender string
}

// RAGConfig rag-service 检索配置（P3-A）。
//
// 两种 embedding 供应商（OpenAI 兼容协议）：
//   - 本地 Ollama（默认）：开源免费，随 docker 部署，无需 APIKey；
//     端点 http://localhost:11434/v1，模型 bge-m3（1024 维）。
//   - 硅基流动（云端，可选）：设置 SILICONFLOW_API_KEY 后自动切换，
//     端点 https://api.siliconflow.cn/v1，模型 BAAI/bge-m3。
//
// 两者均可通过 RAG_EMBEDDING_* 环境变量显式覆盖。未配置任何供应商时
// rag 服务照常启动，相关 RPC（Search/UpsertDocument）返回"未配置"提示。
type RAGConfig struct {
	// EmbeddingBaseURL 向量模型 OpenAI 兼容端点
	// （RAG_EMBEDDING_BASE_URL，默认 http://localhost:11434/v1）。
	EmbeddingBaseURL string
	// EmbeddingAPIKey 向量模型密钥：本地 Ollama 留空即可；
	// 硅基流动走 SILICONFLOW_API_KEY（或 RAG_EMBEDDING_API_KEY 覆盖）。
	EmbeddingAPIKey string
	// EmbeddingModel 向量模型名（RAG_EMBEDDING_MODEL，默认 bge-m3，1024 维）。
	EmbeddingModel string
	// EmbeddingDim 向量维度（RAG_EMBEDDING_DIM，默认 1024，须与迁移建表一致）。
	EmbeddingDim int
	// EmbeddingBatchSize 批量向量化大小（RAG_EMBEDDING_BATCH_SIZE，默认 16）。
	EmbeddingBatchSize int
	// IngestWorkers 摄取 worker 并发数（RAG_INGEST_WORKERS，默认 2）。
	IngestWorkers int
	// IngestPollInterval 摄取任务轮询间隔（RAG_INGEST_POLL_MS，默认 1s）。
	IngestPollInterval time.Duration
	// IngestMaxAttempts 摄取失败自动重试上限（RAG_INGEST_MAX_ATTEMPTS，默认 3）。
	// 瞬时故障（embedding 上游/网络/DB）指数退避后重试，超限才落 failed 终态。
	IngestMaxAttempts int
	// SandboxURL sandbox-service 地址（RAG_SANDBOX_URL，默认空 = 不启用外部解析）。
	// 非空时 pdf/docx/pptx 委托沙盒预置解析脚本（profile 模式）；空则仅支持
	// md/txt/html/xlsx（pdf/docx/pptx 返回"暂未接入"提示）。
	SandboxURL string
	// SandboxWorkRoot 共享卷容器内根目录（RAG_SANDBOX_WORK_ROOT，默认 /work，
	// 与 sandbox 容器同路径，rag 需挂载同一宿主目录）。
	SandboxWorkRoot string
	// SandboxUserID 解析沙盒固定使用的用户 id（RAG_SANDBOX_USER_ID，默认 1）。
	// 摄取是管理端后台上传，按上传者隔离意义不大；沙盒按该用户划分工作区。
	SandboxUserID int64
	// MediaCleanupInterval 无主媒体定期清理周期（RAG_MEDIA_CLEANUP_INTERVAL_HOURS，
	// 默认 6；0 = 禁用）。文档删除后 rag-media/<docID>/ 目录 best-effort 清理，
	// 残留的孤儿目录（DB 已无对应文档）在宽限期后由本 job 兜底删除。
	MediaCleanupInterval time.Duration
	// MediaCleanupTTL 无主媒体宽限期（RAG_MEDIA_CLEANUP_TTL_HOURS，默认 168=7 天）。
	// 文档删除后目录保留宽限期再删，防 docID 复用的误删。
	MediaCleanupTTL time.Duration
	// MaxDocMB 单篇文档字节上限（RAG_MAX_DOC_MB，默认 50）。
	// 摄取入队校验（沙盒解析/落库前拒绝超大文档）。与 admin 侧
	// ADMIN_KB_UPLOAD_MAX_MB、agent 侧 AGENT_CHAT_DOC_MAX_SIZE_MB 语义一致。
	MaxDocMB int
}

// GatewayConfig gateway 对接的下游服务地址。
type GatewayConfig struct {
	AuthAddr      string   // auth-service gRPC 地址
	AgentAddr     string   // agent-service gRPC 地址
	RagAddr       string   // rag-service gRPC 地址（P3-A 知识库管理）
	LlmBaseURL    string   // llm-gateway HTTP 基址（P2-AI 用量按智能体聚合查询）
	LlmAdminToken string   // llm-gateway 模型管理端点令牌（LLM_ADMIN_TOKEN，须与 llm-gateway 一致）
	CORSOrigins   []string // 允许的跨域来源（浏览器直连时必填；逗号分隔配置）
	// AgentHTTPAddr agent-service HTTP 基址（GATEWAY_AGENT_HTTP_ADDR，默认
	// http://localhost:8182）。非空时 gateway 反向代理其 /files 端点：浏览器/
	// 桌面端统一从 gateway 拉取工作区媒体（图片/文件），与模型输出的
	// files_base_url 直连 agent 是同一数据源——避免用户气泡图片（URL 指向
	// gateway）与 AI 气泡图片（直连 agent）渲染结果不一致的问题。
	AgentHTTPAddr string // agent-service HTTP 基址（/files 媒体代理目标）
	// ChatUploadMaxMB 聊天上传单文档大小上限 MB（GATEWAY_CHAT_UPLOAD_MAX_MB，
	// 默认 50）。gateway 先用该值拦截请求体（含 multipart 开销加 1MB 余量），
	// agent 侧以 AGENT_CHAT_DOC_MAX_SIZE_MB 做最终校验——两值应保持一致。
	ChatUploadMaxMB int
}

// AgentConfig agent-service 会话与模型配置。
type AgentConfig struct {
	// AgentID 本服务实例服务的智能体 ID（AGENT_ID，默认 tutor）。
	// 多租户资源域：skill 装配 <SkillsDir>/<AgentID>、MCP 装配
	// <McpConfigFile 目录>/<AgentID>/<文件名>，与 adminsvc 落盘规则一致。
	// 多个智能体实例通过该值区分各自的资源目录。
	AgentID      string // 智能体资源域 ID
	Model        string // 智能体默认模型
	SystemPrompt string // 系统提示词（走 framework agent.NewSession）
	MaxRounds    int    // 单次对话最大工具推理轮数
	MaxMessages  int    // 短期记忆窗口（历史恢复的最大消息数）
	LLMBaseURL   string // 上游 llm-gateway 地址（agent 不再直连厂商）
	LLMAPIKey    string // 调 llm-gateway 的"占位"密钥（网关不校验 Authorization，见下）

	// AutoApproveTools L2/L3 工具是否自动放行（AGENT_AUTO_APPROVE_TOOLS，默认 true）。
	// 个人本地部署：file_ops 写 / code_executor 无需审批 UI 直接执行（记审计日志）；
	// 生产多用户环境应置 false 并接入前端审批。
	AutoApproveTools bool
	// FilesBaseURL 本地媒体 URL 基址（AGENT_FILES_BASE_URL，默认空）。
	// 非空时注入渲染协议，模型用 <base>/files/<相对路径> 输出本地媒体 URL，
	// 由 agent HTTP 服务 /files 端点提供内容（默认 http://localhost:8182）。
	FilesBaseURL string
	// WebSearchBackend web_search 搜索后端（AGENT_WEB_SEARCH_BACKEND，默认 bing）。
	// bing = cn.bing.com（国内可直连）；duckduckgo = html.duckduckgo.com（海外）。
	WebSearchBackend string
	// CodeExecAllowlist code_executor 命令白名单正则（AGENT_CODE_EXEC_ALLOWLIST，
	// 逗号分隔，默认空）。非空时仅命中规则的命令可执行，其余拒绝。
	CodeExecAllowlist []string
	// SandboxURL 沙盒服务地址（AGENT_SANDBOX_URL，默认空 = 进程内本地执行）。
	// 非空时 code_executor 的代码执行委托给独立 sandbox-service（阶段2：
	// 禁网络 + 资源限制 + 每用户独立工作区）。部署值如 http://sandbox:8087。
	SandboxURL string
	// RagAddr rag-service gRPC 地址（AGENT_RAG_ADDR，默认空 = 不装配 kb_search）。
	// 非空时装配 kb_search 工具（L1 只读，混合检索课程知识库，结果带来源引用），
	// 检索范围强制限定本智能体域（AgentID），防跨域泄露。部署值如 rag:8085。
	RagAddr string
	// WorkRoot 用户工作区根（AGENT_WORK_ROOT，默认空 = 进程工作目录）。
	// 聊天上传文档落盘 users/<uid>/chat-files/<sid>/<file> 于此；容器内 /app
	// 与沙盒 /work 为同一共享卷（见 deploy/docker-compose.yml）。
	WorkRoot string
	// ChatSandboxURL 聊天文档解析沙盒地址（AGENT_CHAT_SANDBOX_URL，默认回退
	// SandboxURL）。pdf/docx/pptx 委托沙盒预置解析脚本；空 = 仅原生解析
	// （md/txt/html/xlsx），此时上传 pdf/docx/pptx 返回"需启用解析沙盒"。
	ChatSandboxURL string

	// SkillsDir 技能根目录（AGENT_SKILLS_DIR，默认空 = 工作目录下 skills/）。
	// 目录中每个含 SKILL.md 的子目录 = 一个技能（Anthropic Agent Skills 格式），
	// 自动注册为 skill_<名称> 工具；目录不存在 = 零技能（正常）。
	SkillsDir string
	// McpServersJSON 外部 MCP Server 列表 JSON（AGENT_MCP_SERVERS_JSON，默认空）。
	// 数组元素见 internal/tools/mcp.ServerConfig；每个 server 的工具经 tools/list
	// 动态发现，注册为 mcp_<server>_<工具>。
	McpServersJSON string
	// McpConfigFile MCP Server 配置文件路径（AGENT_MCP_CONFIG_FILE，默认空）。
	// 管理端把 MCP server 列表写到此文件（JSON 数组），agent 监听该文件热加载。
	// 优先级：文件存在且非空 → 用文件；否则回退 McpServersJSON 环境变量。
	McpConfigFile string

	// ChatDocMaxSizeMB 聊天上传单文档大小上限（AGENT_CHAT_DOC_MAX_SIZE_MB，默认 50MB）。
	// 与 gateway 侧 GATEWAY_CHAT_UPLOAD_MAX_MB 保持一致（gateway 先按同值拦截）。
	ChatDocMaxSizeMB int
	// ChatDocsPerSession 每会话文档数量上限（AGENT_CHAT_DOCS_PER_SESSION，默认 20）。
	ChatDocsPerSession int
	// ChatDocInjectRunes 解析工具单次返回正文上限（AGENT_CHAT_DOC_INJECT_RUNES，
	// 默认 8000）。read_document 缺省截断用。
	ChatDocInjectRunes int

	// OrchSubtaskTimeoutSec 编排子任务单次执行超时秒数（AGENT_ORCH_SUBTASK_TIMEOUT_SEC，
	// 默认 1800，0 = 不限制）。本地大模型响应慢 + 子任务常带工具多轮往返，
	// 默认放宽到 30 分钟（P4-L 宽松化）；单个角色还可通过角色级超时进一步
	// 覆盖（见 agentsvc.runSubTask），兼顾"长任务不误杀"与"短任务不空耗"。
	OrchSubtaskTimeoutSec int
	// OrchSubtaskRetries 编排子任务失败自动重试次数（AGENT_ORCH_SUBTASK_RETRIES，
	// 默认 1）。仅对可重试错误（5xx/429/网络/超时）重试；业务错误不重试。
	// 与 llm 层单请求重试互补：此处重试的是"整个子任务"，可覆盖多轮过程中的
	// 任一次失败，最大限度保证中间链不出错（避免前面成功、后面失败整体报废）。
	OrchSubtaskRetries int

	// 用户工作区磁盘配额（模块三·保护区配额，MB，0 = 不限）：
	// 只约束 protected/ 保护区（永不清除的长期资产）的上限，临时/散落内容由
	// 清理器 TTL 自动回收，不占配额。优先级：sandbox_disk_quota 表显式覆盖 >
	// 角色默认（本字段）。部署方按机器磁盘在 .env 调整即可。
	DiskQuotaUserMB       int64 // user        普通用户（AGENT_DISK_QUOTA_MB_USER，默认 256）
	DiskQuotaAdminMB      int64 // admin       普通管理员（AGENT_DISK_QUOTA_MB_ADMIN，默认 512）
	DiskQuotaAgentAdminMB int64 // agent_admin 智能体超管（AGENT_DISK_QUOTA_MB_AGENT_ADMIN，默认 1024）
	DiskQuotaSuperAdminMB int64 // super_admin 最高超管（AGENT_DISK_QUOTA_MB_SUPER_ADMIN，默认 0 = 不限）
}

// DBConfig PostgreSQL 连接配置。
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

// DSN 生成 pgx 兼容的连接串。
func (d DBConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode,
	)
}

// JWTConfig JWT 双令牌配置（auth-service 使用）。
type JWTConfig struct {
	Secret     string        // HMAC 签名密钥（JWT_SECRET 环境变量）
	AccessTTL  time.Duration // access 令牌有效期
	RefreshTTL time.Duration // refresh 令牌有效期
}

// RateLimitConfig 全局限流参数（gateway 按 IP / 用户维度）。
type RateLimitConfig struct {
	Rate  float64 // 每秒补充令牌数
	Burst int     // 突发容量
}

// LLMConfig llm-gateway 上游大模型配置。
// 密钥一律通过环境变量注入（DEEPSEEK_API_KEY），禁止硬编码。
type LLMConfig struct {
	UpstreamBaseURL string        // 上游厂商 OpenAI 兼容端点（默认 DeepSeek 官方）
	Model           string        // 默认模型名（入站请求未指定 model 时使用）
	APIKey          string        // 大模型密钥（DEEPSEEK_API_KEY）
	Timeout         time.Duration // 上游非流式请求整体超时
	MaxRetries      int           // 上游可重试错误的最大重试次数

	// 按用户限流与配额（P2-34）。
	RequestRate  float64 // 每用户每秒请求速率
	RequestBurst int     // 每用户突发容量
	// TokenQuotaMonth 普通用户每用户每月 token 配额（0=不限制）。
	// 与 AdminTokenQuotaMonth 的优先级：user_quota 表显式覆盖 > 角色默认
	// （管理员用 AdminTokenQuotaMonth，普通用户用 TokenQuotaMonth）。
	TokenQuotaMonth int64
	// AdminTokenQuotaMonth 管理员角色（super_admin/agent_admin/admin）每用户
	// 每月 token 配额（0=不限制）。与 TokenQuotaMonth 的优先级见上。
	AdminTokenQuotaMonth int64

	// 成本计价（美元 / 百万 token，P2-33 写 usage_logs.cost_usd）。
	// 默认值为示例价格，生产部署前请按厂商最新官方价通过环境变量调整。
	PromptPricePer1M     float64
	CompletionPricePer1M float64

	// AdminToken 模型管理端点管理令牌（LLM_ADMIN_TOKEN）。
	// 管理端点（/v1/admin/models*）须携带 X-Admin-Token 才能调用，
	// 与 gateway 的 adminsvc 共享同一环境变量；为空时管理端点禁用（503）。
	AdminToken string

	// AdminModelsJSON 启动批量播种的模型列表（ADMIN_MODELS，JSON 数组）。
	// 用于"多模型一次性接入"（如学校本地网关、多个厂商）：启动时逐个
	// CreateModel（已存在则跳过），再按 is_default 字段设置默认模型。
	// 条目字段与 ModelSpec 对齐：name / provider_name / base_url / api_key /
	// upstream_model / timeout_sec / max_retries / prompt_price_per_1m /
	// completion_price_per_1m / is_default / enabled。留空 = 不批量播种。
	AdminModelsJSON string
}

// Load 读取配置并返回。serviceName 用于默认库名（<serviceName>）；
// defaultPort 是该服务的默认端口，可被环境变量 HTTP_PORT / GRPC_PORT 覆盖。
// 校验规则：DB_PASSWORD 必须通过环境变量显式设置，缺失时返回错误。
func Load(serviceName string, defaultPort int) (*Config, error) {
	return LoadWith(serviceName, defaultPort, true)
}

// LoadWith 同 Load，requireDB=false 时跳过 DB_PASSWORD 必填校验。
// 供不连数据库的服务（如 gateway，只调下游 gRPC）使用。
func LoadWith(serviceName string, defaultPort int, requireDB bool) (*Config, error) {
	v := viper.New()

	// 默认值：本地开发默认连接本机 PG。
	v.SetDefault("env", "dev")
	v.SetDefault("http_port", defaultPort)
	v.SetDefault("grpc_port", defaultPort)
	v.SetDefault("log_level", "info")
	v.SetDefault("db_host", "localhost")
	v.SetDefault("db_port", 5432)
	v.SetDefault("db_user", "postgres")
	v.SetDefault("db_password", "")
	v.SetDefault("db_name", serviceName)
	v.SetDefault("db_sslmode", "disable")

	// JWT 默认时长：access 15 分钟，refresh 7 天（与 P2-12 auth 包约定一致）。
	v.SetDefault("jwt_secret", "")
	v.SetDefault("jwt_access_ttl", "15m")
	v.SetDefault("jwt_refresh_ttl", "168h") // 7*24h

	// 限流默认值：每秒 100 个令牌，突发 50。
	v.SetDefault("rate_limit_rate", 100.0)
	v.SetDefault("rate_limit_burst", 50)

	// llm-gateway 上游配置默认值（P2-30）。
	v.SetDefault("llm_upstream_base_url", "https://api.deepseek.com")
	v.SetDefault("llm_model", "deepseek-v4-flash")
	v.SetDefault("llm_api_key", "")     // 由 DEEPSEEK_API_KEY 环境变量注入
	v.SetDefault("llm_timeout", "300s") // 上游非流式请求整体超时（P4-M 宽松化：编排子任务请求大、生成久，120s 易触发上游超时 504）
	v.SetDefault("llm_max_retries", 3)
	v.SetDefault("llm_request_rate", 60.0)          // 每用户每秒请求数
	v.SetDefault("llm_request_burst", 20)           // 每用户突发容量
	v.SetDefault("llm_token_quota_month", 10000000) // 每用户每月 token 配额（0=不限制）
	v.SetDefault("llm_admin_token_quota_month", 0)  // 管理员默认配额（0=不限制；可经 user_quota 表按用户覆盖）
	v.SetDefault("llm_prompt_price_per_1m", 0.27)   // 示例价格（美元/百万 token）
	v.SetDefault("llm_completion_price_per_1m", 1.10)

	// agent-service 配置默认值（P2-40）。
	// agent_id 多租户资源域：默认 tutor（与 adminsvc 默认域、authsvc 播种一致）。
	v.SetDefault("agent_id", "tutor")
	v.SetDefault("agent_model", "deepseek-v4-flash")
	v.SetDefault("agent_system_prompt", "你是智能助手，请用中文简洁、准确地回答问题。你拥有多种工具能力，需要实时信息、精确计算、读写文件、运行代码、解析或生成文档等任务时，应主动调用对应工具完成，不要声称没有工具或凭空编造结果。")
	v.SetDefault("agent_max_rounds", 12)                           // 单次对话最多 12 轮工具推理（工具多轮往返场景宽松化，P4-L 调优）
	v.SetDefault("agent_max_messages", 2000)                       // 短期记忆窗口 2000 条（适配 100k 上下文窗口的模型）
	v.SetDefault("agent_llm_base_url", "http://localhost:8083/v1") // 上游 llm-gateway（带 /v1：framework 拼 baseURL+/chat/completions）
	// 占位密钥：llm-gateway 的 /v1/chat/completions 不校验 Authorization
	// （user_id 走 X-User-Id），仅为满足 framework 客户端"APIKey 非空"校验。
	// agent 永不接触真实厂商密钥；将来网关若加校验，用真实 key 覆盖此值。
	v.SetDefault("agent_llm_api_key", "internal-llm-gateway")
	// 内置工具集：L2/L3 工具自动放行（个人本地部署默认开，见 approveToolCall）。
	v.SetDefault("agent_auto_approve_tools", true)
	// 本地媒体 URL 基址：空 = 不注入 /files 渲染协议（AGENT_FILES_BASE_URL）。
	v.SetDefault("agent_files_base_url", "")
	// web_search 搜索后端：bing（国内可直连）| duckduckgo（海外）。
	v.SetDefault("agent_web_search_backend", "bing")
	// code_executor 命令白名单（逗号分隔正则）；空 = 仅黑名单限制。
	v.SetDefault("agent_code_exec_allowlist", "")
	// rag-service gRPC 地址（P3-A6）；空 = 不装配 kb_search 工具。
	v.SetDefault("agent_rag_addr", "")

	// gateway 下游服务地址（P2-E）。
	v.SetDefault("gateway_auth_addr", "localhost:8081")
	v.SetDefault("gateway_agent_addr", "localhost:8082")
	v.SetDefault("gateway_rag_addr", "localhost:8085") // rag-service（P3-A）
	// agent-service HTTP 基址：gateway 反代其 /files 端点（媒体代理目标，见
	// GatewayConfig.AgentHTTPAddr）。默认与 agent HTTP 服务监听一致。
	v.SetDefault("gateway_agent_http_addr", "http://localhost:8182")
	// 聊天上传单文档大小上限（与 agent 侧 AGENT_CHAT_DOC_MAX_SIZE_MB 对齐）。
	v.SetDefault("gateway_chat_upload_max_mb", 50)
	// 跨域白名单：web(React :3000) 与 desktop(Tauri dev :3001) 开发地址；
	// tauri://localhost 是 Tauri 2 打包后加载本地前端资源的生产 origin（WebView2）。
	// 生产按实际前端域名收紧（默认不含 *）。
	v.SetDefault("gateway_cors_origins", "http://localhost:3000,http://localhost:3001,http://localhost:1420,tauri://localhost")
	// gateway 管理端查询 llm-gateway 用量聚合（P2-AI；与 agent_llm_base_url 同基址不带 /v1）。
	v.SetDefault("gateway_llm_base_url", "http://localhost:8083")

	// auth-service 初始超级管理员（AUTH_ADMIN_*）。
	v.SetDefault("auth_admin_username", "admin")
	v.SetDefault("auth_admin_password", "")

	// MCP 配置文件：默认工作目录下 mcp_servers.json（与 adminsvc 默认一致，
	// 本地 go run 均在 backend/ 下运行；docker 用 AGENT_MCP_CONFIG_FILE 显式指到 /app）。
	v.SetDefault("agent_mcp_config_file", "mcp_servers.json")
	// 聊天上传文档限制（模块二，P4-L 收口 env）：单文档 50MB、每会话 20 份、
	// 解析正文缺省截断 8000 字。与 gateway 侧 GATEWAY_CHAT_UPLOAD_MAX_MB 对齐。
	v.SetDefault("agent_chat_doc_max_size_mb", 50)
	v.SetDefault("agent_chat_docs_per_session", 20)
	v.SetDefault("agent_chat_doc_inject_runes", 8000)
	v.SetDefault("agent_orch_subtask_timeout_sec", 1800) // 编排子任务单任务超时 1800s（30 分钟，0 = 不限制）；本地大模型响应慢，硬限制宽松化优先保证任务成功（P4-L）
	v.SetDefault("agent_orch_subtask_retries", 1)
	// 用户工作区磁盘配额（模块三·保护区配额，MB，0 = 不限）：super_admin 默认
	// 不限；部署方按机器磁盘在 .env 覆盖（见 AgentConfig.DiskQuota*MB 注释）。
	v.SetDefault("agent_disk_quota_mb_user", 256)
	v.SetDefault("agent_disk_quota_mb_admin", 512)
	v.SetDefault("agent_disk_quota_mb_agent_admin", 1024)
	v.SetDefault("agent_disk_quota_mb_super_admin", 0)

	// gateway 管理端（admin panel）配置：默认与 agent 同目录/同文件
	// （本地 go run 均在 backend/ 下运行；docker 里用 ADMIN_* 显式指到 /app）。
	v.SetDefault("admin_skills_dir", "")
	v.SetDefault("admin_mcp_config_file", "")
	v.SetDefault("admin_mcp_servers_dir", "")
	v.SetDefault("admin_logs_dir", "")
	// 管理端上传限制（P4-L 收口 env）。
	v.SetDefault("admin_kb_upload_max_mb", 20)
	v.SetDefault("admin_skill_upload_max_mb", 10)

	// rag-service（P3-A）。
	// 默认本地 Ollama（开源免费、随 docker 部署，无需 APIKey）；设置
	// SILICONFLOW_API_KEY 后自动切换为硅基流动云端（BGE-M3，见 Load 末尾）。
	v.SetDefault("rag_embedding_base_url", "http://localhost:11434/v1")
	v.SetDefault("rag_embedding_model", "bge-m3")
	v.SetDefault("rag_embedding_dim", 1024) // 须与 migrations/rag 建表 vector(1024) 一致
	v.SetDefault("rag_embedding_batch_size", 16)
	v.SetDefault("rag_ingest_workers", 2)
	v.SetDefault("rag_ingest_poll_ms", 1000)
	// 摄取失败自动重试 3 次（10s/1m/5m 指数退避），仍失败才落 failed 终态。
	v.SetDefault("rag_ingest_max_attempts", 3)
	// 文档解析沙盒（P3-A3b）：默认空 = 不启用外部解析；compose 部署时指向
	// sandbox 服务并挂载共享卷（RAG_SANDBOX_WORK_ROOT=/work 与 sandbox 同路径）。
	v.SetDefault("rag_sandbox_url", "")
	v.SetDefault("rag_sandbox_work_root", "/work")
	v.SetDefault("rag_sandbox_user_id", int64(1))
	// 无主媒体定期清理（模块三）：周期 6h（0=禁用），TTL 7 天。
	v.SetDefault("rag_media_cleanup_interval_hours", 6)
	v.SetDefault("rag_media_cleanup_ttl_hours", 168)
	// 单篇文档字节上限（摄取入队校验；与 ADMIN_KB_UPLOAD_MAX_MB 语义一致）。
	v.SetDefault("rag_max_doc_mb", 50)

	// 文档生成（render_document）限制（P4-L）：默认值 = 既有硬编码行为，
	// 需要调整时经 DOC_* 环境变量覆盖（见 DocConfig 字段注释）。
	v.SetDefault("doc_max_title_len", 60)
	v.SetDefault("doc_max_subtitle_len", 100)
	v.SetDefault("doc_max_heading_len", 100)
	v.SetDefault("doc_max_sections", 50)
	v.SetDefault("doc_max_section_body", 8000)
	v.SetDefault("doc_max_blocks", 100)
	v.SetDefault("doc_max_total_body", 200000)
	v.SetDefault("doc_max_paragraph", 4000)
	v.SetDefault("doc_max_list_items", 50)
	v.SetDefault("doc_max_list_item_len", 1000)
	v.SetDefault("doc_max_table_cols", 12)
	v.SetDefault("doc_max_table_rows", 100)
	v.SetDefault("doc_max_table_cell", 500)
	v.SetDefault("doc_max_image_src", 300)
	v.SetDefault("doc_max_svg", 20000)
	v.SetDefault("doc_max_caption", 200)
	v.SetDefault("doc_max_width", 2000)
	v.SetDefault("doc_max_formula", 500)
	v.SetDefault("doc_max_code", 8000)
	v.SetDefault("doc_max_footer", 200)
	v.SetDefault("doc_max_file_name", 40)
	// 公式缺省渲染方式：image（默认，稳健）| native（OMML 可编辑）。
	v.SetDefault("doc_formula_render", "image")

	// 自动绑定环境变量：DB_PASSWORD、JWT_SECRET、GRPC_PORT 等。
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	// llm-gateway 密钥统一走 DEEPSEEK_API_KEY（与 framework/examples 约定一致）。
	v.BindEnv("llm_api_key", "DEEPSEEK_API_KEY")
	// 模型管理端点令牌：llm-gateway 与 gateway（adminsvc 代理）共享同一环境变量。
	v.BindEnv("llm_admin_token", "LLM_ADMIN_TOKEN")
	// rag 向量模型密钥：优先 SILICONFLOW_API_KEY（供应商惯例名），
	// RAG_EMBEDDING_API_KEY 可显式覆盖。
	v.BindEnv("rag_embedding_api_key", "SILICONFLOW_API_KEY")
	// 聊天文档解析沙盒：默认回退 code_executor 沙盒地址（同一 sandbox 服务）；
	// 独立部署时用 AGENT_CHAT_SANDBOX_URL 显式覆盖。须在 AutomaticEnv 之后取值。
	v.SetDefault("agent_chat_sandbox_url", v.GetString("agent_sandbox_url"))

	// 云端保留：设置了 SILICONFLOW_API_KEY 且未显式指定 embedding 端点/模型时，
	// 自动切换为硅基流动（本地 Ollama 与云端共用一套默认维度 1024）。
	if key := v.GetString("rag_embedding_api_key"); key != "" {
		if _, ok := os.LookupEnv("RAG_EMBEDDING_BASE_URL"); !ok {
			v.SetDefault("rag_embedding_base_url", "https://api.siliconflow.cn/v1")
		}
		if _, ok := os.LookupEnv("RAG_EMBEDDING_MODEL"); !ok {
			v.SetDefault("rag_embedding_model", "BAAI/bge-m3")
		}
	}

	cfg := &Config{
		ServiceName: serviceName,
		Env:         v.GetString("env"),
		HTTPPort:    v.GetInt("http_port"),
		GRPCPort:    v.GetInt("grpc_port"),
		LogLevel:    v.GetString("log_level"),
		DB: DBConfig{
			Host:     v.GetString("db_host"),
			Port:     v.GetInt("db_port"),
			User:     v.GetString("db_user"),
			Password: v.GetString("db_password"),
			Name:     v.GetString("db_name"),
			SSLMode:  v.GetString("db_sslmode"),
		},
		JWT: JWTConfig{
			Secret:     v.GetString("jwt_secret"),
			AccessTTL:  v.GetDuration("jwt_access_ttl"),
			RefreshTTL: v.GetDuration("jwt_refresh_ttl"),
		},
		RateLimit: RateLimitConfig{
			Rate:  v.GetFloat64("rate_limit_rate"),
			Burst: v.GetInt("rate_limit_burst"),
		},
		LLM: LLMConfig{
			UpstreamBaseURL:      v.GetString("llm_upstream_base_url"),
			Model:                v.GetString("llm_model"),
			APIKey:               v.GetString("llm_api_key"),
			Timeout:              v.GetDuration("llm_timeout"),
			MaxRetries:           v.GetInt("llm_max_retries"),
			RequestRate:          v.GetFloat64("llm_request_rate"),
			RequestBurst:         v.GetInt("llm_request_burst"),
			TokenQuotaMonth:      v.GetInt64("llm_token_quota_month"),
			AdminTokenQuotaMonth: v.GetInt64("llm_admin_token_quota_month"),
			PromptPricePer1M:     v.GetFloat64("llm_prompt_price_per_1m"),
			CompletionPricePer1M: v.GetFloat64("llm_completion_price_per_1m"),
			AdminToken:           v.GetString("llm_admin_token"),
			AdminModelsJSON:      v.GetString("admin_models"),
		},
		Agent: AgentConfig{
			AgentID:            v.GetString("agent_id"),
			Model:              v.GetString("agent_model"),
			SystemPrompt:       v.GetString("agent_system_prompt"),
			MaxRounds:          v.GetInt("agent_max_rounds"),
			MaxMessages:        v.GetInt("agent_max_messages"),
			LLMBaseURL:         v.GetString("agent_llm_base_url"),
			LLMAPIKey:          v.GetString("agent_llm_api_key"),
			AutoApproveTools:   v.GetBool("agent_auto_approve_tools"),
			FilesBaseURL:       v.GetString("agent_files_base_url"),
			WebSearchBackend:   v.GetString("agent_web_search_backend"),
			CodeExecAllowlist:  splitComma(v.GetString("agent_code_exec_allowlist")),
			SandboxURL:         v.GetString("agent_sandbox_url"),
			RagAddr:            v.GetString("agent_rag_addr"),
			WorkRoot:           v.GetString("agent_work_root"),
			ChatSandboxURL:     v.GetString("agent_chat_sandbox_url"),
			SkillsDir:          v.GetString("agent_skills_dir"),
			McpServersJSON:     v.GetString("agent_mcp_servers_json"),
			McpConfigFile:      v.GetString("agent_mcp_config_file"),
			ChatDocMaxSizeMB:   v.GetInt("agent_chat_doc_max_size_mb"),
			ChatDocsPerSession: v.GetInt("agent_chat_docs_per_session"),
			ChatDocInjectRunes: v.GetInt("agent_chat_doc_inject_runes"),
			// 编排子任务韧性（P4-L 收口 env）：超时秒数 + 失败重试次数。
			OrchSubtaskTimeoutSec: v.GetInt("agent_orch_subtask_timeout_sec"),
			OrchSubtaskRetries:    v.GetInt("agent_orch_subtask_retries"),
			// 用户工作区磁盘配额（模块三·保护区配额，MB，0 = 不限）。
			DiskQuotaUserMB:       v.GetInt64("agent_disk_quota_mb_user"),
			DiskQuotaAdminMB:      v.GetInt64("agent_disk_quota_mb_admin"),
			DiskQuotaAgentAdminMB: v.GetInt64("agent_disk_quota_mb_agent_admin"),
			DiskQuotaSuperAdminMB: v.GetInt64("agent_disk_quota_mb_super_admin"),
		},
		Gateway: GatewayConfig{
			AuthAddr:        v.GetString("gateway_auth_addr"),
			AgentAddr:       v.GetString("gateway_agent_addr"),
			RagAddr:         v.GetString("gateway_rag_addr"),
			LlmBaseURL:      v.GetString("gateway_llm_base_url"),
			LlmAdminToken:   v.GetString("llm_admin_token"),
			CORSOrigins:     splitComma(v.GetString("gateway_cors_origins")),
			AgentHTTPAddr:   v.GetString("gateway_agent_http_addr"),
			ChatUploadMaxMB: v.GetInt("gateway_chat_upload_max_mb"),
		},
		Auth: AuthConfig{
			AdminUsername: v.GetString("auth_admin_username"),
			AdminPassword: v.GetString("auth_admin_password"),
		},
		Admin: AdminConfig{
			SkillsDir:        v.GetString("admin_skills_dir"),
			McpConfigFile:    v.GetString("admin_mcp_config_file"),
			McpServersDir:    v.GetString("admin_mcp_servers_dir"),
			LogsDir:          v.GetString("admin_logs_dir"),
			KbUploadMaxMB:    v.GetInt("admin_kb_upload_max_mb"),
			SkillUploadMaxMB: v.GetInt("admin_skill_upload_max_mb"),
		},
		RAG: RAGConfig{
			EmbeddingBaseURL:     v.GetString("rag_embedding_base_url"),
			EmbeddingAPIKey:      v.GetString("rag_embedding_api_key"),
			EmbeddingModel:       v.GetString("rag_embedding_model"),
			EmbeddingDim:         v.GetInt("rag_embedding_dim"),
			EmbeddingBatchSize:   v.GetInt("rag_embedding_batch_size"),
			IngestWorkers:        v.GetInt("rag_ingest_workers"),
			IngestPollInterval:   time.Duration(v.GetInt("rag_ingest_poll_ms")) * time.Millisecond,
			IngestMaxAttempts:    v.GetInt("rag_ingest_max_attempts"),
			SandboxURL:           v.GetString("rag_sandbox_url"),
			SandboxWorkRoot:      v.GetString("rag_sandbox_work_root"),
			SandboxUserID:        v.GetInt64("rag_sandbox_user_id"),
			MediaCleanupInterval: time.Duration(v.GetInt("rag_media_cleanup_interval_hours")) * time.Hour,
			MediaCleanupTTL:      time.Duration(v.GetInt("rag_media_cleanup_ttl_hours")) * time.Hour,
			MaxDocMB:             v.GetInt("rag_max_doc_mb"),
		},
		Doc: DocConfig{
			MaxTitleLen:    v.GetInt("doc_max_title_len"),
			MaxSubtitleLen: v.GetInt("doc_max_subtitle_len"),
			MaxHeadingLen:  v.GetInt("doc_max_heading_len"),
			MaxSections:    v.GetInt("doc_max_sections"),
			MaxSectionBody: v.GetInt("doc_max_section_body"),
			MaxBlocks:      v.GetInt("doc_max_blocks"),
			MaxTotalBody:   v.GetInt("doc_max_total_body"),
			MaxParagraph:   v.GetInt("doc_max_paragraph"),
			MaxListItems:   v.GetInt("doc_max_list_items"),
			MaxListItemLen: v.GetInt("doc_max_list_item_len"),
			MaxTableCols:   v.GetInt("doc_max_table_cols"),
			MaxTableRows:   v.GetInt("doc_max_table_rows"),
			MaxTableCell:   v.GetInt("doc_max_table_cell"),
			MaxImageSrc:    v.GetInt("doc_max_image_src"),
			MaxSVG:         v.GetInt("doc_max_svg"),
			MaxCaption:     v.GetInt("doc_max_caption"),
			MaxWidth:       v.GetInt("doc_max_width"),
			MaxFormula:     v.GetInt("doc_max_formula"),
			MaxCode:        v.GetInt("doc_max_code"),
			MaxFooter:      v.GetInt("doc_max_footer"),
			MaxFileName:    v.GetInt("doc_max_file_name"),
			FormulaRender:  v.GetString("doc_formula_render"),
		},
	}

	if requireDB && cfg.DB.Password == "" {
		return nil, fmt.Errorf(
			"config: 环境变量 DB_PASSWORD 未设置，请通过环境变量提供数据库密码",
		)
	}

	return cfg, nil
}

// splitComma 按逗号切分配置字符串为列表（去除空白与空项）。
func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
