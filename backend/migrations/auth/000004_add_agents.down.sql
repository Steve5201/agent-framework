-- 000004_add_agents.down.sql —— 回滚：删除智能体表 + 角色还原。
DROP TABLE IF EXISTS agents;

UPDATE users SET role = 'admin' WHERE role = 'super_admin';
