-- 000003: messages 表补充工具调用字段（P2-D）
--
-- 背景：assistant 带工具调用的消息必须与 role=tool 的结果消息成对存在，
-- 历史恢复时缺一不可（framework 协议要求，否则模型拒绝继续推理）。
-- 000002 刻意未持久化工具细节，P2-44 历史恢复要完整，本迁移补齐。

ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS tool_call_id TEXT,   -- tool 消息：对应哪次工具调用
    ADD COLUMN IF NOT EXISTS tool_calls  JSONB;   -- assistant 消息：工具调用指令（schema.ToolCall 数组）
