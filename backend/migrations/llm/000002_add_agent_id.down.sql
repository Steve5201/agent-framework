-- 回滚：删除用量按智能体域聚合的列与索引。
DROP INDEX IF EXISTS idx_usage_logs_agent_created;
ALTER TABLE usage_logs
	DROP COLUMN IF EXISTS agent_id;
