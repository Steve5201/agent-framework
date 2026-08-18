package authsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-backend/internal/errors"
)

// Config auth-service 依赖注入与安全策略配置。
type Config struct {
	Repo       Repository    // 持久化（PostgreSQL 实现或测试 fake）
	JWT        *auth.Manager // JWT 双令牌签发/校验
	AccessTTL  time.Duration // access 令牌有效期（LoginResponse.expires_in 来源）
	RefreshTTL time.Duration // refresh 令牌有效期（入库用）

	// 登录失败限流策略（P2-26）。
	MaxLoginAttempts int           // 阈值：连续失败达到该次数后锁定
	LoginLockWindow  time.Duration // 锁定时长
}

// LoginResult 登录/刷新成功后的令牌结果。
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64 // access 有效期（秒）
	User         *User
}

// Service 认证业务逻辑（Register/Login/Refresh/Logout/Me）。
type Service struct {
	repo       Repository
	jwt        *auth.Manager
	accessTTL  time.Duration
	refreshTTL time.Duration
	throttle   *loginThrottle
	now        func() time.Time // 可注入时钟，便于单测
}

// NewService 创建认证服务。
func NewService(cfg Config) *Service {
	if cfg.MaxLoginAttempts <= 0 {
		cfg.MaxLoginAttempts = 5
	}
	if cfg.LoginLockWindow <= 0 {
		cfg.LoginLockWindow = 5 * time.Minute
	}
	return &Service{
		repo:       cfg.Repo,
		jwt:        cfg.JWT,
		accessTTL:  cfg.AccessTTL,
		refreshTTL: cfg.RefreshTTL,
		throttle:   newLoginThrottle(cfg.MaxLoginAttempts, cfg.LoginLockWindow),
		now:        time.Now,
	}
}

// ---------------------------------------------------------------------------
// Register
// ---------------------------------------------------------------------------

