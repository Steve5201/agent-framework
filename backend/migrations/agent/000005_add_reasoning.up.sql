-- 000005: messages 表新增 reasoning 列（思考内容）
-- 用途：保存 DeepSeek 思考模型的 reasoning_content。思考内容既是前端
-- "思考过程"气泡的数据源，也是工具调用轮次回传上游的必需上下文
-- （官方规则：工具轮后续请求必须带 reasoning_content，否则 400）。
ALTER TABLE messages ADD COLUMN IF NOT EXISTS reasoning TEXT NOT NULL DEFAULT '';
