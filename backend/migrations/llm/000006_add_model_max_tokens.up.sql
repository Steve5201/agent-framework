-- 模型注册表：请求级 max_tokens（completion 输出上限）。
--
-- 背景（2026-08 排查）：render_html 生成大文档时 deepseek-v4 的完成参数总被
-- 截断。根因：llm-gateway 之前不发送 max_tokens 字段，DeepSeek 官方端点未显式
-- 传 max_tokens 时受服务端默认输出上限约束（实测 completion 精确卡 8192 截断）；
-- 而 GLM-5.2 经学校网关默认上限更大，所以同样代码能成功。
--
-- 修复：models 表增加 max_tokens 列（0 = 不设置，交上游默认），注册表配置后
-- llm-gateway 在请求体携带 max_tokens，解除默认 8192 截断。大文档/长工具参数
-- 模型建议显式设置（如 16384）。

ALTER TABLE models ADD COLUMN max_tokens INT NOT NULL DEFAULT 0;
