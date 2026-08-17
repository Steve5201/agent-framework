// Package gatewaysvc 实现 gateway 的 HTTP 业务层（P2-E）。
//
// gateway 是系统唯一对外入口（HTTP :8080）：接收客户端请求 → 解析 JWT →
// 调用下游 gRPC 服务（auth:8081 / agent:8082）→ 统一返回 JSON/SSE。
//
// 分层约定：
//
//	浏览器/客户端 ──HTTP──▶ gateway(本包) ──gRPC──▶ auth / agent
//
// 关键职责：
//   - 鉴权：JWT access 校验，把 user_id 注入 gRPC metadata（x-user-id）；
//   - 协议转换：HTTP JSON ↔ gRPC 请求/响应，gRPC 错误 → 统一 JSON 错误体；
//   - SSE 透传：agent 的 StreamChat 流逐事件转成 SSE（打字机效果）。
package gatewaysvc

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-backend/internal/grpcx"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// Clients 聚合 gateway 依赖的下游客户端与本地组件。
type Clients struct {
	Auth  authpb.AuthServiceClient   // auth-service 认证
	Agent agentv1.AgentServiceClient // agent-service 会话与对话
	Rag   ragv1.RagServiceClient     // rag-service 知识库（P3-A）
	JWT   *auth.Manager              // 本地 JWT 校验（不把 access token 发给下游）
	Log   *zap.Logger

	// 管理端（admin panel）文件态配置路径（由 cmd 注入）。
	// SkillsDir/McpConfigFile 分别对应 agent 的 AGENT_SKILLS_DIR / AGENT_MCP_CONFIG_FILE；
	// AdminLogsDir 为操作审计日志根目录（阶段4·日志管理，按智能体域分目录）。
	AdminSkillsDir     string
	AdminMcpConfigFile string
	AdminMcpServersDir string
	AdminLogsDir       string
	// AdminKbUploadMaxMB 知识库文档上传单文件上限（ADMIN_KB_UPLOAD_MAX_MB，由 cmd
	// 注入；0 = adminsvc 内置默认 20MB）。
	AdminKbUploadMaxMB int
	// AdminSkillUploadMaxMB 技能/MCP zip 上传上限（ADMIN_SKILL_UPLOAD_MAX_MB，
	// 由 cmd 注入；0 = adminsvc 内置默认 10MB）。
	AdminSkillUploadMaxMB int
	// LlmGatewayBaseURL llm-gateway HTTP 基址（P2-AI 用量按智能体聚合查询）。
	// 空 = 管理端用量接口返回 503 提示未配置，不影响其它功能。
	LlmGatewayBaseURL string
	// LlmAdminToken llm-gateway 模型管理端点令牌（LLM_ADMIN_TOKEN，P3 大模型
	// 管理；空 = 管理端模型模块返回 503，不影响其它功能）。
	LlmAdminToken string
	// AgentHTTPAddr agent-service HTTP 基址（如 http://localhost:8182）。
	// 非空时 gateway 反向代理其 /files 端点（见 files_proxy.go），浏览器统一
	// 经 gateway 拉取工作区媒体；空 = 不挂代理（单服务/测试场景向后兼容）。
	AgentHTTPAddr string
	// ChatDocMaxBytes 聊天上传单文档大小上限字节（GATEWAY_CHAT_UPLOAD_MAX_MB，
	// 由 cmd 注入；0 = 默认 20MB）。须与 agent 侧 AGENT_CHAT_DOC_MAX_SIZE_MB 一致。
	ChatDocMaxBytes int64
}

// defaultChatDocMaxBytes 聊天上传单文档大小默认上限（20MB），
// 与 agent-service 侧 AGENT_CHAT_DOC_MAX_SIZE_MB 默认值保持一致。
const defaultChatDocMaxBytes = 20 << 20

// chatDocMaxBytes 返回聊天上传单文档大小上限；未配置（0）时回退默认值。
func (c *Clients) chatDocMaxBytes() int64 {
	if c.ChatDocMaxBytes <= 0 {
		return defaultChatDocMaxBytes
	}
	return c.ChatDocMaxBytes
}

// NewClients 连接下游 gRPC 服务并组装客户端。
// 返回关闭函数（进程退出时调用，释放连接）。
// auth/agent 连不上立即报错（尽早失败，避免启动一个"半瘫痪"网关）；
// rag-service 是软依赖：连不上仅记 Warn 并置 Rag=nil，网关照常启动，
// 管理端知识库模块对 nil 返回"未接入"提示（见 internal/adminsvc/kb.go）。
func NewClients(ctx context.Context, authAddr, agentAddr, ragAddr string, jwtMgr *auth.Manager, log *zap.Logger) (*Clients, func(), error) {
	authConn, err := grpcx.Dial(ctx, authAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("gatewaysvc: 连接 auth-service(%s) 失败: %w", authAddr, err)
	}
	agentConn, err := grpcx.Dial(ctx, agentAddr)
	if err != nil {
		_ = authConn.Close()
		return nil, nil, fmt.Errorf("gatewaysvc: 连接 agent-service(%s) 失败: %w", agentAddr, err)
	}

	clients := &Clients{
		Auth:  authpb.NewAuthServiceClient(authConn),
		Agent: agentv1.NewAgentServiceClient(agentConn),
		JWT:   jwtMgr,
		Log:   log,
	}

	closeAll := func() {
		_ = authConn.Close()
		_ = agentConn.Close()
	}

	// rag 软依赖：连接失败降级而非拒绝启动。
	ragConn, err := grpcx.Dial(ctx, ragAddr)
	if err != nil {
		log.Warn("rag-service 连接失败，知识库功能降级不可用（不影响其它服务）",
			zap.String("rag_addr", ragAddr), zap.Error(err))
		return clients, closeAll, nil
	}
	clients.Rag = ragv1.NewRagServiceClient(ragConn)
	closeAll = func() {
		_ = authConn.Close()
		_ = agentConn.Close()
		_ = ragConn.Close()
	}
	return clients, closeAll, nil
}
