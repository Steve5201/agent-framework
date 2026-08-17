-- 000001: 创建 auth 服务核心表 users
-- 说明：auth 服务负责身份认证，username 为登录标识（唯一）。
CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL DEFAULT 'user', -- user | admin（RBAC）
    status        SMALLINT    NOT NULL DEFAULT 1,      -- 1=正常 0=禁用
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 常用查询索引
CREATE INDEX IF NOT EXISTS idx_users_status ON users (status);
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users (created_at);
