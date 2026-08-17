-- 编排过程入库增加 round_no：把过程记录与"该轮对话"关联（重新生成场景
-- 同一轮有多个版本，round_no 对齐；前端按轮次/版本展示历史编排过程）。
ALTER TABLE orchestration_runs ADD COLUMN IF NOT EXISTS round_no BIGINT NOT NULL DEFAULT 0;
