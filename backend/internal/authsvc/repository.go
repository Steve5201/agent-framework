package authsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository 用户与刷新令牌的持久化接口。
// 定义为接口便于 service 层测试注入 fake 实现（P2-27 单测不依赖真实 DB）。
type Repository interface {
	// CreateUser 创建用户；username 冲突返回 CodeAlreadyExists。
	CreateUser(ctx context.Context, username, passwordHash string, role Role, tags []Tag) (*User, error)
	// GetUserByUsername 按用户名查用户；不存在返回 CodeNotFound。
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	// GetUserByID 按 ID 查用户；不存在返回 CodeNotFound。
	GetUserByID(ctx context.Context, id string) (*User, error)
	// AddUserTag 为用户追加一条标签（已存在同 key 时覆盖）。
	AddUserTag(ctx context.Context, id string, tag Tag) error
	// RemoveUserTag 移除指定 key 的用户标签（不存在时静默成功）。
	RemoveUserTag(ctx context.Context, id string, key string) error
	// UpdateUserRole 更新用户角色（阶段3：创建智能体时授予其超管 agent_admin）。
	UpdateUserRole(ctx context.Context, userID int64, role Role) error
	// UpdateUserPassword 重置用户密码（哈希后写入）；用户不存在返回 CodeNotFound。
	UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error
	// DeleteUser 删除用户（refresh_tokens 经 FK ON DELETE CASCADE 级联清理）；
	// 用户不存在返回 CodeNotFound。
	DeleteUser(ctx context.Context, userID int64) error
	// CountRole 统计指定角色的用户数（防"删除最后一名最高超管"锁死系统）。
	CountRole(ctx context.Context, role Role) (int64, error)
	// ListUsers 分页查询用户（keyword 为空 = 全部；scope 非空 = 仅含该智能体标签的用户；
	// 按 id 倒序）。返回用户列表与总数。
	ListUsers(ctx context.Context, keyword, scope string, page, pageSize int) ([]*User, int, error)
	// ListUsersByIDs 按 ID 批量查询用户（数据管理模块 Top 用户用户名回填）。
	// scope 非空 = 仅返回含该智能体标签的用户（管辖范围语义同 ListUsers）；
	// 返回顺序与请求顺序一致，不存在的 ID 静默跳过。
	ListUsersByIDs(ctx context.Context, ids []int64, scope string) ([]*User, error)
	// GetFirstSuperAdmin 返回 id 最小的最高超管（播种默认智能体时作 owner）。
	GetFirstSuperAdmin(ctx context.Context) (*User, error)

	// CreateAgent 创建智能体；id 冲突返回 CodeAlreadyExists。返回完整记录（含时间戳）。
	CreateAgent(ctx context.Context, a *Agent) (*Agent, error)
	// GetAgent 按 ID 查智能体；不存在返回 CodeNotFound。
	GetAgent(ctx context.Context, id string) (*Agent, error)
	// ListAgents 列出全部智能体（含停用，按创建时间正序；可见性过滤在 service 层）。
	ListAgents(ctx context.Context) ([]*Agent, error)
	// UpdateAgent 更新智能体元数据（全量覆盖指定字段，保留未传字段）。
	UpdateAgent(ctx context.Context, a *Agent) (*Agent, error)
	// UpdateAgentOwner 绑定/解绑智能体 owner（ownerUserID<=0 = 解绑置 NULL）。
	UpdateAgentOwner(ctx context.Context, id string, ownerUserID int64) (*Agent, error)
	// ClearAgentsOwner 清空指定用户担任 owner 的智能体 owner（删除用户后防悬空）。
	ClearAgentsOwner(ctx context.Context, userID int64) error
	// SetAgentStatus 启停智能体；返回更新后的记录。
	SetAgentStatus(ctx context.Context, id string, status int) (*Agent, error)
	// DeleteAgent 软删除智能体（status=0）。
	DeleteAgent(ctx context.Context, id string) (*Agent, error)
	// EnsureDefaultAgent 幂等播种默认智能体 tutor（已存在则跳过）。
	EnsureDefaultAgent(ctx context.Context, ownerUserID int64) error

	// CreateRefreshToken 记录一枚新签发的 refresh token。
	CreateRefreshToken(ctx context.Context, userID int64, familyID, tokenHash string, expiresAt time.Time) error
	// GetRefreshTokenByHash 按哈希查令牌记录；无效（不存在）返回 CodeUnauthenticated。
	GetRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error)
	// RevokeRefreshToken 吊销单条令牌（族轮换：旧令牌标记已吊销）。
	RevokeRefreshToken(ctx context.Context, id int64) error
	// RevokeFamily 吊销指定族内所有有效令牌（Logout 整族下线）。
	RevokeFamily(ctx context.Context, familyID string) error
	// RevokeFamilyByUser 吊销指定用户全部有效令牌（重置密码后强制下线）。
	RevokeFamilyByUser(ctx context.Context, userID int64) error
}

