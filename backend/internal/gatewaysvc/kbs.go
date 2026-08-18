// kbs.go —— 普通用户知识库列表接口（会话配置勾选用，P3-A6）。
//
// 与 /v1/admin/kb（仅管理员）的差异：本接口面向普通用户，供对话配置区
// "知识库"弹窗拉取当前资源域的知识库清单（会话级 kb_ids 勾选）。
//
// 多租户隔离：域范围由用户身份锁定（userAgentScope）——
//   - super_admin（可切换智能体）：取请求显式指定的 agent_id；
//   - 其它用户：强制用 JWT 携带的自身归属，忽略请求参数（防越权枚举）。
package gatewaysvc

import (
	"net/http"
	"regexp"
	"strings"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/identity"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
	"google.golang.org/grpc/metadata"
)

// defaultAgentID 资源域缺省值（与 adminsvc / 前端 DEFAULT_AGENT_ID 一致）。
const defaultAgentID = "tutor"

// allAgentScopeID 超管全门户标识（与 authsvc allAgentID、前端 ALL_AGENT_ID 一致）。
// 它不是注册表里的真实智能体，仅作为超管专属门户（/agent/*、/login/*）标识。
const allAgentScopeID = "*"

// agentIDRe 智能体域 ID 白名单（与 authsvc/adminsvc 一致）：字母数字 + 中划线，≤64 字符。
var agentIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// kbLiteView 普通用户视角的知识库视图（只含会话配置所需的轻量字段）。
type kbLiteView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	DocCount    int32  `json:"doc_count"`
}

// userAgentScope 解析普通用户请求的资源域（多租户资源隔离）。
// 语义与 adminsvc.agentScopeFor 一致，但面向普通用户：
//   - super_admin（无智能体归属）：取请求显式指定的 agent_id（对话切换智能体后
//     会话归属目标域）；空 / "*"（全门户标识）回退默认域；
//   - 其它登录用户：锁定 JWT 携带的自身归属（identity.AgentID），忽略请求参数，
//     防"声明管 A 域、实际操作 B 域"的越权枚举。
//
// 解析出的具体域必须已注册且启用（严格多租户）：孤儿域 / 已停用域一律拒绝。
func (c *Clients) userAgentScope(r *http.Request, requested string) (string, error) {
	agent := ""
	if roleFrom(r) == "super_admin" {
		agent = strings.TrimSpace(requested)
		if agent == "*" {
			agent = ""
		}
	} else {
		agent = identity.AgentID(r.Context())
	}
	if agent == "" {
		agent = defaultAgentID
	}
	if !agentIDRe.MatchString(agent) {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的智能体 ID（仅限字母/数字/中划线，≤64 字符）")
	}
	if err := c.ensureAgentAccessible(r, agent); err != nil {
		return "", err
	}
	return agent, nil
}

// agentScopeFor 解析会话/工具/资源类接口的请求域并校验访问权（多租户域隔离）。
// 与 userAgentScope 的区别：显式处理管理端域（”）与全门户（'*'），并对
// 管理员类角色的跨域请求**显式拒绝**（而非静默锁定），防越权枚举。
//
// 访问权语义：
//   - super_admin：管理端域（”）、全门户（'*'）、任意具体域；
//   - agent_admin / admin：管理端域（”）与自身 JWT 归属域，其它域拒绝；
//   - 普通用户 / 游客：锁定 JWT 自身归属（游客回退默认域），忽略请求参数。
//
// 解析出的具体域必须已注册且启用（严格多租户）：孤儿域 / 已停用域一律拒绝，
// 超管访问孤儿域同样报错（'*' 与 '' 豁免）。
func (c *Clients) agentScopeFor(r *http.Request, requested string) (string, error) {
	role := roleFrom(r)
	req := strings.TrimSpace(requested)
	switch role {
	case "super_admin":
		if req == "*" || req == "" {
			return "", nil // 全门户 / 管理端域
		}
	case "agent_admin", "admin":
		if req == "" {
			return "", nil // 管理端域
		}
		if req == "*" {
			return "", apperr.New(apperr.CodePermissionDenied, "仅最高超管可访问全部域")
		}
		if req != identity.AgentID(r.Context()) {
			return "", apperr.New(apperr.CodePermissionDenied, "该账号不归属于智能体 "+req)
		}
	default: // user / guest：锁定自身归属，忽略请求参数
		req = identity.AgentID(r.Context())
		if req == "" {
			req = defaultAgentID
		}
	}
	if !agentIDRe.MatchString(req) {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的智能体 ID（仅限字母/数字/中划线，≤64 字符）")
	}
	// 严格多租户：具体域必须是已注册且启用的智能体（孤儿域 / 已停用域拒绝）。
	if err := c.ensureAgentAccessible(r, req); err != nil {
		return "", err
	}
	return req, nil
}

