-- 000002_add_agent_id.up.sql —— 知识库按智能体归属（阶段3·多租户）。
--
-- agent_id：知识库所属智能体域（与 sessions.agent_id 对应，如 'tutor'）。
-- 存量数据（开发期全局知识库）统一归属默认智能体 tutor（已与用户确认）。
-- 管理端权限：除最高超管外，管理员只能看到/操作自己智能体组的资源。

ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT 'tutor';

CREATE INDEX IF NOT EXISTS idx_kb_agent_updated ON knowledge_bases (agent_id, updated_at);
