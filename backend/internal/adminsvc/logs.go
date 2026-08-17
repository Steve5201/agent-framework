package adminsvc

// logs.go —— 操作日志查询模块（阶段4·日志管理模块）。
//
// 模块归属：对所有管理员可见（super_admin 全量；agent_admin/admin 仅本组域），
// 与 skills/mcp/kb 同级，故 ModuleVisible 走默认可见分支。
// 数据源：WithAudit 中间件按"目标域"落盘的 JSONL（见 audit.go）。
// 查询权限模型：
//   - super_admin：agent_id 参数可选（空 = 扫描全部域）；
//   - agent_admin / admin：强制锁定自身归属域（忽略参数，防越权窥探其它组）。

import (
	"net/http"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/identity"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// logsModule 操作日志查询模块。
type logsModule struct{}

func (logsModule) Key() string  { return "logs" }
func (logsModule) Name() string { return "操作日志" }
func (logsModule) Description() string {
	return "审计管理端操作：谁在何时对哪个智能体执行了什么操作"
}
func (logsModule) Implemented() bool { return true }

// Register 注册 GET /v1/admin/logs（只读，不写日志）。
func (m logsModule) Register(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/admin/logs", s.handleAdminListLogs)
}

// logsQuery 前端可传参数：agent_id（仅超管生效）、action、user_id、page、page_size。
func (s *Service) handleAdminListLogs(w http.ResponseWriter, r *http.Request) {
	role := identity.Role(r.Context())

	// 1) 域范围解析（多租户锁定）。
	var agents []string
	switch role {
	case "super_admin":
		if a := strings.TrimSpace(r.URL.Query().Get("agent_id")); a != "" {
			agents = []string{a} // 显式指定单域
		}
		// 空 = 全部域
	case "agent_admin", "admin":
		locked, err := agentScopeFor(r, "") // 锁定自身归属（空参数，忽略请求指定）
		if err != nil {
			writeError(w, r, err)
			return
		}
		agents = []string{locked}
	default:
		writeError(w, r, apperr.New(apperr.CodePermissionDenied, "无日志查看权限"))
		return
	}

	// 2) 过滤条件解析。
	filter := AuditFilter{AgentIDs: agents}
	filter.Action = strings.TrimSpace(r.URL.Query().Get("action"))
	if uid := strings.TrimSpace(r.URL.Query().Get("user_id")); uid != "" {
		id, err := strconv.ParseInt(uid, 10, 64)
		if err != nil {
			writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "user_id 必须是整数"))
			return
		}
		filter.UserID = id
	}
	filter.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	filter.PageSize, _ = strconv.Atoi(r.URL.Query().Get("page_size"))
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 || filter.PageSize > 200 {
		filter.PageSize = 50
	}

	// 3) 查询。
	entries, total, err := s.audit.List(filter)
	if err != nil {
		if s.log != nil {
			s.log.Warn("查询操作日志失败", zap.Error(err))
		}
		writeError(w, r, apperr.New(apperr.CodeInternal, "查询操作日志失败"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":      entries,
		"total":     total,
		"page":      filter.Page,
		"page_size": filter.PageSize,
	})
}
