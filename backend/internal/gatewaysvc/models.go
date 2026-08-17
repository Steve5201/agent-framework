// models.go —— gateway 公开模型列表（/v1/models，P3 大模型管理）。
//
// 会话配置区"大模型"下拉需要知道当前有哪些可用模型。模型数据的单一事实源
// 在 llm-gateway（models 表 + 运行期注册表），这里只是把它的公开只读端点
// 透传到 8080，供前端免鉴权拉取。公开端点只返回 name/provider_name/is_default，
// 不携带任何 API Key 与接入地址，因此无需管理令牌，且放在 authWhitelist。
package gatewaysvc

import (
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// ListPublicModels GET /v1/models：代理到 llm-gateway 公开端点。
// llm-gateway 未配置时返回 503（提示模型服务未接入），不影响其它功能。
func (c *Clients) ListPublicModels(w http.ResponseWriter, r *http.Request) {
	if c.LlmGatewayBaseURL == "" {
		writeError(w, r, apperr.New(apperr.CodeUnavailable,
			"llm-gateway 未配置（GATEWAY_LLM_BASE_URL），模型列表不可用"))
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		strings.TrimRight(c.LlmGatewayBaseURL, "/")+"/v1/models", nil)
	if err != nil {
		writeError(w, r, apperr.New(apperr.CodeInternal, "构造模型服务请求失败"))
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.Log.Warn("模型服务不可达", zap.Error(err))
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "模型服务不可达，请稍后再试"))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// 状态码与响应体原样透传（模型列表为空/上游 5xx 时错误文案直达前端）。
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		c.Log.Warn("模型服务响应透传失败", zap.Error(err))
	}
}
