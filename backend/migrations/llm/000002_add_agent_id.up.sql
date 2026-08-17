-- 用量按智能体域聚合：usage_logs 增加 agent_id（X-Agent-Id 注入，P2-AI）。
-- 非智能体入口（直连调试）保持空串，不计入任何域。
ALTER TABLE usage_logs
	ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_usage_logs_agent_created
	ON usage_logs (agent_id, created_at);
