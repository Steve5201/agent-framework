-- 000003: users 表新增 tags JSONB（用户标签，key-value 数组）
-- 用途：
--   - 分智能体来源：注册/登录经 /v1/auth/register/{agent_id} 时写入 {key:'agent', value:<agent_id>}；
--   - 分用户群体：管理员建用户时可为不同群体打标签，前端据此控制配置按钮可见性
--     （如：不允许某些群体切换大模型/智能体）。
-- 设计：JSON 数组 [{key,value}]，按 key 可无限扩展；查询用 JSON 表达式（tags @> '[{"key":"agent","value":"tutor"}]'）。
ALTER TABLE users ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]'::jsonb;

-- 标签检索索引（按 key 过滤用户的场景：管理端按标签查询等）。
CREATE INDEX IF NOT EXISTS idx_users_tags ON users USING GIN (tags);
