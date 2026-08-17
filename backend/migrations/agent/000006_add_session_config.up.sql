-- 000006 会话配置：sessions 表新增 config JSONB 列（工具权限 / 思考模式）。
-- 默认空对象 = 全部工具启用、思考按厂商默认。后期管理端扩展配置时
-- 只在此 JSON 里加键，无需再迁移。
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS config JSONB NOT NULL DEFAULT '{}'::jsonb;
