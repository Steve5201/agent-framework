-- 000012 会话操作日志：配置变更历史 + 每轮工具注入快照。
--
-- 目的（排查"模型感知工具出错"）：可追溯"工具集一步步怎么改的"与
-- "每轮实际注入给模型的工具是什么"，据此判断是配置问题还是模型幻觉。
--
-- 表1 session_config_logs：每次 UpdateSessionConfig 落一条（改前/改后 config）。
-- 表2 session_tool_snapshots：每轮对话开始前注入的工具名列表（快照）。
--
-- 写入均为旁路日志，失败只记服务日志、不阻塞对话主流程。
CREATE TABLE IF NOT EXISTS session_config_logs (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT      NOT NULL,
    session_id   BIGINT      NOT NULL,
    before_config JSONB      NOT NULL DEFAULT '{}'::jsonb,
    after_config  JSONB      NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_session_config_logs_session_created
    ON session_config_logs (session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS session_tool_snapshots (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT      NOT NULL,
    user_id      BIGINT      NOT NULL,
    tools        TEXT[]      NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_session_tool_snapshots_session_created
    ON session_tool_snapshots (session_id, created_at DESC);
