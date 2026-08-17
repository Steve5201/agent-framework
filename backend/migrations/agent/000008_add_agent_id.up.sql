-- 000008: sessions 表新增 agent_id 域（阶段2·独立地址 / 游客模式）。
--
-- 背景：各智能体与管理端不再共用同一会话列表——每个智能体有自己的
-- 聊天主界面地址（/agent/<id>），会话按 agent_id 隔离：
--   agent_id = ''      → 管理端域（管理员创建的会话）；
--   agent_id = '<id>'  → 对应智能体域（如 tutor），游客与普通用户在此域下建会话。
--
-- 现有数据统一回填为 ''（归属管理端域），保证老会话仍可在管理端聊天界面看到；
-- ADD COLUMN ... NOT NULL DEFAULT '' 在 PostgreSQL 中会对存量行自动回填默认值。
ALTER TABLE sessions ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';

-- 会话列表查询（按用户 + 智能体域 + 更新时间倒序）
CREATE INDEX IF NOT EXISTS idx_sessions_user_agent_updated
    ON sessions (user_id, agent_id, updated_at DESC);
