-- 用户 token 配额覆盖表（每用户独立额度）。
--
-- 背景（2026-08）：原配额机制只有全局默认 LLM_TOKEN_QUOTA_MONTH（每用户每月
-- 一个值），最高管理员也被同一上限约束。本表提供"按用户覆盖默认"的能力：
--   * 有记录 = 显式覆盖（token_quota_month 0 = 不限）；
--   * 无记录 = 跟随角色默认（管理员 → LLM_ADMIN_TOKEN_QUOTA_MONTH，
--     普通用户 → LLM_TOKEN_QUOTA_MONTH）。
--
-- 管理端点：/v1/admin/quota*（PUT 设额度 / DELETE 清除覆盖 / GET 列表含本月用量），
-- 由 llm-gateway 提供、gateway 的 adminsvc 代理，X-Admin-Token 保护。

CREATE TABLE IF NOT EXISTS user_quota (
    user_id           BIGINT      PRIMARY KEY,
    token_quota_month BIGINT      NOT NULL DEFAULT 0,  -- 0 = 不限
    updated_by        BIGINT      NOT NULL DEFAULT 0,  -- 操作者用户 ID（0 = 系统/迁移）
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
