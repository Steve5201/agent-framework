-- 回退：将未绑定 owner 的行置 0 后恢复 NOT NULL（owner_user_id=0 表示无 owner）。
UPDATE agents SET owner_user_id = 0 WHERE owner_user_id IS NULL;
ALTER TABLE agents ALTER COLUMN owner_user_id SET NOT NULL;