// postgresRepo 基于 pgxpool 的 Repository 实现。
type postgresRepo struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository 创建 PostgreSQL 实现。
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return &postgresRepo{pool: pool}
}

// parseUserID 将领域层 string ID 转为 DB int64。
func parseUserID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, apperr.New(apperr.CodeInvalidArgument, "非法的用户 ID")
	}
	return n, nil
}

const (
	sqlCreateUser = `INSERT INTO users (username, password_hash, role, tags)
		VALUES ($1, $2, $3, $4::jsonb) RETURNING id, username, password_hash, role, status, tags, created_at`

	sqlGetUserByUsername = `SELECT id, username, password_hash, role, status, tags, created_at
		FROM users WHERE username = $1`

	sqlGetUserByID = `SELECT id, username, password_hash, role, status, tags, created_at
		FROM users WHERE id = $1`

	sqlAddUserTag = `UPDATE users SET tags = (
			SELECT COALESCE(
				jsonb_agg(t) FILTER (WHERE t IS NOT NULL),
				'[]'::jsonb
			) FROM (
				SELECT jsonb_build_object('key', x.key, 'value', x.value) AS t
				FROM jsonb_array_elements(
					CASE WHEN tags IS NULL THEN '[]'::jsonb ELSE tags END
				) AS elem,
				jsonb_to_record(elem) AS x(key text, value text)
				WHERE x.key <> $2
			) q
		) || jsonb_build_object('key', $2::text, 'value', $3::text)
		WHERE id = $1 RETURNING tags`

	sqlListUsers = `SELECT id, username, password_hash, role, status, tags, created_at
		FROM users
		WHERE ($1 = '' OR username ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR tags @> jsonb_build_array(jsonb_build_object('key','agent','value',$2)))
		ORDER BY id DESC
		LIMIT $3 OFFSET $4`

	sqlCountUsers = `SELECT count(*) FROM users
		WHERE ($1 = '' OR username ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR tags @> jsonb_build_array(jsonb_build_object('key','agent','value',$2)))`

	// 按 ID 批量查询：与 ListUsers 相同的管辖范围语义（scope 非空时仅本组用户）；
	// 顺序由应用层按请求 ID 重排（此处按 id 升序便于测试断言确定性）。
	sqlListUsersByIDs = `SELECT id, username, password_hash, role, status, tags, created_at
		FROM users
		WHERE id = ANY($1::bigint[])
		  AND ($2 = '' OR tags @> jsonb_build_array(jsonb_build_object('key','agent','value',$2)))
		ORDER BY id`

	sqlGetFirstSuperAdmin = `SELECT id, username, password_hash, role, status, tags, created_at
		FROM users WHERE role = 'super_admin' ORDER BY id LIMIT 1`

	sqlUpdateUserRole = `UPDATE users SET role = $2, updated_at = now() WHERE id = $1`

	sqlUpdateUserPassword = `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`

	sqlDeleteUser = `DELETE FROM users WHERE id = $1`

	sqlRemoveUserTag = `UPDATE users SET tags = (
			SELECT COALESCE(
				jsonb_agg(t) FILTER (WHERE t IS NOT NULL),
				'[]'::jsonb
			) FROM (
				SELECT jsonb_build_object('key', x.key, 'value', x.value) AS t
				FROM jsonb_array_elements(
					CASE WHEN tags IS NULL THEN '[]'::jsonb ELSE tags END
				) AS elem,
				jsonb_to_record(elem) AS x(key text, value text)
				WHERE x.key <> $2
			) q
		) WHERE id = $1`

	sqlCountRole = `SELECT count(*) FROM users WHERE role = $1`

	// agentCols 智能体行通用列（INSERT RETURNING / SELECT / UPDATE RETURNING 复用）。
	agentCols = `id, name, description, model, owner_user_id, status, created_at, updated_at,
		avatar, welcome, system_prompt, reasoning_effort`

	sqlCreateAgent = `INSERT INTO agents (id, name, description, model, owner_user_id,
			avatar, welcome, system_prompt, reasoning_effort)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING ` + agentCols

	sqlGetAgent = `SELECT ` + agentCols + ` FROM agents WHERE id = $1`

	sqlListAgents = `SELECT ` + agentCols + ` FROM agents ORDER BY created_at`

	sqlUpdateAgent = `UPDATE agents SET name = $2, description = $3, model = $4,
			avatar = $5, welcome = $6, system_prompt = $7, reasoning_effort = $8,
			updated_at = now()
		WHERE id = $1 RETURNING ` + agentCols

	sqlSetAgentStatus = `UPDATE agents SET status = $2, updated_at = now()
		WHERE id = $1 RETURNING ` + agentCols

	sqlUpdateAgentOwner = `UPDATE agents SET owner_user_id = $2, updated_at = now()
		WHERE id = $1 RETURNING ` + agentCols

	sqlClearAgentsOwner = `UPDATE agents SET owner_user_id = NULL, updated_at = now()
		WHERE owner_user_id = $1`

	sqlDeleteAgent = `UPDATE agents SET status = 0, updated_at = now()
		WHERE id = $1 RETURNING ` + agentCols

	sqlEnsureDefaultAgent = `INSERT INTO agents (id, name, owner_user_id)
		VALUES ('tutor', '智能助手', $1) ON CONFLICT (id) DO NOTHING`

	sqlCreateRefreshToken = `INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)`

	sqlGetRefreshToken = `SELECT id, user_id, family_id, token_hash, expires_at, revoked_at
		FROM refresh_tokens WHERE token_hash = $1`

	sqlRevokeToken = `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`

	sqlRevokeFamily = `UPDATE refresh_tokens SET revoked_at = now()
		WHERE family_id = $1 AND revoked_at IS NULL`

	sqlRevokeByUser = `UPDATE refresh_tokens SET revoked_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL`
)

