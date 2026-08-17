// 模型注册表持久化：PostgreSQL models 表（llm 库）。
//
// 全部写操作走事务保证一致性；默认模型的切换/转移在同一事务内完成，
// 配合唯一部分索引 models_single_default_idx 兜底（至多一个 is_default=true）。
package llmsvc

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrModelNotFound 目标模型不存在。
var ErrModelNotFound = errors.New("模型不存在")

// ErrModelExists 同名模型已存在。
var ErrModelExists = errors.New("同名模型已存在")

// ErrDefaultModel 默认模型受保护：不可删除也不可禁用。
// 默认位 = 唯一且始终可用的兜底实例，只能被"把另一模型设为默认"转移。
var ErrDefaultModel = errors.New("默认模型受保护：不可删除或禁用，请先将另一模型设为默认")

// ErrClearDefault 拒绝取消唯一默认模型的默认位（先用另一模型接管默认再操作）。
var ErrClearDefault = errors.New("不能取消当前默认模型的默认位，请先将另一模型设为默认")

// ModelStore 模型注册表存储接口（便于 handler 单测注入 fake）。
type ModelStore interface {
	// ListModels 返回全部模型（按创建时间升序）。
	ListModels(ctx context.Context) ([]ModelSpec, error)
	// CreateModel 创建模型。同名已存在 → ErrModelExists；is_default=true 时
	// 在同一事务内先取消现有默认（唯一默认转移）。表空时强制首个模型为默认
	// （保证始终有且仅有一个默认实例，与"默认不可删除"共同维护不变式）。
	CreateModel(ctx context.Context, spec ModelSpec) error
	// UpdateModel 更新模型接入参数（不改变 is_default；改名/删默认见专门方法）。
	// 模型不存在 → ErrModelNotFound。
	UpdateModel(ctx context.Context, name string, spec ModelSpec) error
	// DeleteModel 删除模型。默认模型不可删除 → ErrDefaultModel；
	// 非默认模型可直接删除（默认位不受影响，无需转移）。
	DeleteModel(ctx context.Context, name string) error
	// SetDefault 把指定模型设为默认（同一事务内取消其它默认，并把该模型
	// 强制启用——默认位 = 始终可用）。模型不存在 → ErrModelNotFound。
	SetDefault(ctx context.Context, name string) error
	// SetEnabled 启用/禁用模型。禁用默认模型 → ErrDefaultModel。
	// 模型不存在 → ErrModelNotFound。
	SetEnabled(ctx context.Context, name string, enabled bool) error
}

// postgresModelStore 基于 pgxpool 的 PostgreSQL 实现。
type postgresModelStore struct {
	pool *pgxpool.Pool
}

// NewModelStore 创建模型注册表存储。
func NewModelStore(pool *pgxpool.Pool) ModelStore {
	return &postgresModelStore{pool: pool}
}

const (
	colModelList = `name, provider_name, base_url, api_key, upstream_model,
		timeout_sec, max_retries, prompt_price_per_1m, completion_price_per_1m,
		is_default, enabled, no_thinking, max_tokens, created_at, updated_at`

	sqlModelList = `SELECT ` + colModelList + ` FROM models ORDER BY created_at, name`

	sqlModelInsert = `INSERT INTO models
		(name, provider_name, base_url, api_key, upstream_model,
		 timeout_sec, max_retries, prompt_price_per_1m, completion_price_per_1m,
		 is_default, enabled, no_thinking, max_tokens, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now(), now())`

	sqlModelUpdate = `UPDATE models SET
		provider_name = $2, base_url = $3, api_key = $4, upstream_model = $5,
		timeout_sec = $6, max_retries = $7,
		prompt_price_per_1m = $8, completion_price_per_1m = $9,
		no_thinking = $10, max_tokens = $11,
		updated_at = now()
		WHERE name = $1`

	sqlModelDelete = `DELETE FROM models WHERE name = $1`

	sqlClearDefault = `UPDATE models SET is_default = false WHERE is_default = true`

	sqlSetDefault = `UPDATE models SET is_default = true, enabled = true, updated_at = now() WHERE name = $1`

	sqlModelSetEnabled = `UPDATE models SET enabled = $2, updated_at = now() WHERE name = $1`
)

