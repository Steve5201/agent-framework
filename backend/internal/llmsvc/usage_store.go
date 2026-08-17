package llmsvc

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UsageStore 用量落库、配额查询与按智能体聚合接口。
// 定义为接口便于 handler 单测注入内存 fake（不依赖真实 DB）。
type UsageStore interface {
	// LogUsage 写入一条用量记录（成功/失败各一条）。
	LogUsage(ctx context.Context, log *UsageLog) error
	// MonthTotalTokens 查询用户本月累计消耗 token（配额判断用）。
	MonthTotalTokens(ctx context.Context, userID int64) (int64, error)
	// AgentTotals 按智能体域聚合时间窗口内的成功用量；无记录返回零值。
	AgentTotals(ctx context.Context, agentID string, days int) (*AgentUsage, error)
	// Overview 管理端用量总览（数据管理模块）：近 days 天（含当天）的
	// 摘要、按日完整序列（含 0 值）、按模型/智能体/用户聚合（成功调用）。
	Overview(ctx context.Context, days int) (*UsageOverview, error)
}

// ---------------------------------------------------------------------------
// 管理端用量总览领域模型（数据管理模块）
// ---------------------------------------------------------------------------

// UsageOverview 平台用量总览：摘要 + 按日序列 + 多维聚合。
type UsageOverview struct {
	Summary UsageSummary `json:"summary"`
	Daily   []DayUsage   `json:"daily"`    // 完整日序列（近 days 天含当天，升序）
	ByModel []UsageGroup `json:"by_model"` // 按模型（成功调用，成本降序，Top 20）
	ByAgent []UsageGroup `json:"by_agent"` // 按智能体域（成功调用，成本降序，Top 20）
	ByUser  []UserUsage  `json:"by_user"`  // 按用户（成功调用，调用数降序，Top 100）
}