// errUserNotFound 统一的"用户不存在"错误（避免调用方收到模糊的 INTERNAL）。
var errUserNotFound = apperr.New(apperr.CodeNotFound, "用户不存在")

// rowToUser 将查询行映射为领域模型；无匹配行返回 pgx.ErrNoRows（由调用方翻译）。
func rowToUser(row pgx.Row) (*User, error) {
	var u User
	var id int64
	var role string
	var tags []byte
	if err := row.Scan(&id, &u.Username, &u.PasswordHash, &role, &u.Status, &tags, &u.CreatedAt); err != nil {
		return nil, err // 保持 pgx.ErrNoRows 等原始错误，交调用方判定
	}
	u.ID = strconv.FormatInt(id, 10)
	u.Role = Role(role)
	// tags 为空（NULL）按无标签处理。
	if len(tags) > 0 {
		_ = json.Unmarshal(tags, &u.Tags)
	}
	return &u, nil
}

// tagsJSON 序列化标签为 JSONB 写入值。
func tagsJSON(tags []Tag) ([]byte, error) {
	if len(tags) == 0 {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "序列化用户标签失败", err)
	}
	return b, nil
}

// translateUserErr 将 rowToUser 的错误翻译为统一错误：
// ErrNoRows → CodeNotFound；其余包装为 CodeInternal。
func translateUserErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errUserNotFound
	}
	return apperr.Wrap(apperr.CodeInternal, "查询用户失败", err)
}

