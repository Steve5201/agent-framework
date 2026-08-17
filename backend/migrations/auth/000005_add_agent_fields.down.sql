-- 回滚：删除智能体元数据扩展列。
ALTER TABLE agents
	DROP COLUMN IF EXISTS avatar,
	DROP COLUMN IF EXISTS welcome,
	DROP COLUMN IF EXISTS system_prompt,
	DROP COLUMN IF EXISTS reasoning_effort;
