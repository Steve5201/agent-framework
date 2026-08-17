-- 智能体元数据扩展：形象/欢迎语/系统提示词/默认推理强度。
-- 全部可空（'' 表示未设置，运行时用实例默认兜底）。
ALTER TABLE agents
	ADD COLUMN IF NOT EXISTS avatar          TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS welcome         TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS system_prompt   TEXT NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS reasoning_effort TEXT NOT NULL DEFAULT '';
