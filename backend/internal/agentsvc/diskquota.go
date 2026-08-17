// diskquota.go —— 用户工作区磁盘配额（模块三·保护区配额管理）。
//
// 语义（与 llm-gateway 的 token 配额同构，表落在 agent 库——file_ops 校验侧）：
//   - sandbox_disk_quota 表有记录 = 显式覆盖（disk_quota_mb=0 表示不限）；
//   - 无记录 = 走角色默认（AGENT_DISK_QUOTA_MB_* 环境变量，super_admin 默认不限）；
//   - 优先级：单用户显式覆盖 > 角色默认。
//
// 组成：
//  1. DiskQuotaStore：表读写（pgx），接口便于单测注入内存 fake；
//  2. DiskQuotaEnforcer：file_ops 写 protected/ 前的配额校验（懒统计目录大小，
//     软上限拦截——只限制"保护区"这个永不清的空间，临时区靠清理器 TTL 管）；
//  3. DiskQuotaAdmin：管理端 HTTP 端点（X-Admin-Token，gateway 经 adminsvc 代理）。
package agentsvc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// ---------------------------------------------------------------------------
// DiskQuotaStore（表读写）
// ---------------------------------------------------------------------------

// DiskQuota 单用户磁盘配额视图（管理端点 JSON 契约）。
type DiskQuota struct {
	UserID      int64     `json:"user_id"`
	DiskQuotaMB int64     `json:"disk_quota_mb"` // 0 = 不限
	UpdatedBy   int64     `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DiskQuotaStore 用户磁盘配额覆盖存储（sandbox_disk_quota 表）。
// 语义：有记录 = 显式覆盖（0 = 不限）；无记录 = 走角色默认。
type DiskQuotaStore interface {
	// Get 返回用户显式配额；ok=false 表示无覆盖记录（走角色默认）。
	Get(ctx context.Context, userID int64) (quotaMB int64, ok bool, err error)
	// Set 设置/更新用户显式配额（0 = 不限）。updatedBy 为操作人 user_id。
	Set(ctx context.Context, userID, quotaMB, updatedBy int64) error
	// Clear 删除用户显式配额（恢复角色默认）。
	Clear(ctx context.Context, userID int64) error
	// List 返回全部显式配额记录（管理端点用）。
	List(ctx context.Context) ([]DiskQuota, error)
}

// postgresDiskQuotaStore 基于 pgxpool 的 PostgreSQL 实现。
type postgresDiskQuotaStore struct {
	pool *pgxpool.Pool
}

// NewDiskQuotaStore 创建 PostgreSQL 用户磁盘配额存储。
func NewDiskQuotaStore(pool *pgxpool.Pool) DiskQuotaStore {
	return &postgresDiskQuotaStore{pool: pool}
}

const (
	sqlDiskQuotaGet = `SELECT disk_quota_mb FROM sandbox_disk_quota WHERE user_id = $1`

	// UPSERT：存在即更新（updated_at 刷新），不存在则插入。
	sqlDiskQuotaUpsert = `INSERT INTO sandbox_disk_quota (user_id, disk_quota_mb, updated_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
			disk_quota_mb = EXCLUDED.disk_quota_mb,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`

	sqlDiskQuotaDelete = `DELETE FROM sandbox_disk_quota WHERE user_id = $1`

	sqlDiskQuotaList = `SELECT user_id, disk_quota_mb, updated_by, updated_at
		FROM sandbox_disk_quota ORDER BY user_id`
)

func (s *postgresDiskQuotaStore) Get(ctx context.Context, userID int64) (int64, bool, error) {
	var quotaMB int64
	err := s.pool.QueryRow(ctx, sqlDiskQuotaGet, userID).Scan(&quotaMB)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return quotaMB, true, nil
}

func (s *postgresDiskQuotaStore) Set(ctx context.Context, userID, quotaMB, updatedBy int64) error {
	_, err := s.pool.Exec(ctx, sqlDiskQuotaUpsert, userID, quotaMB, updatedBy)
	return err
}

func (s *postgresDiskQuotaStore) Clear(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, sqlDiskQuotaDelete, userID)
	return err
}

func (s *postgresDiskQuotaStore) List(ctx context.Context) ([]DiskQuota, error) {
	rows, err := s.pool.Query(ctx, sqlDiskQuotaList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DiskQuota, 0)
	for rows.Next() {
		var q DiskQuota
		if err := rows.Scan(&q.UserID, &q.DiskQuotaMB, &q.UpdatedBy, &q.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// DiskQuotaEnforcer（file_ops 写 protected/ 前置校验）
// ---------------------------------------------------------------------------

// RoleDiskQuota 四角色默认磁盘配额（MB，0 = 不限）。
// 与 authsvc.Role 角色体系一一对应，缺省见 RoleDiskQuotaDefaults。
type RoleDiskQuota struct {
	User       int64 // user       普通用户
	Admin      int64 // admin      普通管理员
	AgentAdmin int64 // agent_admin 智能体超管
	SuperAdmin int64 // super_admin 最高超管（默认 0 = 不限）
}

// RoleDiskQuotaDefaults 内置默认配额（MB）：用户 256MB / 管理员 512MB /
// 智能体超管 1GB / 最高超管不限。部署方可用 AGENT_DISK_QUOTA_MB_* 覆盖。
var RoleDiskQuotaDefaults = RoleDiskQuota{
	User:       256,
	Admin:      512,
	AgentAdmin: 1024,
	SuperAdmin: 0,
}

// DiskQuotaEnforcer 保护区磁盘配额执行器。
// 由 cmd/agent 装配（store + 角色默认 + 日志），注入 file_ops 工具。
type DiskQuotaEnforcer struct {
	store    DiskQuotaStore // nil = 仅角色默认（测试/本地降级）
	defaults RoleDiskQuota
	log      *zap.Logger
}

// NewDiskQuotaEnforcer 创建配额执行器。store 可为 nil（无显式覆盖时仅角色默认）。
func NewDiskQuotaEnforcer(store DiskQuotaStore, defaults RoleDiskQuota, log *zap.Logger) *DiskQuotaEnforcer {
	return &DiskQuotaEnforcer{store: store, defaults: defaults, log: log}
}

// Check 校验"向保护区写入 writeBytes 字节"是否超配额。
// userID 为写入者，role 为其角色（空 = 普通用户）。超配额返回明确错误拒绝写入；
// 配额为 0（不限）/ 统计失败降级 / store 查询失败降级时放行并记日志（不阻断主链路）。
func (e *DiskQuotaEnforcer) Check(ctx context.Context, userID int64, protectedDir string, writeBytes int64, role string) error {
	quotaMB := e.effectiveQuotaMB(ctx, userID, role)
	if quotaMB <= 0 {
		return nil // 不限
	}
	quotaBytes := quotaMB << 20
	used, err := dirSizeBytes(protectedDir)
	if err != nil {
		e.log.Error("统计保护区大小失败，保守放行（配额校验降级）", zap.Error(err),
			zap.Int64("user_id", userID), zap.String("dir", protectedDir))
		return nil
	}
	if used+writeBytes > quotaBytes {
		e.log.Warn("保护区磁盘配额已满，拒绝写入", zap.Int64("user_id", userID),
			zap.Int64("quota_mb", quotaMB), zap.Int64("used", used), zap.Int64("write", writeBytes))
		return fmt.Errorf("file_ops: 保护区磁盘配额已满（上限 %d MB，当前已用 %s，本次写入 %s）。请删除保护区部分内容，或请管理员在管理端提高该用户配额",
			quotaMB, humanBytes(used), humanBytes(writeBytes))
	}
	return nil
}

// effectiveQuotaMB 计算用户有效配额（MB，0 = 不限）。
// 优先级：显式覆盖（表）> 角色默认。查询失败降级角色默认，仅记日志。
func (e *DiskQuotaEnforcer) effectiveQuotaMB(ctx context.Context, userID int64, role string) int64 {
	if e.store != nil {
		if quota, ok, err := e.store.Get(ctx, userID); err != nil {
			e.log.Error("查询磁盘配额覆盖失败，降级角色默认", zap.Error(err), zap.Int64("user_id", userID))
		} else if ok {
			return quota
		}
	}
	return e.defaultForRole(role)
}

// defaultForRole 按角色返回默认配额；未知/空角色按普通用户处理。
func (e *DiskQuotaEnforcer) defaultForRole(role string) int64 {
	switch role {
	case "super_admin":
		return e.defaults.SuperAdmin
	case "agent_admin":
		return e.defaults.AgentAdmin
	case "admin":
		return e.defaults.Admin
	default:
		return e.defaults.User
	}
}

// dirSizeBytes 懒统计目录占用（含嵌套子目录，仅文件大小累加）。
// 目录不存在视为 0。
func dirSizeBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可读条目，不中断统计
		}
		if d.IsDir() {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			total += fi.Size()
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

// humanBytes 人类可读大小（B/KB/MB/GB…，保留 1 位小数）。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---------------------------------------------------------------------------
// DiskQuotaAdmin（管理端 HTTP 端点）
// ---------------------------------------------------------------------------

// DiskQuotaAdmin 管理端用户磁盘配额处理器（agent HTTP 服务侧）。
// token 复用 LLM_ADMIN_TOKEN（与 llm-gateway/gateway 管理端点共享同一令牌）。
type DiskQuotaAdmin struct {
	store DiskQuotaStore
	token string // LLM_ADMIN_TOKEN；空 = 管理端点禁用
	log   *zap.Logger
}

// NewDiskQuotaAdmin 创建管理端磁盘配额处理器。
func NewDiskQuotaAdmin(store DiskQuotaStore, token string, log *zap.Logger) *DiskQuotaAdmin {
	return &DiskQuotaAdmin{store: store, token: token, log: log}
}

// RegisterAdmin 注册管理端点（要求 X-Admin-Token）。
func (a *DiskQuotaAdmin) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/disk-quota", a.requireToken(a.handleList))
	mux.HandleFunc("PUT /v1/admin/disk-quota/{user_id}", a.requireToken(a.handlePut))
	mux.HandleFunc("DELETE /v1/admin/disk-quota/{user_id}", a.requireToken(a.handleDelete))
}

// requireToken 管理端点令牌中间件：令牌未配置 → 503；不匹配 → 401。
func (a *DiskQuotaAdmin) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			dqWriteError(w, apperr.New(apperr.CodeUnavailable,
				"磁盘配额管理端点未启用：请为 agent-service 设置 LLM_ADMIN_TOKEN"))
			return
		}
		got := r.Header.Get("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
			dqWriteError(w, apperr.New(apperr.CodeUnauthenticated, "管理令牌无效"))
			return
		}
		next(w, r)
	}
}

// dqQuotaInput 设置配额的接入参数（对外 JSON 契约）。
type dqQuotaInput struct {
	DiskQuotaMB int64 `json:"disk_quota_mb"` // 0 = 不限
}

// handleList 返回全部显式配额记录。
func (a *DiskQuotaAdmin) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.List(r.Context())
	if err != nil {
		a.log.Error("disk quota list failed", zap.Error(err))
		dqWriteError(w, apperr.Wrap(apperr.CodeInternal, "查询磁盘配额列表失败", err))
		return
	}
	if list == nil {
		list = []DiskQuota{}
	}
	dqWriteJSON(w, http.StatusOK, map[string]any{"quotas": list})
}

// handlePut 设置/更新用户显式配额（disk_quota_mb=0 表示不限）。
func (a *DiskQuotaAdmin) handlePut(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		dqWriteError(w, apperr.New(apperr.CodeInvalidArgument, "user_id 须为正整数"))
		return
	}
	var in dqQuotaInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		dqWriteError(w, apperr.New(apperr.CodeInvalidArgument, "请求体不是合法的 JSON"))
		return
	}
	if in.DiskQuotaMB < 0 {
		dqWriteError(w, apperr.New(apperr.CodeInvalidArgument, "disk_quota_mb 不能为负数（0 = 不限）"))
		return
	}
	// 操作人：管理令牌不经用户体系，写 updated_by=0（网关代理调用时无法
	// 区分到具体管理员；如需审计可在网关层注入操作人，属后续增强）。
	if err := a.store.Set(r.Context(), userID, in.DiskQuotaMB, 0); err != nil {
		a.log.Error("disk quota set failed", zap.Error(err), zap.Int64("user_id", userID))
		dqWriteError(w, apperr.Wrap(apperr.CodeInternal, "设置磁盘配额失败", err))
		return
	}
	dqWriteJSON(w, http.StatusOK, map[string]any{
		"user_id":        userID,
		"disk_quota_mb": in.DiskQuotaMB,
	})
}

// handleDelete 删除用户显式配额（恢复角色默认）。
func (a *DiskQuotaAdmin) handleDelete(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		dqWriteError(w, apperr.New(apperr.CodeInvalidArgument, "user_id 须为正整数"))
		return
	}
	if err := a.store.Clear(r.Context(), userID); err != nil {
		a.log.Error("disk quota clear failed", zap.Error(err), zap.Int64("user_id", userID))
		dqWriteError(w, apperr.Wrap(apperr.CodeInternal, "删除磁盘配额覆盖失败", err))
		return
	}
	dqWriteJSON(w, http.StatusOK, map[string]any{"user_id": userID})
}

// dqWriteJSON 写出 JSON 响应。
func dqWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// dqWriteError 写出统一错误体 {code, message, request_id}。
func dqWriteError(w http.ResponseWriter, err error) {
	status, body := apperr.HTTPBody(err)
	if body["request_id"] == "" {
		body["request_id"] = apperr.RequestIDFromContext(context.Background())
	}
	dqWriteJSON(w, status, body)
}
