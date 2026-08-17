package llmsvc

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QuotaStore 用户配额覆盖存储（user_quota 表）。
// 语义：user_quota 有记录 = 显式覆盖（0 = 不限）；无记录 = 走角色默认。
// 定义为接口便于 handler 单测注入内存 fake（不依赖真实 DB）。
type QuotaStore interface {
	// Get 返回用户显式配额；ok=false 表示无覆盖记录（走角色默认）。
	Get(ctx context.Context, userID int64) (quota int64, ok bool, err error)
	// Set 设置/更新用户显式配额（0 = 不限）。updatedBy 为操作人 user_id。
	Set(ctx context.Context, userID, quota, updatedBy int64) error
	// Clear 删除用户显式配额（恢复角色默认）。
	Clear(ctx context.Context, userID int64) error
	// List 返回全部显式配额记录（含本月用量，管理端点用）。
	List(ctx context.Context) ([]UserQuota, error)
}

// UserQuota 单用户配额视图（管理端点 JSON 契约）。
type UserQuota struct {
	UserID          int64     `json:"user_id"`
	TokenQuotaMonth int64     `json:"token_quota_month"` // 0 = 不限
	UpdatedBy       int64     `json:"updated_by"`
	UpdatedAt       time.Time `json:"updated_at"`
	UsedThisMonth   int64     `json:"used_this_month"` // 本月累计（usage_logs 实时）
}

// postgresQuotaStore 基于 pgxpool 的 PostgreSQL 实现。
type postgresQuotaStore struct {
	pool *pgxpool.Pool
}

// NewQuotaStore 创建 PostgreSQL 用户配额存储。
func NewQuotaStore(pool *pgxpool.Pool) QuotaStore {
	return &postgresQuotaStore{pool: pool}
}

const (
	sqlQuotaGet = `SELECT token_quota_month FROM user_quota WHERE user_id = $1`

	// UPSERT：存在即更新（updated_at 刷新），不存在则插入。
	sqlQuotaUpsert = `INSERT INTO user_quota (user_id, token_quota_month, updated_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
			token_quota_month = EXCLUDED.token_quota_month,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`

	sqlQuotaDelete = `DELETE FROM user_quota WHERE user_id = $1`

	// 列表 LEFT JOIN 本月用量（与配额判断同一口径：date_trunc('month')）。
	sqlQuotaList = `SELECT q.user_id, q.token_quota_month, q.updated_by, q.updated_at,
			COALESCE(u.used, 0) AS used
		FROM user_quota q
		LEFT JOIN (
			SELECT user_id, SUM(total_tokens) AS used
			FROM usage_logs
			WHERE created_at >= date_trunc('month', now())
			GROUP BY user_id
		) u ON u.user_id = q.user_id
		ORDER BY q.user_id`
)

func (s *postgresQuotaStore) Get(ctx context.Context, userID int64) (int64, bool, error) {
	var quota int64
	err := s.pool.QueryRow(ctx, sqlQuotaGet, userID).Scan(&quota)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return quota, true, nil
}

func (s *postgresQuotaStore) Set(ctx context.Context, userID, quota, updatedBy int64) error {
	_, err := s.pool.Exec(ctx, sqlQuotaUpsert, userID, quota, updatedBy)
	return err
}

func (s *postgresQuotaStore) Clear(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, sqlQuotaDelete, userID)
	return err
}

func (s *postgresQuotaStore) List(ctx context.Context) ([]UserQuota, error) {
	rows, err := s.pool.Query(ctx, sqlQuotaList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]UserQuota, 0)
	for rows.Next() {
		var q UserQuota
		if err := rows.Scan(&q.UserID, &q.TokenQuotaMonth, &q.UpdatedBy, &q.UpdatedAt, &q.UsedThisMonth); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
