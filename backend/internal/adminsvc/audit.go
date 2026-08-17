package adminsvc

// audit.go —— 管理端操作审计日志（阶段4·日志管理模块）。
//
// 定位：与 skill/mcp 一致，审计日志走"文件态"——按智能体域分目录落盘
// （<LogsDir>/<agentID>/audit.jsonl，JSONL 追加写），单行一条审计记录。
//
// 数据流：gateway 的 RequireAdmin 之后、adminsvc handler 之前，WithAudit
// 中间件记录全部 /v1/admin/* 写操作（POST/PUT/DELETE/PATCH）；GET（只读）
// 不记录，避免查询本身刷爆日志。handler 返回后统一写盘（拿到最终状态码
// 与耗时），写盘失败仅记 zap 日志，不影响业务响应。
//
// 多租户隔离：日志文件按"操作目标域"（target_agent）分目录——
//   - super_admin：操作任意域（query.agent_id 指定），记录到对应域；
//   - agent_admin / admin：目标域被锁定为自身归属，只能写到本组域。
// 查询时（logs.go）agent_admin/admin 只能读到本组域文件；super_admin
// 可指定 agent_id（缺省扫描全部域）——与资源隔离模型完全一致。

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/identity"
)

// auditFileName 每个智能体域下的审计日志文件名。
const auditFileName = "audit.jsonl"

// AuditEntry 单条审计记录（JSONL 一行）。
type AuditEntry struct {
	TS time.Time `json:"ts"`
	// UserID 操作者（管理员）用户 ID。
	UserID int64 `json:"user_id"`
	// Role 操作者角色（super_admin / agent_admin / admin）。
	Role string `json:"role"`
	// ActorAgent 操作者归属域（agent_admin/admin 非空；super_admin 无归属为空）。
	ActorAgent string `json:"actor_agent,omitempty"`
	// TargetAgent 操作目标域（写日志文件所在的域）。
	TargetAgent string `json:"target_agent"`
	// Action 动作（模块.动词[.子路径]），如 skills.create / mcp.delete / kb.documents.upload。
	Action string `json:"action"`
	// Method / Path 原始请求（定位与排障用）。
	Method string `json:"method"`
	Path   string `json:"path"`
	// Status 响应状态码（200/201/400/403…；写盘前由中间件捕获）。
	Status int `json:"status"`
	// RequestID 全链路请求 ID（可与运行日志/错误响应关联）。
	RequestID string `json:"request_id"`
	// LatencyMS handler 处理耗时（毫秒）。
	LatencyMS int64 `json:"latency_ms"`
}

// AuditFilter 日志查询过滤条件（logs.go 使用）。
type AuditFilter struct {
	// AgentIDs 目标域白名单；空 = 全部域（仅 super_admin 可达）。
	AgentIDs []string
	// Action 动作前缀过滤（如 "skills" 或 "skills.update"）；空 = 不过滤。
	Action string
	// UserID 操作者过滤；0 = 不过滤。
	UserID int64
	// Page / PageSize 分页（1-based；PageSize ≤ 0 时默认 50）。
	Page     int
	PageSize int
}

// AuditStore 审计日志存储：按域追加写 + 全量扫描过滤分页。
// 单进程模型：gateway 只有一个 adminsvc 实例，用互斥锁串行化并发追加，
// 避免多 handler 同时写同一文件导致交错（进程内已足够，无需跨进程锁）。
type AuditStore struct {
	root string
	log  *zap.Logger
	mu   sync.Mutex
}

// newAuditStore 创建审计日志存储。root 为空时落到工作目录下 "admin-logs/"。
func newAuditStore(root string, log *zap.Logger) *AuditStore {
	if strings.TrimSpace(root) == "" {
		root = "admin-logs"
	}
	return &AuditStore{root: root, log: log}
}

// filePath 返回某域日志文件路径。agentID 必须已通过 agentIDRe 白名单校验
// （调用方保证），此处再防御一次防目录穿越。
func (a *AuditStore) filePath(agentID string) (string, error) {
	if !agentIDRe.MatchString(agentID) {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的智能体 ID")
	}
	return filepath.Join(a.root, agentID, auditFileName), nil
}

