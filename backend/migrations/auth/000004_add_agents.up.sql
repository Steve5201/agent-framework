-- 000004_add_agents.up.sql —— 智能体注册表 + 角色体系升级（阶段3·多租户）。
--
-- 1) 新增 agents 表（auth 库）：每个智能体一条记录，id 与会话/资源的
--    agent_id 维度对应（如 'tutor'）；owner_user_id 指向该智能体的超管
--    （agent_admin）用户，标识"智能体管理员组"的负责人。
-- 2) 角色升级：旧唯一管理员（role='admin'）→ 最高超管（role='super_admin'），
--    权限完整保留、不锁死系统；新角色体系见 internal/authsvc/rbac.go。

CREATE TABLE IF NOT EXISTS agents (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    owner_user_id BIGINT NOT NULL,
    status        SMALLINT NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agents_owner ON agents (owner_user_id);

-- 旧 admin → 最高超管（幂等：仅迁移一次，之后新角色体系不再产生 admin）。
UPDATE users SET role = 'super_admin' WHERE role = 'admin';