// ensureAgentAccessible 校验智能体域存在且启用（严格多租户域访问硬校验）。
// 空串（管理端域）与 '*'（超管全门户标识）不是注册表里的真实智能体，直接放行。
// 复用 authsvc.GetAgentPublic（游客负 user_id 会跳过用户存在性校验），
// NotFound → 孤儿域；status=0 → 已停用。供 agentScopeFor / userAgentScope /
// GetAgentDomain 统一调用，保证"各种访问地址必须有对应智能体才能访问"。
func (c *Clients) ensureAgentAccessible(r *http.Request, agentID string) error {
	if agentID == "" || agentID == allAgentScopeID {
		return nil
	}
	// 调用方身份必须经 gRPC 出站 metadata 透传（x-user-id），否则 authsvc
	// 无法识别调用者，直接回"缺少调用者身份"。
	userID, err := userIDFrom(r)
	if err != nil {
		return err
	}
	ar, err := c.Auth.GetAgentPublic(userCtx(r, userID), &authpb.GetAgentRequest{Id: agentID})
	if err != nil {
		if apperr.CodeOf(apperr.FromGRPCError(err)) == apperr.CodeNotFound {
			return apperr.New(apperr.CodeNotFound, "智能体 "+agentID+" 不存在或尚未创建，无法访问")
		}
		return apperr.FromGRPCError(err)
	}
	if ar.GetStatus() != 1 {
		return apperr.New(apperr.CodePermissionDenied, "智能体 "+agentID+" 已停用，无法访问")
	}
	return nil
}

// ListKBs GET /v1/agent/kbs?agent_id=tutor：列出当前资源域的知识库。
// 供对话配置区"知识库"弹窗拉取清单（会话级 kb_ids 勾选）；rag 未接入时 503。
func (c *Clients) ListKBs(w http.ResponseWriter, r *http.Request) {
	if c.Rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	agent, err := c.userAgentScope(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Rag.ListKnowledgeBases(r.Context(), &ragv1.ListKBRequest{AgentId: agent})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	bases := make([]kbLiteView, 0, len(resp.GetBases()))
	for _, pb := range resp.GetBases() {
		// 管理端停用的知识库对普通用户不可见（资源启停体系，P3 反馈）。
		if !pb.GetEnabled() {
			continue
		}
		bases = append(bases, kbLiteView{
			ID:          pb.GetId(),
			Name:        pb.GetName(),
			Description: pb.GetDescription(),
			DocCount:    pb.GetDocCount(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bases": bases, "agent_id": agent})
}

// agentDomainView 公开域校验接口的响应体（前端域守卫用）。
type agentDomainView struct {
	Exists bool   `json:"exists"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status int32  `json:"status"` // 1=启用 0=停用（exists=true 时有效）
}

// GetAgentDomain GET /v1/agent/domains/{id}：公开校验智能体域是否存在且启用。
// 供前端在切换 /agent/{id} 时先验域（孤儿域直接拒绝/踢回），免登录调用
// （域名"是否存在"是公开信息，不泄露私有字段）。调用方以游客身份查询：
// authsvc.GetAgentPublic 对游客（负 user_id）已跳过用户存在性校验。
func (c *Clients) GetAgentDomain(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || id == allAgentScopeID {
		writeJSON(w, http.StatusOK, agentDomainView{Exists: false, ID: id})
		return
	}
	// 以游客身份（-1）查询公开元数据；authsvc 会跳过用户校验，只查智能体表。
	ctx := metadata.AppendToOutgoingContext(r.Context(), "x-user-id", "-1")
	ar, err := c.Auth.GetAgentPublic(ctx, &authpb.GetAgentRequest{Id: id})
	if err != nil {
		// 智能体不存在 = 孤儿域；其余错误（依赖未就绪等）按真实错误返回。
		if apperr.CodeOf(apperr.FromGRPCError(err)) == apperr.CodeNotFound {
			writeJSON(w, http.StatusOK, agentDomainView{Exists: false, ID: id})
			return
		}
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	writeJSON(w, http.StatusOK, agentDomainView{
		Exists: true,
		ID:     ar.GetId(),
		Name:   ar.GetName(),
		Status: ar.GetStatus(),
	})
}