// Append 追加一条审计记录到目标域日志文件。
// 写盘失败仅记 zap 日志（审计不可因 IO 失败阻塞业务响应）。
func (a *AuditStore) Append(agentID string, e AuditEntry) error {
	path, err := a.filePath(agentID)
	if err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// List 扫描全部（或指定）域的日志文件，过滤后按时间倒序分页返回。
// 返回 (条目, 总数)；total 为过滤后的全量条数（供前端分页展示）。
func (a *AuditStore) List(filter AuditFilter) ([]AuditEntry, int, error) {
	entries := make([]AuditEntry, 0, 128)
	visit := func(agentID, path string) error {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 该域尚无日志，跳过
			}
			return err
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20) // 单行上限 1MB，防异常行撑爆内存
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var e AuditEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				continue // 跳过损坏行（容忍旧格式/半截写入）
			}
			if !a.match(&e, filter) {
				continue
			}
			// 文件路径目录即权威目标域（兜底纠正空字段的旧数据）。
			if e.TargetAgent == "" {
				e.TargetAgent = agentID
			}
			entries = append(entries, e)
		}
		return sc.Err()
	}

	// 域范围：显式指定则只读这些域；否则扫描全部子目录。
	if len(filter.AgentIDs) > 0 {
		for _, id := range filter.AgentIDs {
			if !agentIDRe.MatchString(id) {
				continue
			}
			p, err := a.filePath(id)
			if err != nil {
				continue
			}
			if err := visit(id, p); err != nil {
				return nil, 0, err
			}
		}
	} else {
		dirs, err := os.ReadDir(a.root)
		if err != nil {
			if os.IsNotExist(err) {
				return []AuditEntry{}, 0, nil
			}
			return nil, 0, err
		}
		for _, d := range dirs {
			if !d.IsDir() || !agentIDRe.MatchString(d.Name()) {
				continue
			}
			if err := visit(d.Name(), filepath.Join(a.root, d.Name(), auditFileName)); err != nil {
				return nil, 0, err
			}
		}
	}

	// 时间倒序（新 → 旧）。
	sort.Slice(entries, func(i, j int) bool { return entries[i].TS.After(entries[j].TS) })

	// 分页。
	page, size := filter.Page, filter.PageSize
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 200 {
		size = 50
	}
	total := len(entries)
	start := (page - 1) * size
	if start > total {
		return []AuditEntry{}, total, nil
	}
	end := start + size
	if end > total {
		end = total
	}
	return entries[start:end], total, nil
}

// match 单条日志是否命中过滤条件。
func (a *AuditStore) match(e *AuditEntry, f AuditFilter) bool {
	if f.Action != "" && !strings.HasPrefix(e.Action, f.Action) {
		return false
	}
	if f.UserID > 0 && e.UserID != f.UserID {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// WithAudit 中间件
// ---------------------------------------------------------------------------

// statusRecorder 捕获 ResponseWriter 状态码（默认 200）。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// WithAudit 审计中间件：包装 /v1/admin/* 全部路由，记录写操作。
// GET / HEAD（只读）直接放行不记录；OPTIONS 预检也放行。
// 目标域解析与各资源模块共用 agentScopeFor（super_admin 取 query.agent_id
// 缺省默认域；agent_admin/admin 锁定自身归属）。
func (s *Service) WithAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			next.ServeHTTP(w, r)
			return
		}

		target, _ := agentScopeFor(r, r.URL.Query().Get("agent_id"))
		if target == "" {
			target = defaultAgentID
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		e := AuditEntry{
			TS:          time.Now().UTC(),
			UserID:      userIDOf(r.Context()),
			Role:        identity.Role(r.Context()),
			ActorAgent:  identity.AgentID(r.Context()),
			TargetAgent: target,
			Action:      actionFor(r.Method, r.URL.Path),
			Method:      r.Method,
			Path:        r.URL.Path,
			Status:      rec.status,
			RequestID:   apperr.RequestIDFromContext(r.Context()),
			LatencyMS:   time.Since(start).Milliseconds(),
		}
		if err := s.audit.Append(target, e); err != nil && s.log != nil {
			s.log.Warn("审计日志写入失败",
				zap.String("agent_id", target), zap.String("action", e.Action), zap.Error(err))
		}
	})
}

// actionFor 由请求方法与路径推导动作名：模块.动词[.子路径]。
//
//	POST   /v1/admin/skills            → skills.create
//	PUT    /v1/admin/skills/{name}     → skills.update
//	DELETE /v1/admin/skills/{name}     → skills.delete
//	POST   /v1/admin/kb/{id}/documents → kb.create.documents
func actionFor(method, path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	module := ""
	for i, p := range parts {
		if p == "v1" && i+1 < len(parts) && parts[i+1] == "admin" && i+2 < len(parts) {
			module = parts[i+2]
			break
		}
	}
	if module == "" {
		return strings.ToLower(method)
	}
	verb := mapMethodVerb(method)
	sub := ""
	if rest := strings.TrimPrefix(path, "/v1/admin/"+module); rest != "" {
		sub = "." + strings.Trim(strings.Trim(rest, "/"), "/") // 子路径原样拼接（含 {name} 段）
	}
	return module + "." + verb + sub
}

// mapMethodVerb HTTP 方法 → 动作动词。
func mapMethodVerb(method string) string {
	switch method {
	case http.MethodPost:
		return "create"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return strings.ToLower(method)
	}
}

// userIDOf 从 identity context 提取操作者 ID（游客/未注入时为 0）。
func userIDOf(ctx context.Context) int64 {
	id, _ := identity.UserID(ctx)
	return id
}
