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
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// defaultAgentID 资源域缺省值（与 adminsvc / 前端 DEFAULT_AGENT_ID 一致）。
const defaultAgentID = "tutor"

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
func userAgentScope(r *http.Request, requested string) (string, error) {
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
	return agent, nil
}

// agentScopeFor 解析会话/工具/资源类接口的请求域并校验访问权（多租户域隔离）。
// 与 userAgentScope 的区别：显式处理管理端域（''）与全门户（'*'），并对
// 管理员类角色的跨域请求**显式拒绝**（而非静默锁定），防越权枚举。
//
// 访问权语义：
//   - super_admin：管理端域（''）、全门户（'*'）、任意具体域；
//   - agent_admin / admin：管理端域（''）与自身 JWT 归属域，其它域拒绝；
//   - 普通用户 / 游客：锁定 JWT 自身归属（游客回退默认域），忽略请求参数。
func agentScopeFor(r *http.Request, requested string) (string, error) {
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
	return req, nil
}

// ListKBs GET /v1/agent/kbs?agent_id=tutor：列出当前资源域的知识库。
// 供对话配置区"知识库"弹窗拉取清单（会话级 kb_ids 勾选）；rag 未接入时 503。
func (c *Clients) ListKBs(w http.ResponseWriter, r *http.Request) {
	if c.Rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	agent, err := userAgentScope(r, r.URL.Query().Get("agent_id"))
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