func (r *postgresRepo) CreateUser(ctx context.Context, username, passwordHash string, role Role, tags []Tag) (*User, error) {
	tagsB, err := tagsJSON(tags)
	if err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, sqlCreateUser, username, passwordHash, string(role), tagsB)
	u, err := rowToUser(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, apperr.New(apperr.CodeAlreadyExists, "用户名已被注册")
		}
		return nil, apperr.Wrap(apperr.CodeInternal, "创建用户失败", err)
	}
	return u, nil
}

func (r *postgresRepo) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	u, err := rowToUser(r.pool.QueryRow(ctx, sqlGetUserByUsername, username))
	if err != nil {
		return nil, translateUserErr(err)
	}
	return u, nil
}

func (r *postgresRepo) GetUserByID(ctx context.Context, id string) (*User, error) {
	uid, err := parseUserID(id)
	if err != nil {
		return nil, err
	}
	u, err := rowToUser(r.pool.QueryRow(ctx, sqlGetUserByID, uid))
	if err != nil {
		return nil, translateUserErr(err)
	}
	return u, nil
}

// AddUserTag 追加/覆盖一条标签（SQL 内先剔除同 key 再合并，单语句保证原子）。
func (r *postgresRepo) AddUserTag(ctx context.Context, id string, tag Tag) error {
	uid, err := parseUserID(id)
	if err != nil {
		return err
	}
	var tags []byte
	if err := r.pool.QueryRow(ctx, sqlAddUserTag, uid, tag.Key, tag.Value).Scan(&tags); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "追加用户标签失败", err)
	}
	return nil
}

// RemoveUserTag 移除指定 key 的用户标签（不存在时静默成功）。
func (r *postgresRepo) RemoveUserTag(ctx context.Context, id string, key string) error {
	uid, err := parseUserID(id)
	if err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, sqlRemoveUserTag, uid, key); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "移除用户标签失败", err)
	}
	return nil
}

// UpdateUserRole 更新用户角色（授予/收回管理权）。
func (r *postgresRepo) UpdateUserRole(ctx context.Context, userID int64, role Role) error {
	if _, err := r.pool.Exec(ctx, sqlUpdateUserRole, userID, string(role)); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "更新用户角色失败", err)
	}
	return nil
}

// UpdateUserPassword 重置用户密码（哈希由 service 层计算；行数=0 视为不存在）。
func (r *postgresRepo) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	ct, err := r.pool.Exec(ctx, sqlUpdateUserPassword, userID, passwordHash)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "重置用户密码失败", err)
	}
	if ct.RowsAffected() == 0 {
		return errUserNotFound
	}
	return nil
}

// DeleteUser 删除用户。refresh_tokens 表 user_id 带 ON DELETE CASCADE，级联清理；
// agents.owner_user_id 仅索引无外键，删除用户后该智能体 owner 悬空由调用方保证
// （service 层禁止删除仍担任 owner 的最高超管场景由角色计数兜底）。
func (r *postgresRepo) DeleteUser(ctx context.Context, userID int64) error {
	ct, err := r.pool.Exec(ctx, sqlDeleteUser, userID)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "删除用户失败", err)
	}
	if ct.RowsAffected() == 0 {
		return errUserNotFound
	}
	return nil
}

// CountRole 统计指定角色用户数（service 层删除最高超管前校验）。
func (r *postgresRepo) CountRole(ctx context.Context, role Role) (int64, error) {
	var n int64
	if err := r.pool.QueryRow(ctx, sqlCountRole, string(role)).Scan(&n); err != nil {
		return 0, apperr.Wrap(apperr.CodeInternal, "统计用户失败", err)
	}
	return n, nil
}

