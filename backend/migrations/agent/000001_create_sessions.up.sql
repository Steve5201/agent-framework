-- 000001: 创建 agent 服务会话表 sessions
-- 说明：user_id 引用 auth 库的 users.id。PostgreSQL 不支持跨库外键，
--       因此仅存值、不加 FK 约束，一致性由应用层保证。
CREATE TABLE IF NOT EXISTS sessions (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL,            -- auth.users.id（跨库，无 FK）
    title      TEXT        NOT NULL DEFAULT '新对话',
    status     SMALLINT    NOT NULL DEFAULT 1,  -- 1=正常 0=已删除（软删）
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 会话列表查询：按用户 + 更新时间倒序
CREATE INDEX IF NOT EXISTS idx_sessions_user_updated ON sessions (user_id, updated_at DESC);
