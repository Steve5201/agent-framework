-- 模型启停（P3 大模型管理增强）：
-- enabled=false 的模型不参与请求路由、不出现在公开列表，但保留配置可再次启用。
-- 默认模型受保护（唯一且不可删除），同样不可禁用——默认位 = 始终可用。
-- 存量行默认 enabled=true，兼容升级。
ALTER TABLE models ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;
