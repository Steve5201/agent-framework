-- owner 可选：创建智能体时可不绑定超管，稍后经 BindAgentOwner 绑定/更换/解绑。
ALTER TABLE agents ALTER COLUMN owner_user_id DROP NOT NULL;
