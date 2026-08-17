-- 模型注册表（P3 大模型管理）：对外模型名 → 上游接入参数。
--
-- 单一事实源：API Key 只允许存在于 llm-gateway（本服务），模型 CRUD 经
-- /v1/admin/models* 管理端点读写本表；agent/gateway 均不接触任何密钥。
--
-- 设计要点：
--   * name 是客户端（会话配置/OpenAI 请求体 model 字段）使用的模型名，主键；
--   * api_key 允许为空 → 本地模型（如 Ollama）无密钥，构造上游客户端时
--     使用占位密钥（OpenAICompatible 构造要求非空），本地端点忽略该头；
--   * upstream_model 是实际发给上游的模型名，空 = 与 name 相同；
--   * 全局至多一个 is_default=true（唯一部分索引兜底，防并发写入破坏）。

CREATE TABLE IF NOT EXISTS models (
    name                    TEXT PRIMARY KEY,
    provider_name           TEXT NOT NULL DEFAULT '',
    base_url                TEXT NOT NULL,
    api_key                 TEXT NOT NULL DEFAULT '',
    upstream_model          TEXT NOT NULL DEFAULT '',
    timeout_sec             INT  NOT NULL DEFAULT 60,
    max_retries             INT  NOT NULL DEFAULT 0,
    prompt_price_per_1m     DOUBLE PRECISION NOT NULL DEFAULT 0,
    completion_price_per_1m DOUBLE PRECISION NOT NULL DEFAULT 0,
    is_default              BOOLEAN NOT NULL DEFAULT false,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 至多一个默认模型的唯一部分索引：is_default=true 的行在常量 true 上唯一。
-- 已有数据（空表）无冲突；若历史上存在多个默认行需先手动归并。
CREATE UNIQUE INDEX IF NOT EXISTS models_single_default_idx
    ON models ((true)) WHERE is_default;
