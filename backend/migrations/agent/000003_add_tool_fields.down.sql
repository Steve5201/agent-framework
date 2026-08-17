-- 000003: 回滚——移除工具调用字段

ALTER TABLE messages
    DROP COLUMN IF EXISTS tool_call_id,
    DROP COLUMN IF EXISTS tool_calls;
