-- 用户工作区磁盘配额（模块三·保护区配额管理）。
--
-- 语义（与 llm 库 user_quota 表同构，数据落在 agent 库——file_ops 校验侧）：
--   - 有记录 = 显式覆盖（disk_quota_mb=0 表示不限）；
--   - 无记录 = 走角色默认（AGENT_DISK_QUOTA_MB_* 环境变量，super_admin 默认不限）；
--   - 优先级：单用户显式覆盖 > 角色默认。
CREATE TABLE IF NOT EXISTS sandbox_disk_quota (
    user_id       BIGINT PRIMARY KEY,
    disk_quota_mb BIGINT NOT NULL,             -- 0 = 不限
    updated_by    BIGINT NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
