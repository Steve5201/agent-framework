package authsvc

import "time"

// Tag 用户标签（key-value 对，DB 存 JSONB 数组）。
// key 语义由调用方约定：目前 "agent" = 分智能体来源（注册/登录入口的 agent_id）。
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// User 用户领域模型。
// ID 对外统一用 string（与 proto 契约一致）；DB 存储为 BIGSERIAL(int64)，
// 转换发生在 repository 层，领域层不感知数据库细节。
type User struct {
	ID           string
	Username     string
	PasswordHash string // 仅服务端内部使用，永不进入 gRPC 响应
	Role         Role
	Status       int // 1=正常 0=禁用
	Tags         []Tag
	CreatedAt    time.Time
}

// Active 判断账号是否可用（未禁用）。
func (u *User) Active() bool { return u.Status == 1 }

// AgentScope 返回用户的智能体归属（tags 中 key="agent" 的值；无归属返回空串）。
// 普通用户/普通管理员/智能体超管：返回其门户 ID（如 "tutor"）；
// 最高超管：返回全门户标识 "*"（含义 = 全部智能体，可经任意门户切换聊天域）。
func (u *User) AgentScope() string {
	for _, t := range u.Tags {
		if t.Key == tagKeyAgent {
			return t.Value
		}
	}
	return ""
}

// Agent 智能体注册表条目（阶段3·多租户）。
// ID 与 sessions.agent_id / 资源归属维度对应（如 'tutor'）；
// OwnerUserID 为该智能体超管（agent_admin）的 users.id。
type Agent struct {
	ID          string
	Name        string
	Description string
	Model       string
	OwnerUserID int64
	Status      int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Avatar          string // 形象 emoji（空 = 首字兜底）
	Welcome         string // 欢迎语（空 = 实例默认）
	SystemPrompt    string // 按智能体系统提示词（空 = 全局）
	ReasoningEffort string // 默认推理强度 low/high/max（空 = 实例默认）
}

// RefreshToken 长效令牌记录。
// FamilyID 标识令牌族：族内轮换、登出整族吊销。
type RefreshToken struct {
	ID        int64
	UserID    int64
	FamilyID  string
	TokenHash string // SHA-256(token)，DB 不落明文
	ExpiresAt time.Time
	RevokedAt *time.Time // nil=有效；非 nil=已吊销
}
