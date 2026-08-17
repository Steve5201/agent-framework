// agent_defaults.go —— 普通用户"智能体默认会话配置"读取接口（P3 反馈）。
//
// 用途：对话配置区"大模型"弹窗的回退链需要"智能体默认配置大模型"——
// 会话绑定的大模型失效（被删除/禁用）时，优先用智能体默认配置的大模型，
// 再找不到才回退系统默认。本接口面向普通用户（经 userAgentScope 隔离资源域），
// 返回指定智能体的默认配置对象；无默认配置文件 → 返回空对象（非 404）。
//
// 安全说明：默认配置含 enabled_tools/资源白名单等管理面信息，本接口仅面向
// 已登录用户且域名由 JWT 归属锁定（与 /v1/agent/kbs 同级的域隔离），
// 不暴露任何密钥类配置。
package gatewaysvc

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"go.uber.org/zap"
)

// agentDefaultsFileName 智能体默认会话配置文件名（与 agentsvc.DefaultsFileName
// 保持一致，两处变更必须同步——文件态配置平面契约）。
const agentDefaultsFileName = "agent_defaults.json"

// ListAgentDefaults GET /v1/agent/defaults?agent_id=xxx。
// 返回当前资源域的智能体默认会话配置对象（前端取 model 字段做回退）。
func (c *Clients) ListAgentDefaults(w http.ResponseWriter, r *http.Request) {
	agent, err := userAgentScope(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(c.AdminMcpConfigFile), agent, agentDefaultsFileName))
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"defaults": map[string]any{}, "agent_id": agent})
			return
		}
		c.Log.Error("读取智能体默认配置失败", zap.Error(err), zap.String("agent_id", agent))
		writeError(w, r, apperr.New(apperr.CodeInternal, "读取智能体默认配置失败"))
		return
	}
	var def map[string]any
	if err := json.Unmarshal(data, &def); err != nil {
		c.Log.Error("智能体默认配置 JSON 解析失败", zap.Error(err), zap.String("agent_id", agent))
		writeError(w, r, apperr.New(apperr.CodeInternal, "智能体默认配置格式非法"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": def, "agent_id": agent})
}
