-- 000003_add_ingest_attempts.down.sql —— 回滚摄取重试相关字段。

DROP INDEX IF EXISTS idx_documents_queued;

ALTER TABLE documents
    DROP COLUMN IF EXISTS attempt,
    DROP COLUMN IF EXISTS retry_at,
    DROP COLUMN IF EXISTS queued_at;
