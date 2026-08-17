-- 000003_add_ingest_attempts.up.sql —— 摄取失败自动重试（P3-A 健壮性）。
--
-- 背景：摄取（解析/分块/向量化/落库）遇瞬时故障（如 embedding 上游冷启动、
-- 网络抖动）时，旧逻辑直接落 failed 终态，用户只能手动重传，且失败即入库。
--
-- attempt   ：失败自动重试计数（0=首次，上限由 RAG_INGEST_MAX_ATTEMPTS 控制）。
-- retry_at  ：下次可领取的时间戳（指数退避）；未到期的 queued 任务不会被 worker 抢占。
-- queued_at ：最近一次入队时间（含失败重入队），worker 按此公平排队，
--             重试任务排到队尾，避免插队饿死新上传的文档。

ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS attempt   INTEGER     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS retry_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS queued_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- 重入队/到期过滤走 queued_at 排序，建索引避免全表扫描。
CREATE INDEX IF NOT EXISTS idx_documents_queued ON documents (status, queued_at)
    WHERE status = 'queued';
