// guest.go —— 游客身份派生（阶段2·游客模式）。
//
// 游客（未登录）访问智能体聊天页时可正常对话，但会话属主是"合成负整数
// user_id"：由前端本地生成的游客 ID（X-Guest-ID 头）经 FNV-1a 哈希稳定
// 派生，负值空间专供游客使用——
//   - 真实用户是 auth.users.id（BIGSERIAL 正整数），两者天然不冲突，
//     无需在 auth 库建任何"游客用户行"；
//   - 同一游客 ID 在任何网关/服务上派生结果一致，会话归属稳定；
//   - 游客登录后由 MergeGuestSessions 把该命名空间下的会话整体转移给
//     真实账号（"游客会话不保留、登录后合并"），随后前端清除本地游客 ID。
//
// 注意：负值 user_id 只允许出现在会话/审计等 agent 域数据上；一切管理端
// 接口、认证接口必须拒绝游客身份（见 gateway RequireAdmin / 合并接口校验）。
package auth

import (
	"hash/fnv"
	"math"
	"regexp"
)

// guestIDPattern X-Guest-ID 头格式：宽松校验 8~64 位字母/数字/连字符
// （前端用 crypto.randomUUID() 生成，标准 36 位）。
var guestIDPattern = regexp.MustCompile(`^[0-9a-zA-Z-]{8,64}$`)

// IsValidGuestID 校验游客 ID 格式。非法时上游不应透传为游客身份。
func IsValidGuestID(guestID string) bool {
	return guestIDPattern.MatchString(guestID)
}

// GuestUserID 由游客 ID 派生稳定的负整数用户 ID（游客命名空间）。
// 返回值为 63 位正整数取负（恒 < 0）；guestID 非法返回 0。
func GuestUserID(guestID string) int64 {
	if !IsValidGuestID(guestID) {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(guestID))
	return -int64(h.Sum64() & math.MaxInt64)
}

// IsGuestUserID 判断 user_id 是否落在游客命名空间（< 0）。
// 真实用户恒为正；负数只可能来自 GuestUserID 派生。
func IsGuestUserID(userID int64) bool { return userID < 0 }
