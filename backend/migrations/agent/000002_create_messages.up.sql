-- 000002: 创建 agent 服务消息表 messages
-- 设计要点：
--   - session_id + seq 保证同一会话内消息有序（UNIQUE 约束）；
--   - role/content 与 framework schema.Message 对齐（system/user/assistant/tool）；
--   - 只落库"对话历史"所需字段，工具调用细节 P2 阶段不持久化。
CREATE TABLE IF NOT EXISTS messages (
    id         BIGSERIAL PRIMARY KEY,
    session_id BIGINT      NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq        INT         NOT NULL,              -- 会话内序号（从 1 起）
    role       TEXT        NOT NULL,              -- system/user/assistant/tool
    content    TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages (session_id, seq);
