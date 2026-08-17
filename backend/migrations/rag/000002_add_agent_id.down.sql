-- 000002_add_agent_id.down.sql —— 回滚：移除智能体归属列。
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS agent_id;
