-- 000002: 创建 refresh_tokens 表（JWT 双令牌中的长效令牌）
-- 设计要点：
--   - token_hash 存 SHA-256 摘要而非明文，DB 泄露也不暴露令牌；
--   - family_id 标识"同一登录会话的令牌族"：族内轮换，登出时整族吊销；
--   - 每次刷新：旧令牌标记 revoked → 签发新令牌（同 family_id）→ 单次使用。
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id  UUID        NOT NULL,
    token_hash TEXT        NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,           -- NULL=有效；非空=已吊销
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens (user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family   ON refresh_tokens (family_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires  ON refresh_tokens (expires_at);
