-- 模型注册表：上游参数兼容性开关（P4-I 学校网关接入）。
--
-- no_thinking：上游是否不支持 thinking / reasoning_effort 参数（如 litellm
-- custom_openai 代理、Ollama 等标准 OpenAI 端点）。为 true 时 llm-gateway
-- 转发前剥离这两个参数，否则上游返回 400（UnsupportedParamsError）。
-- 默认 false（DeepSeek 等官方端点支持思考参数，保持透传）。

ALTER TABLE models ADD COLUMN no_thinking BOOLEAN NOT NULL DEFAULT false;
