package authsvc

import (
	"regexp"
	"strings"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// tagKeyAgent 分智能体来源标签的 key（users.tags JSONB 数组中的 key）。
const tagKeyAgent = "agent"

// allAgentID 超管全门户标识（阶段3·统一标签模型）：
// 最高超管播种时打 {agent, "*"} 标签，含义 = 全部智能体（与 agentsvc 会话列表
// 的 '*' 通配域语义一致）。它不是注册表里的真实智能体，仅作为身份标识与
// 超管专属门户（/agent/*、/login/*）使用，天然不会被普通用户注册占用。
const allAgentID = "*"

// agentIDPattern 智能体 ID 白名单：字母数字 + 中划线（URL 路径参数友好，≤64 字符）。
// 注意：'*' 不在白名单内（它只作为超管全门户标识，不能注册成真实门户）。
var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// validateAgentID 校验 agent_id 格式；空值合法（表示通用/管理员入口）。
// '*' 在此被拒绝（它不是真实智能体，仅限登录/注册门户入口放行）。
func validateAgentID(agentID string) error {
	if agentID == "" {
		return nil
	}
	if !agentIDPattern.MatchString(agentID) {
		return apperr.New(apperr.CodeInvalidArgument, "非法的智能体 ID（仅限字母/数字/中划线，≤64 字符）")
	}
	return nil
}

// validatePortalID 校验门户入口参数（登录/注册路径里的 agent_id）：
// 在 validateAgentID 基础上放行 '*'（超管全门户标识，供超管经自己的门户登录）。
func validatePortalID(agentID string) error {
	if agentID == allAgentID {
		return nil
	}
	return validateAgentID(agentID)
}

// agentTags 由 agent_id 生成标签；空返回 nil。
func agentTags(agentID string) []Tag {
	if agentID == "" {
		return nil
	}
	return []Tag{{Key: tagKeyAgent, Value: agentID}}
}

// mergeTags 合并多组标签（去重：后者覆盖前者同 key；忽略空 key）。
func mergeTags(groups ...[]Tag) []Tag {
	out := make([]Tag, 0, 8)
	seen := make(map[string]bool, 8)
	for _, gs := range groups {
		for _, t := range gs {
			if t.Key == "" {
				continue
			}
			if seen[t.Key] {
				out = upsertTag(out, t)
				continue
			}
			seen[t.Key] = true
			out = append(out, t)
		}
	}
	return out
}

// upsertTag 插入/覆盖同 key 标签（返回新切片，不修改入参）。
func upsertTag(tags []Tag, t Tag) []Tag {
	if t.Key == "" {
		return tags
	}
	for i := range tags {
		if tags[i].Key == t.Key {
			tags[i] = t
			return tags
		}
	}
	return append(tags, t)
}

// HasTag 判断用户是否含指定 key+value 标签。
func (u *User) HasTag(key, value string) bool {
	for _, t := range u.Tags {
		if t.Key == key && t.Value == value {
			return true
		}
	}
	return false
}

// parseRole 解析角色字符串（空 = 默认普通用户）。
// 阶段3：支持 user / admin（历史兼容）/ agent_admin / super_admin。
// 具体创建权限（谁能创建哪种角色）由 AdminCreateUser 依据调用者角色校验。
func parseRole(role string) (Role, error) {
	r := Role(strings.TrimSpace(role))
	if r == "" {
		return RoleUser, nil
	}
	if !r.Valid() {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的角色（支持 user/agent_admin/super_admin）")
	}
	return r, nil
}