func (s *postgresModelStore) ListModels(ctx context.Context) ([]ModelSpec, error) {
	rows, err := s.pool.Query(ctx, sqlModelList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ModelSpec, 0, 8)
	for rows.Next() {
		var sp ModelSpec
		if err := rows.Scan(&sp.Name, &sp.ProviderName, &sp.BaseURL, &sp.APIKey,
			&sp.UpstreamModel, &sp.TimeoutSec, &sp.MaxRetries,
			&sp.PromptPricePer1M, &sp.CompletionPricePer1M,
			&sp.IsDefault, &sp.Enabled, &sp.NoThinking, &sp.MaxTokens,
			&sp.CreatedAt, &sp.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

func (s *postgresModelStore) CreateModel(ctx context.Context, spec ModelSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 表空 → 强制首个模型为默认（默认位唯一且始终存在的兜底实例）。
	if !spec.IsDefault {
		var cnt int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM models`).Scan(&cnt); err != nil {
			return err
		}
		if cnt == 0 {
			spec.IsDefault = true
		}
	}
	if spec.IsDefault {
		if _, err := tx.Exec(ctx, sqlClearDefault); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, sqlModelInsert,
		spec.Name, spec.ProviderName, spec.BaseURL, spec.APIKey, spec.UpstreamModel,
		spec.TimeoutSec, spec.MaxRetries, spec.PromptPricePer1M, spec.CompletionPricePer1M,
		spec.IsDefault, spec.Enabled, spec.NoThinking, spec.MaxTokens,
	); err != nil {
		if isUniqueViolation(err) {
			return ErrModelExists
		}
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresModelStore) UpdateModel(ctx context.Context, name string, spec ModelSpec) error {
	ct, err := s.pool.Exec(ctx, sqlModelUpdate,
		name, spec.ProviderName, spec.BaseURL, spec.APIKey, spec.UpstreamModel,
		spec.TimeoutSec, spec.MaxRetries, spec.PromptPricePer1M, spec.CompletionPricePer1M,
		spec.NoThinking, spec.MaxTokens,
	)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrModelNotFound
	}
	return nil
}

func (s *postgresModelStore) DeleteModel(ctx context.Context, name string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 目标是否仍存在（提前报 NotFound），并记录删除前默认位
	//（须先于删除查询，删除后该行已不存在）。
	var exists, wasDefault bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM models WHERE name = $1), COALESCE(
			(SELECT is_default FROM models WHERE name = $1), false)`, name,
	).Scan(&exists, &wasDefault); err != nil {
		return err
	}
	if !exists {
		return ErrModelNotFound
	}
	// 默认模型受保护（唯一兜底实例）：不可删除，只能先转移默认位。
	if wasDefault {
		return ErrDefaultModel
	}
	if _, err := tx.Exec(ctx, sqlModelDelete, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresModelStore) SetDefault(ctx context.Context, name string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 目标存在性校验（在取消默认之前，否则事务回滚会白改）。
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM models WHERE name = $1)`, name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrModelNotFound
	}
	if _, err := tx.Exec(ctx, sqlClearDefault); err != nil {
		return err
	}
	// 新默认强制启用：默认位 = 始终可用（禁用态的模型成为默认后自动启用）。
	if _, err := tx.Exec(ctx, sqlSetDefault, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresModelStore) SetEnabled(ctx context.Context, name string, enabled bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists, isDefault bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM models WHERE name = $1), COALESCE(
			(SELECT is_default FROM models WHERE name = $1), false)`, name,
	).Scan(&exists, &isDefault); err != nil {
		return err
	}
	if !exists {
		return ErrModelNotFound
	}
	// 默认模型不可禁用（默认位 = 始终可用）。
	if !enabled && isDefault {
		return ErrDefaultModel
	}
	if _, err := tx.Exec(ctx, sqlModelSetEnabled, name, enabled); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// isUniqueViolation 判断是否 PG 唯一键冲突（SQLSTATE 23505）。
// 注意：pgconn.PgError 的 Code 是字段而非方法，不能断言为 interface{ Code() string }，
// 直接匹配错误文本最稳妥（与 rag/store.go 同款实现）。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}
