-- 000008 回滚：移除 agent_id 域列与其索引。
ALTER TABLE sessions DROP COLUMN IF EXISTS agent_id;
DROP INDEX IF EXISTS idx_sessions_user_agent_updated;