// UsageSummary 总览摘要。
type UsageSummary struct {
	Calls       int64   `json:"calls"`
	Success     int64   `json:"success"`
	Failed      int64   `json:"failed"`
	DAU         int64   `json:"dau"` // 去重活跃用户数（成功调用）
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// DayUsage 单日用量（含调用/成功/失败/DAU/成本）。
type DayUsage struct {
	Date        string  `json:"date"`
	Calls       int64   `json:"calls"`
	Success     int64   `json:"success"`
	Failed      int64   `json:"failed"`
	DAU         int64   `json:"dau"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// UsageGroup 单维度用量聚合（模型 / 智能体域）。
type UsageGroup struct {
	Key         string  `json:"key"`
	Calls       int64   `json:"calls"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// UserUsage 单用户用量聚合（Top 用户展示，前端取前 10 并回填用户名）。
type UserUsage struct {
	UserID      int64   `json:"user_id"`
	Calls       int64   `json:"calls"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// postgresUsageStore 基于 pgxpool 的 PostgreSQL 实现（P2-33）。
type postgresUsageStore struct {
	pool *pgxpool.Pool
}

// NewUsageStore 创建 PostgreSQL 用量存储。
func NewUsageStore(pool *pgxpool.Pool) UsageStore {
	return &postgresUsageStore{pool: pool}
}

// status 状态列：0=成功 1=失败（迁移 000001 约定）。
const (
	usageStatusOK   = 0
	usageStatusFail = 1
)

const (
	sqlLogUsage = `INSERT INTO usage_logs
		(user_id, agent_id, request_id, model, prompt_tokens, completion_tokens,
		 total_tokens, cost_usd, stream, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	sqlMonthTotal = `SELECT COALESCE(SUM(total_tokens), 0)
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= date_trunc('month', now())`

	sqlAgentTotals = `SELECT COALESCE(COUNT(*), 0),
		COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0),
		COALESCE(MAX(created_at), 'epoch'::timestamptz)
		FROM usage_logs
		WHERE agent_id = $1 AND status = 0
			AND created_at >= now() - make_interval(days => $2)`

	// ---- 管理端用量总览（数据管理模块）----
	// 窗口统一按"近 $1 天（含当天）"的当日零点截断；按日序列用 generate_series
	// 在 SQL 内补全 0 值（日期按 DB 会话时区，标准部署下 DB 与服务同 TZ）。
	sqlUsageOverviewDaily = `WITH days AS (
			SELECT generate_series(
				date_trunc('day', now() - make_interval(days => $1::int - 1)),
				date_trunc('day', now()),
				'1 day'::interval
			)::date AS day
		)
		SELECT to_char(days.day, 'YYYY-MM-DD'),
			COALESCE(c.calls, 0), COALESCE(c.success, 0), COALESCE(c.failed, 0),
			COALESCE(c.dau, 0), COALESCE(c.tokens, 0), COALESCE(c.cost, 0)
		FROM days
		LEFT JOIN (
			SELECT date_trunc('day', created_at) AS day,
				COUNT(*) AS calls,
				COUNT(*) FILTER (WHERE status = 0) AS success,
				COUNT(*) FILTER (WHERE status = 1) AS failed,
				COUNT(DISTINCT user_id) FILTER (WHERE status = 0) AS dau,
				SUM(total_tokens) AS tokens, SUM(cost_usd) AS cost
			FROM usage_logs
			WHERE created_at >= date_trunc('day', now() - make_interval(days => $1::int - 1))
			GROUP BY 1
		) c ON c.day = days.day
		ORDER BY days.day`

	sqlUsageOverviewSummary = `SELECT COALESCE(COUNT(*), 0),
		COALESCE(COUNT(*) FILTER (WHERE status = 0), 0),
		COALESCE(COUNT(*) FILTER (WHERE status = 1), 0),
		COALESCE(COUNT(DISTINCT user_id) FILTER (WHERE status = 0), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
		FROM usage_logs
		WHERE created_at >= date_trunc('day', now() - make_interval(days => $1::int - 1))`

	sqlUsageOverviewByModel = `SELECT model, COUNT(*), SUM(total_tokens), SUM(cost_usd)
		FROM usage_logs
		WHERE status = 0 AND created_at >= date_trunc('day', now() - make_interval(days => $1::int - 1))
		GROUP BY model ORDER BY 4 DESC LIMIT 20`

	sqlUsageOverviewByAgent = `SELECT COALESCE(agent_id, ''), COUNT(*), SUM(total_tokens), SUM(cost_usd)
		FROM usage_logs
		WHERE status = 0 AND created_at >= date_trunc('day', now() - make_interval(days => $1::int - 1))
		GROUP BY agent_id ORDER BY 4 DESC LIMIT 20`

	sqlUsageOverviewByUser = `SELECT user_id, COUNT(*), SUM(total_tokens), SUM(cost_usd)
		FROM usage_logs
		WHERE status = 0 AND created_at >= date_trunc('day', now() - make_interval(days => $1::int - 1))
		GROUP BY user_id ORDER BY 2 DESC LIMIT 100`
)

func (s *postgresUsageStore) LogUsage(ctx context.Context, log *UsageLog) error {
	status := usageStatusOK
	if !log.Success {
		status = usageStatusFail
	}
	if _, err := s.pool.Exec(ctx, sqlLogUsage,
		log.UserID, log.AgentID, log.RequestID, log.Model,
		log.PromptTokens, log.CompletionTokens, log.TotalTokens,
		log.CostUSD, log.Stream, status,
	); err != nil {
		return err
	}
	return nil
}

func (s *postgresUsageStore) MonthTotalTokens(ctx context.Context, userID int64) (int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, sqlMonthTotal, userID).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *postgresUsageStore) AgentTotals(ctx context.Context, agentID string, days int) (*AgentUsage, error) {
	var u AgentUsage
	if err := s.pool.QueryRow(ctx, sqlAgentTotals, agentID, days).Scan(
		&u.Calls, &u.PromptTokens, &u.CompletionTokens,
		&u.TotalTokens, &u.CostUSD, &u.LastUsedAt,
	); err != nil {
		return nil, err
	}
	u.AgentID = agentID
	return &u, nil
}

// Overview 管理端用量总览（数据管理模块）：摘要 + 按日 + 按模型/智能体/用户聚合。
func (s *postgresUsageStore) Overview(ctx context.Context, days int) (*UsageOverview, error) {
	ov := &UsageOverview{}

	// 摘要
	if err := s.pool.QueryRow(ctx, sqlUsageOverviewSummary, days).Scan(
		&ov.Summary.Calls, &ov.Summary.Success, &ov.Summary.Failed,
		&ov.Summary.DAU, &ov.Summary.TotalTokens, &ov.Summary.CostUSD,
	); err != nil {
		return nil, err
	}

	// 按日完整序列（SQL 内补全 0 值）
	rows, err := s.pool.Query(ctx, sqlUsageOverviewDaily, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d DayUsage
		if err := rows.Scan(&d.Date, &d.Calls, &d.Success, &d.Failed,
			&d.DAU, &d.TotalTokens, &d.CostUSD); err != nil {
			return nil, err
		}
		ov.Daily = append(ov.Daily, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 按模型 / 按智能体域
	scanGroups := func(query string) ([]UsageGroup, error) {
		rows, err := s.pool.Query(ctx, query, days)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []UsageGroup
		for rows.Next() {
			var g UsageGroup
			if err := rows.Scan(&g.Key, &g.Calls, &g.TotalTokens, &g.CostUSD); err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return out, rows.Err()
	}
	if ov.ByModel, err = scanGroups(sqlUsageOverviewByModel); err != nil {
		return nil, err
	}
	if ov.ByAgent, err = scanGroups(sqlUsageOverviewByAgent); err != nil {
		return nil, err
	}

	// 按用户（Top 100，调用数降序）
	rows, err = s.pool.Query(ctx, sqlUsageOverviewByUser, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u UserUsage
		if err := rows.Scan(&u.UserID, &u.Calls, &u.TotalTokens, &u.CostUSD); err != nil {
			return nil, err
		}
		ov.ByUser = append(ov.ByUser, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ov, nil
}