// ListUsers 分页查询用户（keyword 模糊匹配用户名；scope 非空 = 仅含该智能体标签；page 从 1 起）。
func (r *postgresRepo) ListUsers(ctx context.Context, keyword, scope string, page, pageSize int) ([]*User, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var total int
	if err := r.pool.QueryRow(ctx, sqlCountUsers, keyword, scope).Scan(&total); err != nil {
		return nil, 0, apperr.Wrap(apperr.CodeInternal, "统计用户失败", err)
	}

	rows, err := r.pool.Query(ctx, sqlListUsers, keyword, scope, pageSize, offset)
	if err != nil {
		return nil, 0, apperr.Wrap(apperr.CodeInternal, "查询用户列表失败", err)
	}
	defer rows.Close()

	users := make([]*User, 0, pageSize)
	for rows.Next() {
		u, err := rowToUser(rows)
		if err != nil {
			return nil, 0, apperr.Wrap(apperr.CodeInternal, "解析用户行失败", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, apperr.Wrap(apperr.CodeInternal, "遍历用户列表失败", err)
	}
	return users, total, nil
}

// ListUsersByIDs 按 ID 批量查询用户（scope 非空 = 仅本智能体组用户）。
// 返回顺序与请求顺序一致（按内存 map 重排），不存在的 ID 静默跳过。
func (r *postgresRepo) ListUsersByIDs(ctx context.Context, ids []int64, scope string) ([]*User, error) {
	rows, err := r.pool.Query(ctx, sqlListUsersByIDs, ids, scope)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "查询用户失败", err)
	}
	defer rows.Close()

	byID := make(map[int64]*User, len(ids))
	for rows.Next() {
		u, err := rowToUser(rows)
		if err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "解析用户行失败", err)
		}
		byID[mustUserID(u.ID)] = u
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "遍历用户列表失败", err)
	}

	out := make([]*User, 0, len(ids))
	for _, id := range ids {
		if u, ok := byID[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

// GetFirstSuperAdmin 返回 id 最小的最高超管（用于播种默认智能体的 owner）。
func (r *postgresRepo) GetFirstSuperAdmin(ctx context.Context) (*User, error) {
	u, err := rowToUser(r.pool.QueryRow(ctx, sqlGetFirstSuperAdmin))
	if err != nil {
		return nil, translateUserErr(err)
	}
	return u, nil
}

// rowToAgent 将查询行映射为 Agent 领域模型；owner_user_id 可空（NULL=未绑定，转 0）。
func rowToAgent(row pgx.Row) (*Agent, error) {
	var a Agent
	var owner sql.NullInt64
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.Model,
		&owner, &a.Status, &a.CreatedAt, &a.UpdatedAt,
		&a.Avatar, &a.Welcome, &a.SystemPrompt, &a.ReasoningEffort); err != nil {
		return nil, err
	}
	if owner.Valid {
		a.OwnerUserID = owner.Int64
	}
	return &a, nil
}

// translateAgentErr 将 agent 查询错误翻译为统一错误。
func translateAgentErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.New(apperr.CodeNotFound, "智能体不存在")
	}
	return apperr.Wrap(apperr.CodeInternal, "查询智能体失败", err)
}

func (r *postgresRepo) CreateAgent(ctx context.Context, a *Agent) (*Agent, error) {
	row := r.pool.QueryRow(ctx, sqlCreateAgent, a.ID, a.Name, a.Description, a.Model, a.OwnerUserID,
		a.Avatar, a.Welcome, a.SystemPrompt, a.ReasoningEffort)
	created, err := rowToAgent(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, apperr.New(apperr.CodeAlreadyExists, "智能体 ID 已存在")
		}
		return nil, apperr.Wrap(apperr.CodeInternal, "创建智能体失败", err)
	}
	return created, nil
}

func (r *postgresRepo) GetAgent(ctx context.Context, id string) (*Agent, error) {
	a, err := rowToAgent(r.pool.QueryRow(ctx, sqlGetAgent, id))
	if err != nil {
		return nil, translateAgentErr(err)
	}
	return a, nil
}

func (r *postgresRepo) UpdateAgent(ctx context.Context, a *Agent) (*Agent, error) {
	updated, err := rowToAgent(r.pool.QueryRow(ctx, sqlUpdateAgent,
		a.ID, a.Name, a.Description, a.Model, a.Avatar, a.Welcome, a.SystemPrompt, a.ReasoningEffort))
	if err != nil {
		return nil, translateAgentErr(err)
	}
	return updated, nil
}

