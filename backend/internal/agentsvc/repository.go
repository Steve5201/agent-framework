package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository 会话与消息的持久化接口。
// 定义为接口便于 service 单测注入内存 fake（不依赖真实 DB）。
type Repository interface {
	// CreateSession 创建会话并返回带 ID 的完整记录。
	// agentID 标识会话所属智能体域：'' = 管理端域；'<id>' = 对应智能体域。
	CreateSession(ctx context.Context, userID int64, agentID, title string) (*Session, error)
	// ListSessions 分页列出用户在某智能体域的有效会话（更新时间倒序）。
	// agentID 为 '' 时只列管理端域会话；为 "*" 时列出全部域（管理端总览用）。
	// 只返回"有过消息"的会话（空会话不展示，见 P2-K 空会话策略）。
	// page 从 1 起；返回 (会话列表, 总数)。
	ListSessions(ctx context.Context, userID int64, agentID string, page, pageSize int) ([]*Session, int64, error)
	// GetSession 按 ID 查会话（含已删除）；不存在返回 CodeNotFound。
	GetSession(ctx context.Context, sessionID int64) (*Session, error)
	// DeleteSession 软删会话（status=0），已删除则幂等成功。
	DeleteSession(ctx context.Context, sessionID int64) error
	// UpdateSessionTitle 重命名会话（仅有效会话）。
	UpdateSessionTitle(ctx context.Context, sessionID int64, title string) error
	// UpdateSessionConfig 更新会话配置（工具权限/思考模式，仅有效会话）。
	UpdateSessionConfig(ctx context.Context, sessionID int64, cfg SessionConfig) error

	// ListMessages 列出会话全部"可见"消息（hidden=false，seq 升序，含 id）。
	// 附带 round_no/version 与每轮版本总数 total_versions（切换 UI 用）。
	ListMessages(ctx context.Context, sessionID int64) ([]*Message, error)
	// ListMessagesUptoRound 列出"可见"且 round_no <= 目标轮的消息
	// （重生成/分支的上下文恢复用）。
	ListMessagesUptoRound(ctx context.Context, sessionID int64, uptoRound int64) ([]*Message, error)
	// AppendMessages 追加一批消息（事务内：seq 从 max+1 连续分配；
	// round_no/version 由调用方在 Message 上指定）。
	AppendMessages(ctx context.Context, sessionID int64, msgs []*Message) error
	// GetMessage 定位一条可见消息（返回其轮次与角色）。不存在返回 CodeNotFound。
	GetMessage(ctx context.Context, sessionID, messageID int64) (*Message, error)
	// DeleteRound 删除"一轮完整对话"（消息所在轮的全部消息，含其 user/assistant/tool）。
	// 目标消息不存在视为成功（幂等）；删除后会话若已无任何消息则自动软删会话。
	DeleteRound(ctx context.Context, sessionID, messageID int64) error
	// MaxRoundNo 返回会话最大轮次（无消息返回 0）。
	MaxRoundNo(ctx context.Context, sessionID int64) (int64, error)
	// MaxRoundVersion 返回指定轮的最大版本号（无则 0）。
	MaxRoundVersion(ctx context.Context, sessionID, roundNo int64) (int, error)
	// ActiveRoundVersion 返回指定轮当前活跃（可见）回答的版本号（无则 0）。
	ActiveRoundVersion(ctx context.Context, sessionID, roundNo int64) (int, error)
	// HideRoundAndAfter 隐藏指定轮的回答（assistant/tool，保留 user）与
	// 后续轮次的全部消息——重新生成时截断旧分支。
	HideRoundAndAfter(ctx context.Context, sessionID, roundNo int64) error
	// RestoreRoundAndAfter 恢复指定轮指定版本的回答与后续轮次全部消息
	// （重新生成失败时回滚截断）。
	RestoreRoundAndAfter(ctx context.Context, sessionID, roundNo int64, version int) error
	// SetActiveVersion 切换指定轮的活跃版本：显示该版本回答、隐藏其它版本，
	// 并隐藏（截断）后续轮次全部消息。
	SetActiveVersion(ctx context.Context, sessionID, roundNo int64, version int) error

	// InsertAuditToolCall 写入一条工具调用审计记录（阶段1·审计）。
	// 审计写失败不阻塞对话主流程：由调用方决定是否记日志降级。
	InsertAuditToolCall(ctx context.Context, a *AuditToolCall) error

	// SaveOrchestration 落库一次多智能体编排执行记录（过程输出入库，
	// 会话/管理端复盘用）。写失败不阻塞对话主流程（调用方记日志降级）。
	SaveOrchestration(ctx context.Context, run *OrchestrationRun) error

	// MergeGuestSessions 把游客命名空间（guestUserID < 0）下全部有效会话
	// 的属主转移给真实账号（targetUserID > 0），消息随会话自动归属（子表）。
	// 返回迁移的会话数。用于登录后"游客会话合并到账号"。
	MergeGuestSessions(ctx context.Context, guestUserID, targetUserID int64) (int, error)

	// SessionStats 管理端会话统计（数据管理模块）：近 days 天按日新建会话数
	//（完整日序列含 0 值）+ 按智能体域分布（倒序）+ 全量累计有效会话数。
	SessionStats(ctx context.Context, days int) (*SessionStats, error)
}

