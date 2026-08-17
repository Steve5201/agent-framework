-- 000004 回滚
DROP INDEX IF EXISTS idx_messages_session_round;
ALTER TABLE messages
    DROP COLUMN IF EXISTS round_no,
    DROP COLUMN IF EXISTS version,
    DROP COLUMN IF EXISTS hidden;
