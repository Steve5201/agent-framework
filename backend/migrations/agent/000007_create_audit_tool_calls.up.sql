-- 000007 工具调用审计表：记录"谁在哪个会话里、调用了什么工具、参数与结果"。
--
-- 用途（阶段1·审计）：每次工具执行（成功/失败）落一条审计，供管理端数据模块
-- 后续展示与安全审查。写失败只记日志、不阻塞对话主流程。
--
-- user_id 引用 auth.users.id（跨库、无 FK，同 sessions.user_id 约定）；
-- agent_name 预留多智能体扩展，当前统一 "default"。
CREATE TABLE IF NOT EXISTS audit_tool_calls (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT      NOT NULL,
    session_id   BIGINT      NOT NULL,
    agent_name   TEXT        NOT NULL DEFAULT 'default',
    tool         TEXT        NOT NULL,                    -- 工具名（如 file_ops、skill_emoji-helper）
    tool_call_id TEXT        NOT NULL DEFAULT '',         -- 关联 assistant 消息中的工具调用 ID
    arguments    JSONB       NOT NULL DEFAULT '{}'::jsonb,-- 工具调用参数（原样 JSON）
    result       TEXT        NOT NULL DEFAULT '',         -- 工具返回文本（成功内容或错误描述）
    is_error     BOOLEAN     NOT NULL DEFAULT false,
    duration_ms  BIGINT      NOT NULL DEFAULT 0,          -- 单次执行耗时（毫秒）
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_tool_calls_created
    ON audit_tool_calls (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_tool_calls_user_created
    ON audit_tool_calls (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_tool_calls_session_created
    ON audit_tool_calls (session_id, created_at DESC);