// postgresRepo 基于 pgxpool 的 Repository 实现。
type postgresRepo struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository 创建 PostgreSQL 实现。
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return &postgresRepo{pool: pool}
}

const (
	sqlCreateSession = `INSERT INTO sessions (user_id, agent_id, title)
		VALUES ($1, $2, $3)
		RETURNING id, user_id, agent_id, title, status, created_at, updated_at, COALESCE(config::text, '{}')`

	// 只返回"有过消息"的会话：空会话（创建后没对话、或消息被删光）不展示。
	// agent_id 过滤：$3='' → 管理端域；$3='*' → 全部域；否则精确匹配智能体域。
	// COALESCE(s.agent_id, '')：历史遗留的 NULL 域会话按"管理端域"归属，
	// 避免其在"全部域"下半可见（具体域过滤不掉）、造成"无主对话"假象。
	sqlListSessions = `SELECT s.id, s.user_id, s.agent_id, s.title, s.status, s.created_at, s.updated_at, COALESCE(s.config::text, '{}')
		FROM sessions s
		WHERE s.user_id = $1 AND s.status = $2
		  AND ($3 = '*' OR COALESCE(s.agent_id, '') = $3)
		  AND EXISTS (SELECT 1 FROM messages m WHERE m.session_id = s.id)
		ORDER BY s.updated_at DESC
		LIMIT $4 OFFSET $5`

	sqlCountSessions = `SELECT COUNT(*)
		FROM sessions s
		WHERE s.user_id = $1 AND s.status = $2
		  AND ($3 = '*' OR COALESCE(s.agent_id, '') = $3)
		  AND EXISTS (SELECT 1 FROM messages m WHERE m.session_id = s.id)`

	sqlGetSession = `SELECT id, user_id, agent_id, title, status, created_at, updated_at, COALESCE(config::text, '{}')
		FROM sessions WHERE id = $1`

	sqlMergeGuestSessions = `UPDATE sessions SET user_id = $2, updated_at = now()
		WHERE user_id = $1 AND status = 1`

	sqlDeleteSession = `UPDATE sessions SET status = 0, updated_at = now()
		WHERE id = $1 AND status = 1`

	sqlRenameSession = `UPDATE sessions SET title = $2, updated_at = now()
		WHERE id = $1 AND status = 1`

	sqlUpdateSessionConfig = `UPDATE sessions SET config = $2::jsonb, updated_at = now()
		WHERE id = $1 AND status = 1`

	// 管理端会话统计（数据管理模块）：按日新建会话数。generate_series 在 SQL
	// 内补全 0 值完整日序列（近 $2 天含当天），日期统一按 DB 会话时区截断
	//（标准部署下 DB 与服务同 TZ），避免 Go 侧补全因时区差异产生偏差。
	sqlSessionStatsDaily = `WITH days AS (
			SELECT generate_series(
				date_trunc('day', now() - make_interval(days => $2::int - 1)),
				date_trunc('day', now()),
				'1 day'::interval
			)::date AS day
		)
		SELECT to_char(days.day, 'YYYY-MM-DD') AS date, COALESCE(cnt, 0) AS sessions
		FROM days
		LEFT JOIN (
			SELECT date_trunc('day', created_at) AS day, COUNT(*) AS cnt
			FROM sessions
			WHERE status = $1 AND created_at >= date_trunc('day', now() - make_interval(days => $2::int - 1))
			GROUP BY 1
		) s ON s.day = days.day
		ORDER BY days.day`

	sqlSessionStatsByAgent = `SELECT COALESCE(agent_id, ''), COUNT(*)
		FROM sessions
		WHERE status = $1 AND created_at >= date_trunc('day', now() - make_interval(days => $2::int - 1))
		GROUP BY 1 ORDER BY 2 DESC`

	sqlSessionStatsTotal = `SELECT COUNT(*) FROM sessions WHERE status = $1`

	// 仅返回可见消息（hidden=false），并统计每轮版本总数供切换 UI。
	sqlListMessages = `SELECT m.id, m.seq, m.role, m.content, m.reasoning, m.tool_call_id, m.tool_calls,
			m.round_no, m.version,
			(SELECT COUNT(DISTINCT version) FROM messages v
			 WHERE v.session_id = m.session_id AND v.round_no = m.round_no) AS total_versions
		FROM messages m
		WHERE m.session_id = $1 AND m.hidden = false
		ORDER BY m.seq ASC`

	sqlListMessagesUptoRound = `SELECT id, seq, role, content, reasoning, tool_call_id, tool_calls, round_no, version
		FROM messages
		WHERE session_id = $1 AND hidden = false AND round_no <= $2
		ORDER BY seq ASC`

	sqlMaxSeq = `SELECT COALESCE(MAX(seq), 0) FROM messages WHERE session_id = $1`

	sqlMaxRound = `SELECT COALESCE(MAX(round_no), 0) FROM messages WHERE session_id = $1`

	sqlAppendMessage = `INSERT INTO messages
		(session_id, seq, role, content, reasoning, tool_call_id, tool_calls, round_no, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	sqlGetMessage = `SELECT round_no, role FROM messages
		WHERE id = $1 AND session_id = $2 AND hidden = false`

	sqlDeleteRound = `DELETE FROM messages WHERE session_id = $1 AND round_no = $2`

	sqlCountSessionMessages = `SELECT COUNT(*) FROM messages WHERE session_id = $1`

	sqlSoftDeleteEmptySession = `UPDATE sessions SET status = 0, updated_at = now()
		WHERE id = $1 AND status = 1`

	sqlMaxRoundVersion = `SELECT COALESCE(MAX(version), 0) FROM messages
		WHERE session_id = $1 AND round_no = $2`

	sqlActiveRoundVersion = `SELECT COALESCE(MAX(version), 0) FROM messages
		WHERE session_id = $1 AND round_no = $2 AND role <> 'user' AND hidden = false`

	sqlHideRoundAndAfter = `UPDATE messages SET hidden = true
		WHERE session_id = $1 AND (round_no > $2 OR (round_no = $2 AND role <> 'user'))`

	sqlRestoreRoundAndAfter = `UPDATE messages SET hidden = false
		WHERE session_id = $1 AND (round_no > $2 OR (round_no = $2 AND role <> 'user' AND version = $3))`

	sqlSetActiveVersionShow = `UPDATE messages SET hidden = false
		WHERE session_id = $1 AND round_no = $2 AND role <> 'user' AND version = $3`

	sqlSetActiveVersionHide = `UPDATE messages SET hidden = true
		WHERE session_id = $1 AND (round_no > $2 OR (round_no = $2 AND role <> 'user' AND version <> $3))`

	sqlInsertAuditToolCall = `INSERT INTO audit_tool_calls
		(user_id, session_id, agent_name, tool, tool_call_id, arguments, result, is_error, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)`
)

// sessionColumns 复用列集合（rowToSession 用）。
// config 为 JSONB 列，统一转文本后由调用方解析（'{}' 缺省为空配置）。
func rowToSession(row pgx.Row) (*Session, error) {
	var s Session
	var cfg string
	if err := row.Scan(&s.ID, &s.UserID, &s.AgentID, &s.Title, &s.Status, &s.CreatedAt, &s.UpdatedAt, &cfg); err != nil {
		return nil, err // 保持 ErrNoRows 等原始错误，交调用方判定
	}
	if cfg != "" && cfg != "{}" {
		if err := json.Unmarshal([]byte(cfg), &s.Config); err != nil {
			return nil, fmt.Errorf("解析会话配置失败: %w", err)
		}
	}
	return &s, nil
}

// translateSessionErr ErrNoRows → CodeNotFound。
func translateSessionErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.New(apperr.CodeNotFound, "会话不存在")
	}
	return apperr.Wrap(apperr.CodeInternal, "查询会话失败", err)
}

func (r *postgresRepo) CreateSession(ctx context.Context, userID int64, agentID, title string) (*Session, error) {
	s, err := rowToSession(r.pool.QueryRow(ctx, sqlCreateSession, userID, agentID, title))
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "创建会话失败", err)
	}
	return s, nil
}

func (r *postgresRepo) ListSessions(ctx context.Context, userID int64, agentID string, page, pageSize int) ([]*Session, int64, error) {
	offset := (page - 1) * pageSize
	rows, err := r.pool.Query(ctx, sqlListSessions, userID, SessionStatusActive, agentID, pageSize, offset)
	if err != nil {
		return nil, 0, apperr.Wrap(apperr.CodeInternal, "查询会话列表失败", err)
	}
	defer rows.Close()

	list := make([]*Session, 0)
	for rows.Next() {
		var s Session
		var cfg string
		if err := rows.Scan(&s.ID, &s.UserID, &s.AgentID, &s.Title, &s.Status, &s.CreatedAt, &s.UpdatedAt, &cfg); err != nil {
			return nil, 0, apperr.Wrap(apperr.CodeInternal, "读取会话列表失败", err)
		}
		if cfg != "" && cfg != "{}" {
			if err := json.Unmarshal([]byte(cfg), &s.Config); err != nil {
				return nil, 0, apperr.Wrap(apperr.CodeInternal, "解析会话配置失败", err)
			}
		}
		list = append(list, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, apperr.Wrap(apperr.CodeInternal, "遍历会话列表失败", err)
	}

	var total int64
	if err := r.pool.QueryRow(ctx, sqlCountSessions, userID, SessionStatusActive, agentID).Scan(&total); err != nil {
		return nil, 0, apperr.Wrap(apperr.CodeInternal, "统计会话数失败", err)
	}
	return list, total, nil
}

func (r *postgresRepo) GetSession(ctx context.Context, sessionID int64) (*Session, error) {
	s, err := rowToSession(r.pool.QueryRow(ctx, sqlGetSession, sessionID))
	if err != nil {
		return nil, translateSessionErr(err)
	}
	return s, nil
}

// MergeGuestSessions 把游客命名空间下有效会话转移给真实账号。
// 消息表以 session_id 关联，随会话自动归属新属主；仅更新 sessions.user_id。
// 只迁移有效会话（status=1），已删除的游客会话不迁移（保留原状，不复活）。
func (r *postgresRepo) MergeGuestSessions(ctx context.Context, guestUserID, targetUserID int64) (int, error) {
	tag, err := r.pool.Exec(ctx, sqlMergeGuestSessions, guestUserID, targetUserID)
	if err != nil {
		return 0, apperr.Wrap(apperr.CodeInternal, "合并游客会话失败", err)
	}
	return int(tag.RowsAffected()), nil
}

// SessionStats 管理端会话统计（数据管理模块）：按日 + 按智能体域 + 全量累计。
func (r *postgresRepo) SessionStats(ctx context.Context, days int) (*SessionStats, error) {
	st := &SessionStats{}

	rows, err := r.pool.Query(ctx, sqlSessionStatsDaily, SessionStatusActive, days)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "统计按日会话失败", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d SessionDayStat
		if err := rows.Scan(&d.Date, &d.Sessions); err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "解析会话统计行失败", err)
		}
		st.Daily = append(st.Daily, d)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "遍历会话统计失败", err)
	}

	rows, err = r.pool.Query(ctx, sqlSessionStatsByAgent, SessionStatusActive, days)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "统计智能体会话失败", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a SessionAgentStat
		if err := rows.Scan(&a.AgentID, &a.Sessions); err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "解析会话统计行失败", err)
		}
		st.ByAgent = append(st.ByAgent, a)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "遍历会话统计失败", err)
	}

	if err := r.pool.QueryRow(ctx, sqlSessionStatsTotal, SessionStatusActive).Scan(&st.Total); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "统计累计会话失败", err)
	}
	return st, nil
}

func (r *postgresRepo) DeleteSession(ctx context.Context, sessionID int64) error {
	// 已删除（status=0）时 WHERE 不命中，幂等返回成功。
	if _, err := r.pool.Exec(ctx, sqlDeleteSession, sessionID); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "删除会话失败", err)
	}
	return nil
}

func (r *postgresRepo) UpdateSessionTitle(ctx context.Context, sessionID int64, title string) error {
	if _, err := r.pool.Exec(ctx, sqlRenameSession, sessionID, title); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "重命名会话失败", err)
	}
	return nil
}

func (r *postgresRepo) UpdateSessionConfig(ctx context.Context, sessionID int64, cfg SessionConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "序列化会话配置失败", err)
	}
	if _, err := r.pool.Exec(ctx, sqlUpdateSessionConfig, sessionID, string(b)); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "更新会话配置失败", err)
	}
	return nil
}

func (r *postgresRepo) ListMessages(ctx context.Context, sessionID int64) ([]*Message, error) {
	rows, err := r.pool.Query(ctx, sqlListMessages, sessionID)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "查询消息失败", err)
	}
	defer rows.Close()

	out := make([]*Message, 0)
	for rows.Next() {
		var (
			id            int64
			seq           int64
			role          string
			content       string
			reasoning     string
			toolCallID    string
			toolCalls     []byte
			roundNo       int64
			version       int
			totalVersions int
		)
		if err := rows.Scan(&id, &seq, &role, &content, &reasoning, &toolCallID, &toolCalls,
			&roundNo, &version, &totalVersions); err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "读取消息失败", err)
		}
		m := &Message{
			ID: id, Role: role, Content: content, Reasoning: reasoning,
			ToolCallID: toolCallID, RoundNo: roundNo, Version: version,
			TotalVersions: totalVersions,
		}
		if len(toolCalls) > 0 {
			if err := json.Unmarshal(toolCalls, &m.ToolCalls); err != nil {
				return nil, apperr.Wrap(apperr.CodeInternal, "解析 tool_calls 失败", err)
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "遍历消息失败", err)
	}
	return out, nil
}

func (r *postgresRepo) ListMessagesUptoRound(ctx context.Context, sessionID, uptoRound int64) ([]*Message, error) {
	rows, err := r.pool.Query(ctx, sqlListMessagesUptoRound, sessionID, uptoRound)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "查询消息失败", err)
	}
	defer rows.Close()

	out := make([]*Message, 0)
	for rows.Next() {
		var (
			id         int64
			seq        int64
			role       string
			content    string
			reasoning  string
			toolCallID string
			toolCalls  []byte
			roundNo    int64
			version    int
		)
		if err := rows.Scan(&id, &seq, &role, &content, &reasoning, &toolCallID, &toolCalls,
			&roundNo, &version); err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "读取消息失败", err)
		}
		m := &Message{
			ID: id, Role: role, Content: content, Reasoning: reasoning,
			ToolCallID: toolCallID, RoundNo: roundNo, Version: version,
		}
		if len(toolCalls) > 0 {
			if err := json.Unmarshal(toolCalls, &m.ToolCalls); err != nil {
				return nil, apperr.Wrap(apperr.CodeInternal, "解析 tool_calls 失败", err)
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "遍历消息失败", err)
	}
	return out, nil
}

// GetMessage 定位可见消息的轮次与角色（重生成/分支/版本切换入口）。
func (r *postgresRepo) GetMessage(ctx context.Context, sessionID, messageID int64) (*Message, error) {
	var m Message
	err := r.pool.QueryRow(ctx, sqlGetMessage, messageID, sessionID).Scan(&m.RoundNo, &m.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(apperr.CodeNotFound, "消息不存在")
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "查询消息失败", err)
	}
	m.ID = messageID
	return &m, nil
}

// DeleteRound 删除消息所在的一轮完整对话；删空后自动软删会话。
func (r *postgresRepo) DeleteRound(ctx context.Context, sessionID, messageID int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "开启消息删除事务失败", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1) 定位消息所在轮次（不存在/已隐藏 → 幂等成功）。
	var roundNo int64
	err = tx.QueryRow(ctx, sqlGetMessage, messageID, sessionID).Scan(&roundNo, new(string))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "查询目标消息失败", err)
	}

	// 2) 删除该轮全部消息（user + assistant + tool 成对清空）。
	if _, err := tx.Exec(ctx, sqlDeleteRound, sessionID, roundNo); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "删除该轮消息失败", err)
	}

	// 3) 会话已无任何消息 → 自动软删会话（空会话不保留）。
	var remain int64
	if err := tx.QueryRow(ctx, sqlCountSessionMessages, sessionID).Scan(&remain); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "统计剩余消息失败", err)
	}
	if remain == 0 {
		if _, err := tx.Exec(ctx, sqlSoftDeleteEmptySession, sessionID); err != nil {
			return apperr.Wrap(apperr.CodeInternal, "自动删除空会话失败", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "提交消息删除事务失败", err)
	}
	return nil
}

func (r *postgresRepo) MaxRoundNo(ctx context.Context, sessionID int64) (int64, error) {
	var max int64
	if err := r.pool.QueryRow(ctx, sqlMaxRound, sessionID).Scan(&max); err != nil {
		return 0, apperr.Wrap(apperr.CodeInternal, "查询最大轮次失败", err)
	}
	return max, nil
}

func (r *postgresRepo) MaxRoundVersion(ctx context.Context, sessionID, roundNo int64) (int, error) {
	var v int
	if err := r.pool.QueryRow(ctx, sqlMaxRoundVersion, sessionID, roundNo).Scan(&v); err != nil {
		return 0, apperr.Wrap(apperr.CodeInternal, "查询最大版本失败", err)
	}
	return v, nil
}

func (r *postgresRepo) ActiveRoundVersion(ctx context.Context, sessionID, roundNo int64) (int, error) {
	var v int
	if err := r.pool.QueryRow(ctx, sqlActiveRoundVersion, sessionID, roundNo).Scan(&v); err != nil {
		return 0, apperr.Wrap(apperr.CodeInternal, "查询活跃版本失败", err)
	}
	return v, nil
}

func (r *postgresRepo) HideRoundAndAfter(ctx context.Context, sessionID, roundNo int64) error {
	if _, err := r.pool.Exec(ctx, sqlHideRoundAndAfter, sessionID, roundNo); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "截断旧分支失败", err)
	}
	return nil
}

func (r *postgresRepo) RestoreRoundAndAfter(ctx context.Context, sessionID, roundNo int64, version int) error {
	if _, err := r.pool.Exec(ctx, sqlRestoreRoundAndAfter, sessionID, roundNo, version); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "恢复旧分支失败", err)
	}
	return nil
}

func (r *postgresRepo) SetActiveVersion(ctx context.Context, sessionID, roundNo int64, version int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "开启版本切换事务失败", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1) 显示目标版本的回答（assistant/tool）。
	if _, err := tx.Exec(ctx, sqlSetActiveVersionShow, sessionID, roundNo, version); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "显示目标版本失败", err)
	}
	// 2) 隐藏该轮其它版本 + 截断后续轮次。
	if _, err := tx.Exec(ctx, sqlSetActiveVersionHide, sessionID, roundNo, version); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "隐藏其它版本失败", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "提交版本切换事务失败", err)
	}
	return nil
}

func (r *postgresRepo) AppendMessages(ctx context.Context, sessionID int64, msgs []*Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, "开启消息事务失败", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var maxSeq int64
	if err := tx.QueryRow(ctx, sqlMaxSeq, sessionID).Scan(&maxSeq); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "查询消息序号失败", err)
	}

	for i, m := range msgs {
		var toolCalls []byte
		if len(m.ToolCalls) > 0 {
			toolCalls, err = json.Marshal(m.ToolCalls)
			if err != nil {
				return apperr.Wrap(apperr.CodeInternal, "序列化 tool_calls 失败", err)
			}
		}
		seq := maxSeq + int64(i) + 1
		if _, err := tx.Exec(ctx, sqlAppendMessage,
			sessionID, seq, m.Role, m.Content, m.Reasoning, m.ToolCallID, toolCalls,
			m.RoundNo, m.Version,
		); err != nil {
			return apperr.Wrap(apperr.CodeInternal, fmt.Sprintf("写入第 %d 条消息失败", seq), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "提交消息事务失败", err)
	}
	return nil
}

// InsertAuditToolCall 写入一条工具调用审计记录。
// 失败仅向上返回错误，由 service 记日志降级，不阻塞对话主流程。
func (r *postgresRepo) InsertAuditToolCall(ctx context.Context, a *AuditToolCall) error {
	argsJSON, err := json.Marshal(a.Arguments)
	if err != nil {
		return fmt.Errorf("序列化审计参数失败: %w", err)
	}
	if len(argsJSON) == 0 {
		argsJSON = []byte("{}")
	}
	agentName := a.AgentName
	if agentName == "" {
		agentName = defaultAgentName
	}
	_, err = r.pool.Exec(ctx, sqlInsertAuditToolCall,
		a.UserID, a.SessionID, agentName, a.Tool, a.ToolCallID, string(argsJSON),
		a.Result, a.IsError, a.DurationMs,
	)
	if err != nil {
		return fmt.Errorf("写入审计记录失败: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 编排过程输出入库（P4-I）：多智能体编排执行记录。
// ---------------------------------------------------------------------------

// OrchestrationTask 单个子任务的执行记录（JSONB 存储，字段即 JSON 键）。
type OrchestrationTask struct {
	TaskID     string `json:"task_id"`
	Role       string `json:"role"`   // research/outline/content/review/worker...
	Status     string `json:"status"` // completed | failed | skipped
	Output     string `json:"output"` // 子任务输出（入库前截断，防表膨胀）
	Error      string `json:"error,omitempty"`
	Tokens     int64  `json:"tokens"`
	DurationMs int64  `json:"duration_ms"`
}

// OrchestrationRun 一次多智能体编排的执行记录。
type OrchestrationRun struct {
	SessionID   int64
	UserID      int64
	RoundNo     int64 // 关联的对话轮次（重新生成场景多版本共一轮，round_no 对齐）
	Goal        string
	Status      string // completed（全部子任务完成）| partial（部分失败/被跳过）
	Tasks       []OrchestrationTask
	Result      string // 最终回答
	Error       string // run 级失败原因（计划/执行/合并失败；子任务失败不在此）
	TotalTokens int64
}

const sqlInsertOrchestration = `INSERT INTO orchestration_runs
	(session_id, user_id, round_no, goal, status, tasks, result, error, total_tokens)
	VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)`

// SaveOrchestration 落库一次编排执行记录。
// 写失败仅向上返回错误，由调用方（service）记日志降级，不阻塞对话主流程。
func (r *postgresRepo) SaveOrchestration(ctx context.Context, run *OrchestrationRun) error {
	tasks, err := json.Marshal(run.Tasks)
	if err != nil {
		return fmt.Errorf("序列化编排任务失败: %w", err)
	}
	if len(tasks) == 0 {
		tasks = []byte("[]")
	}
	_, err = r.pool.Exec(ctx, sqlInsertOrchestration,
		run.SessionID, run.UserID, run.RoundNo, run.Goal, run.Status, string(tasks),
		run.Result, run.Error, run.TotalTokens,
	)
	if err != nil {
		return fmt.Errorf("写入编排记录失败: %w", err)
	}
	return nil
}
