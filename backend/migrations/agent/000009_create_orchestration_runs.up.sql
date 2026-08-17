-- 编排执行记录表（P4-I 编排过程输出入库）。
-- 每次多智能体编排落库一行：目标、整体状态、各子任务过程（JSONB）、最终回答。
-- 子任务 Output 截断存储（单任务上限，防表膨胀）；Error 记录失败原因。

CREATE TABLE IF NOT EXISTS orchestration_runs (
    id          BIGSERIAL PRIMARY KEY,
    session_id  BIGINT NOT NULL DEFAULT 0,
    user_id     BIGINT NOT NULL DEFAULT 0,
    goal        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT '',        -- completed | partial（部分任务失败）
    tasks       JSONB NOT NULL DEFAULT '[]',     -- 子任务过程数组（OrchestrationTask）
    result      TEXT NOT NULL DEFAULT '',        -- 最终回答
    error       TEXT NOT NULL DEFAULT '',        -- run 级失败原因（计划/执行/合并失败）
    total_tokens BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 会话视角按时间倒序查编排历史（会话详情/管理端复盘）。
CREATE INDEX IF NOT EXISTS idx_orchestration_runs_session
    ON orchestration_runs (session_id, created_at DESC);