// UpdateAgentOwner 绑定/解绑智能体 owner（ownerUserID<=0 写 NULL）。
func (r *postgresRepo) UpdateAgentOwner(ctx context.Context, id string, ownerUserID int64) (*Agent, error) {
	var owner any // nil = NULL
	if ownerUserID > 0 {
		owner = ownerUserID
	}
	updated, err := rowToAgent(r.pool.QueryRow(ctx, sqlUpdateAgentOwner, id, owner))
	if err != nil {
		return nil, translateAgentErr(err)
	}
	return updated, nil
}

// ClearAgentsOwner 清空指定用户担任 owner 的智能体（删除用户后防悬空引用）。
func (r *postgresRepo) ClearAgentsOwner(ctx context.Context, userID int64) error {
	if _, err := r.pool.Exec(ctx, sqlClearAgentsOwner, userID); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "清空智能体 owner 失败", err)
	}
	return nil
}

func (r *postgresRepo) SetAgentStatus(ctx context.Context, id string, status int) (*Agent, error) {
	updated, err := rowToAgent(r.pool.QueryRow(ctx, sqlSetAgentStatus, id, status))
	if err != nil {
		return nil, translateAgentErr(err)
	}
	return updated, nil
}

func (r *postgresRepo) DeleteAgent(ctx context.Context, id string) (*Agent, error) {
	updated, err := rowToAgent(r.pool.QueryRow(ctx, sqlDeleteAgent, id))
	if err != nil {
		return nil, translateAgentErr(err)
	}
	return updated, nil
}

func (r *postgresRepo) ListAgents(ctx context.Context) ([]*Agent, error) {
	rows, err := r.pool.Query(ctx, sqlListAgents)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "查询智能体列表失败", err)
	}
	defer rows.Close()
	out := make([]*Agent, 0, 4)
	for rows.Next() {
		a, err := rowToAgent(rows)
		if err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "解析智能体行失败", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "遍历智能体列表失败", err)
	}
	return out, nil
}

func (r *postgresRepo) EnsureDefaultAgent(ctx context.Context, ownerUserID int64) error {
	if _, err := r.pool.Exec(ctx, sqlEnsureDefaultAgent, ownerUserID); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "播种默认智能体失败", err)
	}
	return nil
}

func (r *postgresRepo) CreateRefreshToken(ctx context.Context, userID int64, familyID, tokenHash string, expiresAt time.Time) error {
	if _, err := r.pool.Exec(ctx, sqlCreateRefreshToken, userID, familyID, tokenHash, expiresAt); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "记录 refresh token 失败", err)
	}
	return nil
}

func (r *postgresRepo) GetRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	var t RefreshToken
	if err := r.pool.QueryRow(ctx, sqlGetRefreshToken, tokenHash).
		Scan(&t.ID, &t.UserID, &t.FamilyID, &t.TokenHash, &t.ExpiresAt, &t.RevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.New(apperr.CodeUnauthenticated, "refresh token 无效")
		}
		return nil, apperr.Wrap(apperr.CodeInternal, "查询 refresh token 失败", err)
	}
	return &t, nil
}

func (r *postgresRepo) RevokeRefreshToken(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, sqlRevokeToken, id); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "吊销 refresh token 失败", err)
	}
	return nil
}

func (r *postgresRepo) RevokeFamily(ctx context.Context, familyID string) error {
	if _, err := r.pool.Exec(ctx, sqlRevokeFamily, familyID); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "吊销 refresh 族失败", err)
	}
	return nil
}

// RevokeFamilyByUser 吊销指定用户全部有效令牌（重置密码后强制下线，防旧会话续用）。
func (r *postgresRepo) RevokeFamilyByUser(ctx context.Context, userID int64) error {
	if _, err := r.pool.Exec(ctx, sqlRevokeByUser, userID); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "吊销用户令牌失败", err)
	}
	return nil
}
