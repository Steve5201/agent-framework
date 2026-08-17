-- 000001: 创建 llm-gateway 用量日志表 usage_logs
-- 作用：成本核算与配额控制的唯一事实来源，每请求一条。
CREATE TABLE IF NOT EXISTS usage_logs (
    id                BIGSERIAL PRIMARY KEY,
    user_id           BIGINT         NOT NULL,          -- 调用方用户（跨库，无 FK）
    request_id        TEXT           NOT NULL,          -- 全链路 request_id
    model             TEXT           NOT NULL,
    prompt_tokens     INT            NOT NULL DEFAULT 0,
    completion_tokens INT            NOT NULL DEFAULT 0,
    total_tokens      INT            NOT NULL DEFAULT 0,
    cost_usd          NUMERIC(10, 6) NOT NULL DEFAULT 0,
    stream            BOOLEAN        NOT NULL DEFAULT FALSE, -- 是否流式请求
    status            SMALLINT       NOT NULL DEFAULT 0,     -- 0=成功 1=失败
    created_at        TIMESTAMPTZ    NOT NULL DEFAULT now()
);

-- 用量查询：按用户 + 时间倒序；按 request_id 精确检索
CREATE INDEX IF NOT EXISTS idx_usage_logs_user_time ON usage_logs (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_logs_request_id ON usage_logs (request_id);
