// 管理端用量端点（数据管理模块）。
//
//   - GET /v1/usage/overview?days=N  平台用量总览（摘要 + 按日 + 多维聚合）
//
// 安全约束：8083 端口（compose 暴露到宿主）必须校验令牌，否则任何宿主机
// 进程都能枚举全平台用量与用户维度数据（成本与隐私敏感）。令牌未配置
// （LLM_ADMIN_TOKEN 为空）时管理端点禁用（503），对话/聚合等既有端点不受影响。
package llmsvc

import (
	"crypto/subtle"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// maxOverviewDays 用量总览最大窗口（与 agent 侧会话统计保持一致）。
const maxOverviewDays = 90

// UsageAdmin 管理端用量处理器（数据管理模块）。
type UsageAdmin struct {
	store UsageStore
	token string // LLM_ADMIN_TOKEN；空 = 管理端点禁用
	log   *zap.Logger
}

// NewUsageAdmin 创建管理端用量处理器。
func NewUsageAdmin(store UsageStore, token string, log *zap.Logger) *UsageAdmin {
	return &UsageAdmin{store: store, token: token, log: log}
}

// RegisterAdmin 注册管理端点（要求 X-Admin-Token）。
func (a *UsageAdmin) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/usage/overview", a.requireToken(a.handleOverview))
}

// requireToken 管理端点令牌中间件：令牌未配置 → 503；不匹配 → 401。
func (a *UsageAdmin) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			writeError(w, "", apperr.New(apperr.CodeUnavailable,
				"用量管理端点未启用：请为 llm-gateway 与 gateway 设置 LLM_ADMIN_TOKEN"))
			return
		}
		got := r.Header.Get("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
			writeError(w, "", apperr.New(apperr.CodeUnauthenticated, "管理令牌无效"))
			return
		}
		next(w, r)
	}
}

// handleOverview 返回平台用量总览（数据管理模块）。
// 参数：days = 窗口天数（1..90，缺省 30）。
func (a *UsageAdmin) handleOverview(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxOverviewDays {
			writeError(w, "", apperr.New(apperr.CodeInvalidArgument, "参数 days 须为 1..90 的整数"))
			return
		}
		days = n
	}
	ov, err := a.store.Overview(r.Context(), days)
	if err != nil {
		a.log.Error("usage overview failed", zap.Error(err))
		writeError(w, "", apperr.Wrap(apperr.CodeInternal, "查询用量总览失败", err))
		return
	}
	writeJSON(w, http.StatusOK, ov)
}
