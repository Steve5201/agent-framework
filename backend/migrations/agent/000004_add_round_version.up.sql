-- 000004: messages 表追加轮次/版本/隐藏字段（P2-K）
--
-- 背景：删除"一轮完整对话"、重新生成（多版本保留+切换）、分支截断，
-- 都要求消息在"轮次"维度上组织，并支持同一轮存在多个版本。
--   round_no : 轮次序号（每个 role=user 消息开始新一轮）
--   version  : 重生成版本号（0=初始回答；重新生成后新版本号递增）
--   hidden   : 隐藏标记（非活跃版本 / 被截断的分支消息，不再进入历史加载）
--              —— 被隐藏的消息只是"不再展示/不再进上下文"，数据仍保留。

ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS round_no INT  NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS version  INT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS hidden   BOOL NOT NULL DEFAULT false;

-- 回填轮次：旧数据按 seq 顺序，每个 role=user 消息开始新轮；
-- 首轮之前无 user 的孤消息统一归入第 1 轮（GREATEST 兜底）。
UPDATE messages SET round_no = sub.r FROM (
    SELECT id,
           GREATEST(SUM(CASE WHEN role = 'user' THEN 1 ELSE 0 END)
                    OVER (PARTITION BY session_id ORDER BY seq), 1) AS r
    FROM messages
) sub WHERE messages.id = sub.id;

CREATE INDEX IF NOT EXISTS idx_messages_session_round ON messages (session_id, round_no);