// Register 注册新用户：
//  1. 校验用户名/密码强度（P2-26 密码强度）；
//  2. bcrypt 哈希密码；
//  3. 入库（用户名冲突映射为 CodeAlreadyExists）。
//
// agentID 非空时（/v1/auth/register/{agent_id} 入口）写入 agent 标签。
// 超管全门户标识 '*'（agentID == allAgentID）不允许注册——它是超管专属门户。
func (s *Service) Register(ctx context.Context, username, password, agentID string) (*User, error) {
	username = normalizeUsername(username)
	if err := validateCredentials(username, password); err != nil {
		return nil, err
	}
	if err := validatePortalID(agentID); err != nil {
		return nil, err
	}
	if agentID == allAgentID {
		return nil, errors.New(errors.CodePermissionDenied, "超管门户不支持注册")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u, err := s.repo.CreateUser(ctx, username, hash, RoleUser, agentTags(agentID))
	if err != nil {
		return nil, err // 唯一性冲突已在 repository 映射为 CodeAlreadyExists
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

// Login 登录：
//  1. 登录失败限流检查（P2-26）；
//  2. 校验账号与密码（错误信息统一为"用户名或密码错误"，不泄露账号是否存在）；
//  3. 签发 access + refresh（新 family_id），refresh 入库（P2-21）；
//  4. agentID 非空且用户尚无该标签时补写（首次经某智能体门户登录即绑定该智能体）。
func (s *Service) Login(ctx context.Context, username, password, agentID string) (*LoginResult, error) {
	username = normalizeUsername(username)
	if err := validatePortalID(agentID); err != nil {
		return nil, err
	}
	now := s.now()

	// 1. 限流：锁定期间直接拒绝
	if ok, remain := s.throttle.allow(username, now); !ok {
		return nil, errors.New(errors.CodeResourceExhausted,
			fmt.Sprintf("登录失败次数过多，请在 %d 秒后重试", int(remain.Seconds())+1))
	}

	// 2. 校验凭据
	u, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.CodeOf(err) == errors.CodeNotFound {
			s.throttle.recordFailure(username, now)
			return nil, errors.New(errors.CodeUnauthenticated, "用户名或密码错误")
		}
		return nil, err
	}
	if !u.Active() {
		return nil, errors.New(errors.CodePermissionDenied, "账号已被禁用")
	}
	if err := auth.CheckPassword(u.PasswordHash, password); err != nil {
		s.throttle.recordFailure(username, now)
		return nil, errors.New(errors.CodeUnauthenticated, "用户名或密码错误")
	}

	// 管理员入口（无 agent_id 的 /v1/auth/login）只允许管理员类账号登录：
	// 普通账号应在对应智能体门户（/v1/auth/login/{agent_id}）登录。
	// 防止"普通用户经管理端入口登录成功"的越权尝试与角色混淆。
	if agentID == "" && !u.Role.IsAdmin() {
		return nil, errors.New(errors.CodePermissionDenied, "该入口仅限管理员登录，请使用对应的智能体门户入口")
	}

	// 管理员账号经智能体门户登录时必须归属该智能体（阶段3·多租户）：
	//   - agent_admin / admin：AgentScope 必须与 agentID 一致，防止跨域登录
	//     （下方自动绑定会把旧 agent 标签覆盖掉，等于让管理员自己改绑到别的
	//     智能体，构成租户越权）；
	//   - super_admin：AgentScope == '*'（全门户标识），仅经自己的超管门户
	//     '/login/*' 放行；经具体智能体门户会被拒绝，保证身份入口标准化。
	if agentID != "" && u.Role.IsAdmin() {
		if u.AgentScope() != agentID {
			return nil, errors.New(errors.CodePermissionDenied,
				"该账号不归属于智能体 "+agentID+"，请核对智能体 ID 或改用管理员入口")
		}
	}

	// 超管门户（'*'）只允许全门户归属者进入：普通用户/普通管理员/无归属账号
	// 经 '*' 门户登录在此被拒，且不会触发下方"首次登录自动绑定标签"逻辑把
	// agent 标签改绑为 '*'（避免越权升级为全门户身份）。
	if agentID == allAgentID && u.AgentScope() != allAgentID {
		return nil, errors.New(errors.CodePermissionDenied, "超管门户仅限最高超管登录")
	}

	// 3. 成功：清零计数并签发双令牌
	s.throttle.reset(username)

	// 4. 分智能体门户首次登录绑定 agent 标签。
	//    仅普通用户自动绑定（首次经某门户登录即归属该智能体）；
	//    管理员归属由管理端创建时指定，禁止经登录入口改绑（见上方校验）。
	if agentID != "" && !u.Role.IsAdmin() && !u.HasTag(tagKeyAgent, agentID) {
		if err := s.repo.AddUserTag(ctx, u.ID, Tag{Key: tagKeyAgent, Value: agentID}); err != nil {
			return nil, err
		}
		u.Tags = upsertTag(u.Tags, Tag{Key: tagKeyAgent, Value: agentID})
	}

	access, _, err := s.jwt.SignAccess(u.ID, string(u.Role), u.AgentScope())
	if err != nil {
		return nil, err
	}
	familyID := newUUID()
	refresh, refreshExp, err := s.jwt.SignRefresh(u.ID, familyID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.CreateRefreshToken(ctx, mustUserID(u.ID), familyID, tokenHash(refresh), refreshExp); err != nil {
		return nil, err
	}

	return &LoginResult{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
		User:         u,
	}, nil
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

// Refresh 用 refresh token 换取新双令牌（P2-22）：
//   - 验签 + 校验令牌类型（refresh）；
//   - 查库确认该令牌存在、未吊销、未过期（单次使用前提）；
//   - 检测到"已被吊销的令牌再次使用"→ 判定为令牌重放，吊销整个族；
//   - 正常流程：吊销旧令牌 → 签发新 access + 新 refresh（同族）→ 新 refresh 入库。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*LoginResult, error) {
	if refreshToken == "" {
		return nil, errors.New(errors.CodeInvalidArgument, "refresh token 不能为空")
	}

	claims, err := s.jwt.Verify(refreshToken, auth.TokenTypeRefresh)
	if err != nil {
		return nil, err // Verify 已映射为 CodeUnauthenticated
	}

	rec, err := s.repo.GetRefreshTokenByHash(ctx, tokenHash(refreshToken))
	if err != nil {
		return nil, err // 不存在 → CodeUnauthenticated
	}

	// 令牌已吊销却被再次使用：疑似泄露，整族吊销（安全事件）。
	if rec.RevokedAt != nil {
		_ = s.repo.RevokeFamily(ctx, rec.FamilyID)
		return nil, errors.New(errors.CodeUnauthenticated, "refresh token 已失效，请重新登录")
	}
	if s.now().After(rec.ExpiresAt) {
		return nil, errors.New(errors.CodeUnauthenticated, "refresh token 已过期，请重新登录")
	}
	// 令牌声明与库记录一致性校验（防御库/令牌被篡改）。
	if claims.UserID != fmt.Sprint(rec.UserID) {
		return nil, errors.New(errors.CodeUnauthenticated, "refresh token 无效")
	}

	// 旧令牌单次使用：先吊销，再签发。
	if err := s.repo.RevokeRefreshToken(ctx, rec.ID); err != nil {
		return nil, err
	}
	return s.issueTokens(ctx, fmt.Sprint(rec.UserID), rec.FamilyID)
}

// ---------------------------------------------------------------------------
// Logout
// ---------------------------------------------------------------------------

// Logout 吊销 refresh token 所在的整个令牌族（全设备下线，P2-23）。
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return errors.New(errors.CodeInvalidArgument, "refresh token 不能为空")
	}
	claims, err := s.jwt.Verify(refreshToken, auth.TokenTypeRefresh)
	if err != nil {
		return err
	}
	return s.repo.RevokeFamily(ctx, claims.FamilyID)
}

// ---------------------------------------------------------------------------
// Me
// ---------------------------------------------------------------------------

// Me 返回用户资料。userID 来自 gRPC metadata（gateway 解析 JWT 注入），
// 服务端不信任客户端入参。
func (s *Service) Me(ctx context.Context, userID string) (*User, error) {
	if userID == "" {
		return nil, errors.New(errors.CodeUnauthenticated, "缺少用户身份")
	}
	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	// 最小暴露：用户资料只读接口不携带密码哈希（gRPC 层 toProtoUser 亦剥离）。
	u.PasswordHash = ""
	return u, nil
}

// ---------------------------------------------------------------------------
// 初始超级管理员（EnsureAdmin）
// ---------------------------------------------------------------------------

// EnsureAdmin 确保初始最高超管存在：
//   - 已存在（同名用户）→ 不覆盖密码、不改变角色；若该超管尚未打全门户标签
//     （历史数据），幂等补写 {agent, "*"}（统一标签模型），返回 created=false；
//   - 不存在 → 用 password 创建 RoleSuperAdmin 用户，并打 {agent, "*"} 标签
//     （全门户标识，含义 = 全部智能体），返回 created=true。
//
// 用于服务启动时播种初始管理员（AUTH_ADMIN_USERNAME/AUTH_ADMIN_PASSWORD）。
// 注意：仅用于初始化，已有账号的密码/角色不会被动修改（防止重启后重置密码）。
func (s *Service) EnsureAdmin(ctx context.Context, username, password string) (bool, error) {
	username = normalizeUsername(username)
	if username == "" {
		return false, errors.New(errors.CodeInvalidArgument, "管理员用户名不能为空")
	}
	// 已存在：跳过（不修改现有账号的角色/密码）。
	if existing, err := s.repo.GetUserByUsername(ctx, username); err == nil {
		// 历史超管（播种于标签模型之前）补写全门户标签，保证统一处理流程。
		if existing.Role == RoleSuperAdmin && !existing.HasTag(tagKeyAgent, allAgentID) {
			if err := s.repo.AddUserTag(ctx, existing.ID, Tag{Key: tagKeyAgent, Value: allAgentID}); err != nil {
				return false, err
			}
		}
		return false, nil
	} else if errors.CodeOf(err) != errors.CodeNotFound {
		return false, err
	}

	if err := validateCredentials(username, password); err != nil {
		return false, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}
	// 最高超管打全门户标签：与其它身份的 agent 标签格式一致，仅值不同（"*"）。
	if _, err := s.repo.CreateUser(ctx, username, hash, RoleSuperAdmin,
		[]Tag{{Key: tagKeyAgent, Value: allAgentID}}); err != nil {
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Admin 用户管理（管理员专用：只能被创建，不能自助注册）
// ---------------------------------------------------------------------------

// AdminCreateUser 管理员创建用户（阶段3·分层权限）：
//   - actorID（调用者，来自 gRPC metadata 的 x-user-id）必须是超管类角色；
//   - super_admin：可创建任意角色（user/admin/agent_admin/super_admin），agentID 任意；
//   - agent_admin：只能在自己智能体组内创建 user/admin，agentID 强制为自身归属；
//   - admin/user：无用户管理权（网关层已拦截，此处双保险）。
func (s *Service) AdminCreateUser(ctx context.Context, actorID, username, password, role string, agentID string, tags []Tag) (*User, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.IsAdmin() {
		return nil, errors.New(errors.CodePermissionDenied, "需要管理员权限")
	}

	username = normalizeUsername(username)
	if err := validateCredentials(username, password); err != nil {
		return nil, err
	}
	r, err := parseRole(role)
	if err != nil {
		return nil, err
	}
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}

	// 分层约束：非最高超管不能创建超管类角色，且只能在本智能体组内创建用户。
	if actor.Role != RoleSuperAdmin {
		if r == RoleSuperAdmin || r == RoleAgentAdmin {
			return nil, errors.New(errors.CodePermissionDenied, "仅最高超管可创建超管账号")
		}
		scope := actor.AgentScope()
		if scope == "" {
			return nil, errors.New(errors.CodePermissionDenied, "当前账号未绑定智能体，无法创建用户")
		}
		if agentID != "" && agentID != scope {
			return nil, errors.New(errors.CodePermissionDenied, "只能在本智能体组内创建用户")
		}
		agentID = scope // 强制归入调用者的智能体
	}

	merged := mergeTags(agentTags(agentID), tags)
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u, err := s.repo.CreateUser(ctx, username, hash, r, merged)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = "" // 响应绝不携带哈希
	return u, nil
}

// AdminListUsers 管理员分页查询用户（含标签）。按管辖范围过滤：
// super_admin 看全局；agent_admin 仅看自己智能体组内的用户。
func (s *Service) AdminListUsers(ctx context.Context, actorID, keyword string, page, pageSize int) ([]*User, int, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, 0, err
	}
	if !actor.Role.CanManageUsers() {
		return nil, 0, errors.New(errors.CodePermissionDenied, "需要超管权限才能管理用户")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	scope := ""
	if actor.Role != RoleSuperAdmin {
		scope = actor.AgentScope()
		if scope == "" {
			return nil, 0, errors.New(errors.CodePermissionDenied, "当前账号未绑定智能体，无法管理用户")
		}
	}
	users, total, err := s.repo.ListUsers(ctx, keyword, scope, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	// 最小暴露：剥离密码哈希。
	for _, u := range users {
		u.PasswordHash = ""
	}
	return users, total, nil
}

// ChangePassword 用户自助修改密码：
//   - 校验旧密码（不匹配返回 Unauthenticated，统一提示不泄露账号状态）；
//   - 新密码强度校验（>=8 位，含字母与数字）；
//   - 更新密码哈希 + 吊销该用户全部 refresh token（安全惯例：改密即强制下线）。
func (s *Service) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string) error {
	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := auth.CheckPassword(u.PasswordHash, oldPassword); err != nil {
		return errors.New(errors.CodeUnauthenticated, "原密码不正确")
	}
	if !validPassword(newPassword) {
		return errors.New(errors.CodeInvalidArgument,
			"密码须不少于 8 位，且同时包含字母与数字")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.repo.UpdateUserPassword(ctx, mustUserID(u.ID), hash); err != nil {
		return err
	}
	// 改密即安全事件：吊销全部 refresh token，防已泄露会话继续有效。
	// 吊销失败不阻断主流程（密码已改，旧 refresh 下一次轮换即被拒）。
	if err := s.revokeUserTokens(ctx, u.ID); err != nil {
		_ = err
	}
	return nil
}

// AdminUpdateUser 管理员重置用户密码（"编辑用户"的唯一能力）。
// 校验（防横向越权，用户明确要求"当前账号权限大于被重置账号权限才能重置"）：
//   - 调用者必须具备用户管理权（super_admin / agent_admin）；
//   - 调用者角色等级必须严格高于被重置账号（平级不可重置）；
//   - 新密码须满足强度要求（>=8 位，含字母与数字）。
//
// 重置成功后吊销该用户全部 refresh token，强制重新登录（旧令牌立即失效）。
func (s *Service) AdminUpdateUser(ctx context.Context, actorID, targetUserID, newPassword string) (*User, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanManageUsers() {
		return nil, errors.New(errors.CodePermissionDenied, "需要超管权限才能管理用户")
	}
	target, err := s.repo.GetUserByID(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanManageUser(target.Role) {
		return nil, errors.New(errors.CodePermissionDenied,
			"无权重置该账号密码：只能操作权限低于自己的账号")
	}
	if !validPassword(newPassword) {
		return nil, errors.New(errors.CodeInvalidArgument,
			"密码须不少于 8 位，且同时包含字母与数字")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateUserPassword(ctx, mustUserID(target.ID), hash); err != nil {
		return nil, err
	}
	// 强制下线：吊销该用户全部 refresh token（重置密码通常是安全事件，
	// 旧令牌必须立即失效，防已泄露会话继续有效）。
	// 吊销失败不阻断主流程：密码已改，旧 refresh 下一次轮换即被拒。
	if err := s.revokeUserTokens(ctx, target.ID); err != nil {
		_ = err // 仅尽力而为，失败静默（无日志器注入，保持 service 纯净）
	}
	target.PasswordHash = "" // 响应绝不携带哈希
	return target, nil
}

// AdminDeleteUser 管理员删除用户（硬删，refresh_tokens 级联清理）。
// 校验：
//   - 调用者必须具备用户管理权；
//   - 调用者角色等级必须严格高于被删除账号；
//   - 禁止删除自己；禁止删除最后一名最高超管（避免系统失管）。
func (s *Service) AdminDeleteUser(ctx context.Context, actorID, targetUserID string) error {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return err
	}
	if !actor.Role.CanManageUsers() {
		return errors.New(errors.CodePermissionDenied, "需要超管权限才能管理用户")
	}
	if actorID == targetUserID {
		return errors.New(errors.CodeInvalidArgument, "不能删除当前登录账号")
	}
	target, err := s.repo.GetUserByID(ctx, targetUserID)
	if err != nil {
		return err
	}
	if !actor.Role.CanManageUser(target.Role) {
		return errors.New(errors.CodePermissionDenied,
			"无权删除该账号：只能操作权限低于自己的账号")
	}
	if target.Role == RoleSuperAdmin {
		n, err := s.repo.CountRole(ctx, RoleSuperAdmin)
		if err != nil {
			return err
		}
		if n <= 1 {
			return errors.New(errors.CodeInvalidArgument, "不能删除最后一名最高超管")
		}
	}
	// owner 已可空：先清空该用户担任 owner 的智能体（防悬空引用），再删用户。
	if err := s.repo.ClearAgentsOwner(ctx, mustUserID(target.ID)); err != nil {
		return err
	}
	return s.repo.DeleteUser(ctx, mustUserID(target.ID))
}

// maxUsersByIDs AdminGetUsersByIds 单次批量查询上限（防放大查询）。
const maxUsersByIDs = 100

// AdminGetUsersByIds 按 ID 批量查询用户（数据管理模块 Top 用户用户名回填）。
// 权限与管辖范围语义同 AdminListUsers：super_admin 全局；agent_admin 仅本组；
// 只返回存在的用户（不存在的静默跳过），顺序与请求一致；去重后超过 100 个拒绝。
func (s *Service) AdminGetUsersByIds(ctx context.Context, actorID string, ids []int64) ([]*User, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanManageUsers() {
		return nil, errors.New(errors.CodePermissionDenied, "需要超管权限才能管理用户")
	}
	uniq := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return []*User{}, nil
	}
	if len(uniq) > maxUsersByIDs {
		return nil, errors.New(errors.CodeInvalidArgument, "一次最多查询 100 个用户")
	}
	scope := ""
	if actor.Role != RoleSuperAdmin {
		scope = actor.AgentScope()
		if scope == "" {
			return nil, errors.New(errors.CodePermissionDenied, "当前账号未绑定智能体，无法管理用户")
		}
	}
	users, err := s.repo.ListUsersByIDs(ctx, uniq, scope)
	if err != nil {
		return nil, err
	}
	// 最小暴露：剥离密码哈希。
	for _, u := range users {
		u.PasswordHash = ""
	}
	return users, nil
}

// revokeUserTokens 吊销某用户全部 refresh token（独立超时，失败仅记日志）。
func (s *Service) revokeUserTokens(ctx context.Context, userID string) error {
	uid, err := parseUserID(userID)
	if err != nil {
		return err
	}
	return s.repo.RevokeFamilyByUser(ctx, uid)
}

// ---------------------------------------------------------------------------
// Agent 智能体管理（阶段3·多租户）
// ---------------------------------------------------------------------------

// EnsureDefaultAgent 播种默认智能体 tutor（owner = 首个最高超管），幂等。
// 保证 /agent/tutor 等默认智能体域在注册表中有记录，供资源归属使用。
func (s *Service) EnsureDefaultAgent(ctx context.Context) error {
	su, err := s.repo.GetFirstSuperAdmin(ctx)
	if err != nil {
		return err
	}
	return s.repo.EnsureDefaultAgent(ctx, mustUserID(su.ID))
}

// CreateAgent 创建智能体（仅最高超管）：
//   - 校验参数（id 白名单、名称非空、推理强度合法）；
//   - owner_user_id 可选：为空则暂不绑定（稍后经 BindAgentOwner 绑定）；
//   - 绑定 owner 时将该用户授予 agent_admin 角色并绑定智能体标签，
//     使其成为该智能体的超管（管理员组负责人）。
func (s *Service) CreateAgent(ctx context.Context, actorID, id, name, description, model, avatar, welcome, systemPrompt, reasoningEffort string, ownerUserID int64) (*Agent, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanCreateAgent() {
		return nil, errors.New(errors.CodePermissionDenied, "仅最高超管可创建智能体")
	}
	if err := validateAgentID(id); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New(errors.CodeInvalidArgument, "智能体名称不能为空")
	}
	if err := validateReasoningEffort(reasoningEffort); err != nil {
		return nil, err
	}
	if ownerUserID > 0 {
		owner, err := s.repo.GetUserByID(ctx, fmt.Sprint(ownerUserID))
		if err != nil {
			return nil, errors.New(errors.CodeInvalidArgument, "指定的智能体超管用户不存在")
		}
		if owner.Role == RoleSuperAdmin {
			return nil, errors.New(errors.CodeInvalidArgument, "最高超管不能作为智能体 owner（会失去最高权限）")
		}
		// 授予 owner 智能体超管角色 + 绑定智能体标签（幂等覆盖）。
		if err := s.repo.UpdateUserRole(ctx, ownerUserID, RoleAgentAdmin); err != nil {
			return nil, err
		}
		if err := s.repo.AddUserTag(ctx, fmt.Sprint(ownerUserID), Tag{Key: tagKeyAgent, Value: id}); err != nil {
			return nil, err
		}
	}
	return s.repo.CreateAgent(ctx, &Agent{
		ID: id, Name: name, Description: description, Model: model, OwnerUserID: ownerUserID,
		Avatar: avatar, Welcome: welcome, SystemPrompt: systemPrompt, ReasoningEffort: reasoningEffort,
	})
}

// BindAgentOwner 绑定/更换/解绑智能体超管（仅最高超管）：
//   - ownerUserID<=0：仅解绑当前 owner（回收其 agent_admin 角色与 agent 标签）；
//   - ownerUserID>0：校验新用户存在且非最高超管；若当前 owner 不同则先回收，
//     再授予新 owner agent_admin 角色与 agent 标签。
//
// 解耦"创建智能体必须已有用户、创建用户必须已有域"的鸡生蛋问题。
func (s *Service) BindAgentOwner(ctx context.Context, actorID, id string, ownerUserID int64) (*Agent, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanCreateAgent() {
		return nil, errors.New(errors.CodePermissionDenied, "仅最高超管可绑定智能体超管")
	}
	if err := validateAgentID(id); err != nil {
		return nil, err
	}
	cur, err := s.repo.GetAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	if ownerUserID <= 0 {
		// 解绑：回收当前 owner。
		if cur.OwnerUserID > 0 {
			if err := s.revokeAgentOwner(ctx, cur.OwnerUserID, id); err != nil {
				return nil, err
			}
		}
		return s.repo.UpdateAgentOwner(ctx, id, 0)
	}
	newOwner, err := s.repo.GetUserByID(ctx, fmt.Sprint(ownerUserID))
	if err != nil {
		return nil, errors.New(errors.CodeInvalidArgument, "指定的智能体超管用户不存在")
	}
	if newOwner.Role == RoleSuperAdmin {
		return nil, errors.New(errors.CodeInvalidArgument, "最高超管不能作为智能体 owner（会失去最高权限）")
	}
	// 更换 owner：先回收旧 owner（若存在且不同）。
	if cur.OwnerUserID > 0 && cur.OwnerUserID != ownerUserID {
		if err := s.revokeAgentOwner(ctx, cur.OwnerUserID, id); err != nil {
			return nil, err
		}
	}
	// 授予新 owner 智能体超管角色 + 绑定智能体标签（幂等覆盖）。
	if err := s.repo.UpdateUserRole(ctx, ownerUserID, RoleAgentAdmin); err != nil {
		return nil, err
	}
	if err := s.repo.AddUserTag(ctx, fmt.Sprint(ownerUserID), Tag{Key: tagKeyAgent, Value: id}); err != nil {
		return nil, err
	}
	return s.repo.UpdateAgentOwner(ctx, id, ownerUserID)
}

// revokeAgentOwner 回收某用户对该智能体的 owner 权限：若其角色仍为
// agent_admin 则降为普通用户（防误伤已被提升为最高超管的账号），
// 并移除指向该智能体的 agent 标签。
func (s *Service) revokeAgentOwner(ctx context.Context, ownerUserID int64, agentID string) error {
	uid := fmt.Sprint(ownerUserID)
	owner, err := s.repo.GetUserByID(ctx, uid)
	if err != nil {
		return err
	}
	if owner.Role == RoleAgentAdmin {
		if err := s.repo.UpdateUserRole(ctx, ownerUserID, RoleUser); err != nil {
			return err
		}
	}
	if owner.HasTag(tagKeyAgent, agentID) {
		if err := s.repo.RemoveUserTag(ctx, uid, tagKeyAgent); err != nil {
			return err
		}
	}
	return nil
}

// ListAgents 列出智能体：最高超管看全部；其它管理员/用户只看自己归属的智能体。
// 列表含停用项（管理端需可见以便重新启用），可用性过滤由调用方完成。
func (s *Service) ListAgents(ctx context.Context, actorID string) ([]*Agent, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	agents, err := s.repo.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	if actor.Role == RoleSuperAdmin {
		return agents, nil
	}
	scope := actor.AgentScope()
	out := agents[:0]
	for _, a := range agents {
		if a.ID == scope {
			out = append(out, a)
		}
	}
	return out, nil
}

// GetAgent 智能体详情：super_admin 任意；agent_admin 仅限自身归属域。
func (s *Service) GetAgent(ctx context.Context, actorID, id string) (*Agent, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanManageAgent(id, actor.AgentScope()) {
		return nil, errors.New(errors.CodePermissionDenied, "无权查看该智能体")
	}
	return s.repo.GetAgent(ctx, id)
}

// GetAgentPublic 公开智能体元数据：任意登录用户可查（白名单字段，不含
// owner/status 等管理信息）。gateway 创建会话时据此注入按智能体 system_prompt
// ——普通用户无需管理权限即可获取，与门户登录归属语义一致。
func (s *Service) GetAgentPublic(ctx context.Context, actorID, id string) (*Agent, error) {
	// 游客（负 user_id 命名空间）无 users 记录，跳过存在性校验；只返回
	// 白名单公开元数据，不涉及任何私有信息。
	if uid, err := strconv.ParseInt(actorID, 10, 64); err == nil && uid > 0 {
		if _, err := s.repo.GetUserByID(ctx, actorID); err != nil {
			return nil, err
		}
	}
	a, err := s.repo.GetAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Agent{
		ID:              a.ID,
		Name:            a.Name,
		Description:     a.Description,
		Avatar:          a.Avatar,
		Welcome:         a.Welcome,
		SystemPrompt:    a.SystemPrompt,
		ReasoningEffort: a.ReasoningEffort,
	}, nil
}

// UpdateAgent 更新智能体元数据：super_admin 任意；agent_admin 仅限自身归属域。
// 全量覆盖指定字段（name 非空；description/model/avatar/welcome/system_prompt 空=清空；
// reasoning_effort 空=实例默认，非空须为 low/high/max）。
func (s *Service) UpdateAgent(ctx context.Context, actorID string, fields Agent) (*Agent, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanManageAgent(fields.ID, actor.AgentScope()) {
		return nil, errors.New(errors.CodePermissionDenied, "无权编辑该智能体")
	}
	if strings.TrimSpace(fields.Name) == "" {
		return nil, errors.New(errors.CodeInvalidArgument, "智能体名称不能为空")
	}
	if err := validateReasoningEffort(fields.ReasoningEffort); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetAgent(ctx, fields.ID); err != nil {
		return nil, err
	}
	return s.repo.UpdateAgent(ctx, &fields)
}

// SetAgentStatus 启停智能体（仅最高超管；status=0 后该域禁止创建会话）。
func (s *Service) SetAgentStatus(ctx context.Context, actorID, id string, status int) (*Agent, error) {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !actor.Role.CanSetAgentStatus() {
		return nil, errors.New(errors.CodePermissionDenied, "仅最高超管可启停智能体")
	}
	if status != 0 && status != 1 {
		return nil, errors.New(errors.CodeInvalidArgument, "status 仅支持 0=停用 / 1=启用")
	}
	if _, err := s.repo.GetAgent(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.SetAgentStatus(ctx, id, status)
}

// DeleteAgent 软删除智能体（仅最高超管；标记停用并保留记录）。
// 系统内置 tutor 禁止删除。
func (s *Service) DeleteAgent(ctx context.Context, actorID, id string) error {
	actor, err := s.repo.GetUserByID(ctx, actorID)
	if err != nil {
		return err
	}
	if !actor.Role.CanSetAgentStatus() {
		return errors.New(errors.CodePermissionDenied, "仅最高超管可删除智能体")
	}
	if id == "tutor" {
		return errors.New(errors.CodeInvalidArgument, "系统内置智能体不可删除")
	}
	if _, err := s.repo.GetAgent(ctx, id); err != nil {
		return err
	}
	_, err = s.repo.DeleteAgent(ctx, id)
	return err
}

// validateReasoningEffort 校验默认推理强度（空 = 实例默认）。
func validateReasoningEffort(e string) error {
	switch e {
	case "", "low", "high", "max":
		return nil
	}
	return errors.New(errors.CodeInvalidArgument, "reasoning_effort 仅支持 low/high/max")
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// issueTokens 签发新双令牌并记录新 refresh（同族轮换）。
func (s *Service) issueTokens(ctx context.Context, userID, familyID string) (*LoginResult, error) {
	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	access, _, err := s.jwt.SignAccess(u.ID, string(u.Role), u.AgentScope())
	if err != nil {
		return nil, err
	}
	refresh, refreshExp, err := s.jwt.SignRefresh(u.ID, familyID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.CreateRefreshToken(ctx, mustUserID(u.ID), familyID, tokenHash(refresh), refreshExp); err != nil {
		return nil, err
	}
	return &LoginResult{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
		User:         u,
	}, nil
}

// tokenHash 计算 refresh token 的 SHA-256 摘要（DB 不落明文）。
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newUUID 生成标准格式的 UUID v4（无外部依赖）。
// 16 字节随机数 + 设置 version(4) 与 variant(10) 位。
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败的概率极低；退回时间戳方案避免阻塞业务。
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// mustUserID 将领域层 string ID 解析为 int64（内部调用方保证合法）。
func mustUserID(id string) int64 {
	n, _ := parseUserID(id)
	return n
}
