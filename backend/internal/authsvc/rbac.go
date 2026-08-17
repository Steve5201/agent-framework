package authsvc

// Role 用户角色（RBAC）。值直接存入 users.role 列，与 proto/DB 三方一致。
//
// 阶段3·多租户角色体系：
//   - super_admin  最高超管（唯一）：完整权限，可创建任意管理员/智能体；
//   - agent_admin  智能体超管：只能管理自己智能体（含组内普通管理员/资源）；
//   - admin        普通管理员：只能管理自己智能体组内的资源（无用户管理）；
//   - user         普通用户（注册/登录产生）。
//
// 历史兼容：RoleAdmin（"admin"）为旧体系遗留值，迁移已将其升级为
// super_admin（见 migrations/auth/000004），此处仅保留解析兼容。
type Role string

const (
	RoleUser       Role = "user"
	RoleAdmin      Role = "admin"
	RoleAgentAdmin Role = "agent_admin"
	RoleSuperAdmin Role = "super_admin"
)

// Valid 判断角色是否合法。
func (r Role) Valid() bool {
	switch r {
	case RoleUser, RoleAdmin, RoleAgentAdmin, RoleSuperAdmin:
		return true
	}
	return false
}

// IsAdmin 判断是否具备管理端访问权（/v1/admin/*）。
// 最高超管/智能体超管/普通管理员均为"管理员"，可进管理端。
func (r Role) IsAdmin() bool {
	switch r {
	case RoleSuperAdmin, RoleAgentAdmin, RoleAdmin:
		return true
	}
	return false
}

// CanManageUsers 判断是否具备用户管理权：
// super_admin 全局；agent_admin 仅限自己智能体组（校验由调用方/AdminListUsers 完成）。
func (r Role) CanManageUsers() bool {
	return r == RoleSuperAdmin || r == RoleAgentAdmin
}

// CanCreateAgent 判断是否可创建智能体（仅最高超管）。
func (r Role) CanCreateAgent() bool { return r == RoleSuperAdmin }

// CanManageAgent 判断是否可管理指定智能体：
// super_admin 任意域；agent_admin 仅限自身归属域（scope 为调用者的智能体组）。
func (r Role) CanManageAgent(agentID, scope string) bool {
	switch r {
	case RoleSuperAdmin:
		return true
	case RoleAgentAdmin:
		return agentID != "" && agentID == scope
	}
	return false
}

// CanSetAgentStatus 判断是否可启停/删除智能体（仅最高超管）。
func (r Role) CanSetAgentStatus() bool { return r == RoleSuperAdmin }

// Rank 角色等级（数值越大权限越高）：
// user=0、admin=1、agent_admin=2、super_admin=3。
// 用于"仅可管理比自己低级的账号"的分层校验（重置密码/删除用户等管理操作）。
func (r Role) Rank() int {
	switch r {
	case RoleSuperAdmin:
		return 3
	case RoleAgentAdmin:
		return 2
	case RoleAdmin:
		return 1
	default:
		return 0
	}
}

// CanManageUser 判断调用者角色（r）能否管理目标角色（target）的账号：
//   - 调用者必须具备用户管理权（super_admin / agent_admin）；
//   - 调用者等级必须严格高于目标——平级（agent_admin 管 agent_admin、
//     super_admin 管 super_admin）一律拒绝，防管理员互相重置密码/删除的
//     横向越权（用户要求："当前账号权限大于被重置账号权限才能操作"）。
func (r Role) CanManageUser(target Role) bool {
	return r.CanManageUsers() && r.Rank() > target.Rank()
}
