package authsvc

import (
	"context"
	"strconv"
	"strings"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// ---------------------------------------------------------------------------
// 用户标签（tags）单元测试：分智能体注册/登录绑定、管理员建用户、标签工具
// ---------------------------------------------------------------------------

func TestRegisterWithAgentID(t *testing.T) {
	svc, _ := newTestService(t)
	u, err := svc.Register(context.Background(), "bob", testPassword, "tutor")
	if err != nil {
		t.Fatalf("Register with agent_id: %v", err)
	}
	if !u.HasTag(tagKeyAgent, "tutor") {
		t.Fatalf("注册后应写入 agent 标签, got %+v", u.Tags)
	}
	if u.Role != RoleUser {
		t.Fatalf("role = %s, want user", u.Role)
	}
}

func TestRegisterInvalidAgentID(t *testing.T) {
	svc, _ := newTestService(t)
	// 非法字符
	_, err := svc.Register(context.Background(), "bob", testPassword, "bad/id;drop")
	if apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("非法 agent_id 应返回 INVALID_ARGUMENT, got %v", err)
	}
	// 超长
	_, err = svc.Register(context.Background(), "bob2", testPassword, "abcdefghijklmnopqrstuvwxyz0123456789-abcdefghijklmnopqrstuvwxyz0123456789")
	if apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超长 agent_id 应返回 INVALID_ARGUMENT, got %v", err)
	}
}

func TestLoginBindsAgentTag(t *testing.T) {
	svc, repo := newTestService(t)
	repo.seedAgent("math")
	// 先通用注册（无 agent 标签）
	u, err := svc.Register(context.Background(), "carol", testPassword, "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.HasTag(tagKeyAgent, "math") {
		t.Fatalf("通用注册不应带 agent 标签, got %+v", u.Tags)
	}
	// 经 math 智能体门户登录 → 补写标签
	_, err = svc.Login(context.Background(), "carol", testPassword, "math")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	// 用领域仓储直接验证标签已落库
	fresh, err := svc.repo.GetUserByUsername(context.Background(), "carol")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if !fresh.HasTag(tagKeyAgent, "math") {
		t.Fatalf("登录后应补写 agent 标签, got %+v", fresh.Tags)
	}
	// 重复登录同一门户：标签去重不膨胀
	_, err = svc.Login(context.Background(), "carol", testPassword, "math")
	if err != nil {
		t.Fatalf("Login again: %v", err)
	}
	fresh2, _ := svc.repo.GetUserByUsername(context.Background(), "carol")
	n := 0
	for _, tg := range fresh2.Tags {
		if tg.Key == tagKeyAgent && tg.Value == "math" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同标签应只保留一条, got %d 条", n)
	}
}

// TestLogin_AdminPortalCrossDomain 阶段3·多租户：管理员经智能体门户登录时必须
// 归属该智能体——跨域登录被拒（防止门户登录自动改绑导致管理员跨租户越权），
// 且超管（无归属）禁止走门户入口。
func TestLogin_AdminPortalCrossDomain(t *testing.T) {
	svc, repo := newTestService(t)
	repo.seedAgent("math")
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	if err := svc.EnsureDefaultAgent(context.Background()); err != nil {
		t.Fatalf("EnsureDefaultAgent: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	rootID := root.ID

	// 播种智能体超管 boss01（绑定 tutor）
	if _, err := svc.AdminCreateUser(context.Background(), rootID, "boss01", testPassword, "agent_admin", "tutor", nil); err != nil {
		t.Fatalf("AdminCreateUser boss01: %v", err)
	}

	// 1. 归属域门户登录 → 放行
	if _, err := svc.Login(context.Background(), "boss01", testPassword, "tutor"); err != nil {
		t.Errorf("归属域门户登录应成功, got %v", err)
	}
	// 2. 跨域门户登录 → PERMISSION_DENIED，且不允许改绑
	var err error
	if _, err = svc.Login(context.Background(), "boss01", testPassword, "math"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Errorf("跨域门户登录应返回 PERMISSION_DENIED, got %q", apperr.CodeOf(err))
	}
	if msg := err.Error(); !strings.Contains(msg, "不归属于智能体 math") {
		t.Errorf("错误信息应明确提示不归属于该智能体, got %q", msg)
	}
	fresh, _ := svc.repo.GetUserByUsername(context.Background(), "boss01")
	if !fresh.HasTag(tagKeyAgent, "tutor") || fresh.HasTag(tagKeyAgent, "math") {
		t.Errorf("登录失败不应改绑, got %+v", fresh.Tags)
	}

	// 3. 最高超管（无归属）经门户登录 → 拒绝，强制走管理员入口
	if _, err := svc.Login(context.Background(), "root", testPassword, "tutor"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Errorf("超管经门户登录应返回 PERMISSION_DENIED, got %q", apperr.CodeOf(err))
	}
}

// TestSuperAdminAllAgentTag 阶段3·统一标签模型：
// 最高超管打 {agent,"*"} 全门户标签（AgentScope 返回 "*"）；超管经自己的
// '*' 门户登录放行、经具体门户拒绝；普通用户不能在 '*' 门户注册/登录；
// 历史超管（无标签）由 EnsureAdmin 幂等补写且不覆盖密码。
func TestSuperAdminAllAgentTag(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")

	// 1. 播种的超管应带全门户标签
	if root.AgentScope() != allAgentID {
		t.Fatalf("超管 AgentScope = %q, want %q", root.AgentScope(), allAgentID)
	}
	// 2. 超管经自己的 '*' 门户登录 → 放行
	if _, err := svc.Login(context.Background(), "root", testPassword, "*"); err != nil {
		t.Errorf("超管经 '*' 门户登录应成功, got %v", err)
	}
	// 3. 超管经具体门户登录 → 拒绝（TestLogin_AdminPortalCrossDomain 亦覆盖 tutor 场景）
	if _, err := svc.Login(context.Background(), "root", testPassword, "tutor"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Errorf("超管经具体门户登录应拒绝, got %q", apperr.CodeOf(err))
	}

	// 4. 普通用户：在 '*' 门户注册 → 拒绝；经 '*' 门户登录 → 拒绝（归属校验）
	if _, err := svc.Register(context.Background(), "bob", testPassword, "*"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Errorf("普通用户注册超管门户应拒绝, got %q", apperr.CodeOf(err))
	}
	if _, err := svc.Register(context.Background(), "bob", testPassword, "tutor"); err != nil {
		t.Fatalf("Register bob@tutor: %v", err)
	}
	if _, err := svc.Login(context.Background(), "bob", testPassword, "*"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Errorf("普通用户经 '*' 门户登录应拒绝, got %q", apperr.CodeOf(err))
	}

	// 5. 历史超管补标签：创建不带标签的 super_admin → EnsureAdmin 幂等补写，不覆盖密码
	if _, err := svc.AdminCreateUser(context.Background(), root.ID, "legacy_root", testPassword, "super_admin", "", nil); err != nil {
		t.Fatalf("创建无标签超管: %v", err)
	}
	legacy, _ := svc.repo.GetUserByUsername(context.Background(), "legacy_root")
	if legacy.AgentScope() != "" {
		t.Fatalf("预置超管不应有标签, got %q", legacy.AgentScope())
	}
	if _, err := svc.EnsureAdmin(context.Background(), "legacy_root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin(legacy_root): %v", err)
	}
	legacy2, _ := svc.repo.GetUserByUsername(context.Background(), "legacy_root")
	if legacy2.AgentScope() != allAgentID {
		t.Fatalf("历史超管应被补写全门户标签, got %q", legacy2.AgentScope())
	}
	if _, err := svc.Login(context.Background(), "legacy_root", testPassword, "*"); err != nil {
		t.Errorf("补标签后超管经 '*' 门户登录应成功, got %v", err)
	}
}

func TestAdminCreateUser(t *testing.T) {
	svc, repo := newTestService(t)
	repo.seedAgent("ops")
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	actorID := root.ID

	u, err := svc.AdminCreateUser(context.Background(), actorID, "admin01", testPassword, "admin", "ops", []Tag{{Key: "plan", Value: "premium"}})
	if err != nil {
		t.Fatalf("AdminCreateUser: %v", err)
	}
	if u.Role != RoleAdmin {
		t.Fatalf("role = %s, want admin", u.Role)
	}
	if !u.HasTag(tagKeyAgent, "ops") || !u.HasTag("plan", "premium") {
		t.Fatalf("应合并 agent + 自定义标签, got %+v", u.Tags)
	}
	if u.PasswordHash != "" {
		t.Fatalf("响应不应携带密码哈希")
	}
	// 非法角色
	if _, err := svc.AdminCreateUser(context.Background(), actorID, "admin02", testPassword, "root", "", nil); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("非法角色应返回 INVALID_ARGUMENT, got %v", err)
	}
	// 非管理员调用者应被拒绝
	alice, _ := svc.Register(context.Background(), "alice", testPassword, "")
	if _, err := svc.AdminCreateUser(context.Background(), alice.ID, "admin03", testPassword, "user", "", nil); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("普通用户创建用户应返回 PERMISSION_DENIED, got %v", err)
	}
}

// TestAdminCreateUser_RoleHierarchy 阶段3·管理员分层：agent_admin 只能在自己
// 智能体组内创建 user/admin；不能创建超管角色，也不能跨智能体组。
func TestAdminCreateUser_RoleHierarchy(t *testing.T) {
	svc, _ := newTestService(t)
	// 播种超管 + 默认智能体 tutor
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	if err := svc.EnsureDefaultAgent(context.Background()); err != nil {
		t.Fatalf("EnsureDefaultAgent: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	rootID := root.ID
	// 严格多租户：建号需域已注册。先建 math 智能体（暂不绑定 owner）。
	if _, err := svc.CreateAgent(context.Background(), rootID, "math", "数学智能体", "", "", "", "", "", "", 0); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	// 创建 math 的智能体超管：owner 用户升级为 agent_admin
	owner, err := svc.AdminCreateUser(context.Background(), rootID, "math_owner", testPassword, "user", "math", nil)
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	ownerID, _ := strconv.ParseInt(owner.ID, 10, 64)
	if _, err := svc.BindAgentOwner(context.Background(), rootID, "math", ownerID); err != nil {
		t.Fatalf("BindAgentOwner: %v", err)
	}
	// owner 现在应是 agent_admin（智能体超管）
	ownerFresh, _ := svc.repo.GetUserByUsername(context.Background(), "math_owner")
	if ownerFresh.Role != RoleAgentAdmin {
		t.Fatalf("owner 角色 = %s, want agent_admin", ownerFresh.Role)
	}

	// 1. agent_admin 在本组内创建普通管理员：放行
	if _, err := svc.AdminCreateUser(context.Background(), owner.ID, "math_admin", testPassword, "admin", "math", nil); err != nil {
		t.Fatalf("agent_admin 创建本组 admin 应放行, got %v", err)
	}
	// 2. agent_admin 创建超管角色：拒绝
	if _, err := svc.AdminCreateUser(context.Background(), owner.ID, "hacker", testPassword, "super_admin", "", nil); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("agent_admin 创建 super_admin 应拒绝, got %v", err)
	}
	// 3. agent_admin 跨组创建（指定其他智能体）：拒绝
	if _, err := svc.AdminCreateUser(context.Background(), owner.ID, "tutor_user", testPassword, "user", "tutor", nil); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("agent_admin 跨组创建应拒绝, got %v", err)
	}
	// 4. 越权传空 agentID 也会被强制归入本组
	if u, err := svc.AdminCreateUser(context.Background(), owner.ID, "math_user", testPassword, "user", "", nil); err != nil {
		t.Fatalf("agent_admin 建本组用户应放行, got %v", err)
	} else if !u.HasTag(tagKeyAgent, "math") {
		t.Fatalf("应强制归入 math 组, got %+v", u.Tags)
	}
}

func TestAdminListUsers(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	actorID := root.ID
	for _, name := range []string{"alice", "alice2", "bob"} {
		if _, err := svc.AdminCreateUser(context.Background(), actorID, name, testPassword, "user", "tutor", nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	users, total, err := svc.AdminListUsers(context.Background(), actorID, "alice", 1, 10)
	if err != nil {
		t.Fatalf("AdminListUsers: %v", err)
	}
	if total != 2 || len(users) != 2 {
		t.Fatalf("keyword=alice 应命中 2 条, got total=%d len=%d", total, len(users))
	}
	for _, u := range users {
		if u.PasswordHash != "" {
			t.Fatalf("列表响应不应携带密码哈希")
		}
	}
	// 分页边界
	if _, total, err := svc.AdminListUsers(context.Background(), actorID, "", 2, 10); err != nil || total != 4 {
		t.Fatalf("全部用户 total = %d, want 4 (err=%v)", total, err)
	}
	// 非超管类角色调用应拒绝
	alice, _ := svc.repo.GetUserByUsername(context.Background(), "alice")
	if _, _, err := svc.AdminListUsers(context.Background(), alice.ID, "", 1, 10); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("普通用户列表应拒绝, got %v", err)
	}
}

// TestAdminListUsers_ScopeFilter 阶段3·管辖过滤：agent_admin 只能看到自己
// 智能体组内的用户；super_admin 看全局。
func TestAdminListUsers_ScopeFilter(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	if err := svc.EnsureDefaultAgent(context.Background()); err != nil {
		t.Fatalf("EnsureDefaultAgent: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	rootID := root.ID
	// math 组超管：先建域再建号再绑定 owner（严格多租户：建号需域已注册）。
	if _, err := svc.CreateAgent(context.Background(), rootID, "math", "数学智能体", "", "", "", "", "", "", 0); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	owner, _ := svc.AdminCreateUser(context.Background(), rootID, "math_owner", testPassword, "user", "math", nil)
	ownerID, _ := strconv.ParseInt(owner.ID, 10, 64)
	if _, err := svc.BindAgentOwner(context.Background(), rootID, "math", ownerID); err != nil {
		t.Fatalf("BindAgentOwner: %v", err)
	}
	// 两个组的普通用户
	if _, err := svc.AdminCreateUser(context.Background(), rootID, "u_tutor", testPassword, "user", "tutor", nil); err != nil {
		t.Fatalf("create u_tutor: %v", err)
	}
	if _, err := svc.AdminCreateUser(context.Background(), rootID, "u_math", testPassword, "user", "math", nil); err != nil {
		t.Fatalf("create u_math: %v", err)
	}

	// agent_admin（math 组）→ 只看到 math 组用户（含自己）
	users, total, err := svc.AdminListUsers(context.Background(), owner.ID, "", 1, 100)
	if err != nil {
		t.Fatalf("agent_admin 列表: %v", err)
	}
	if total != 2 { // u_math + owner 自己（math 标签）
		t.Fatalf("agent_admin 应只看到本组 2 个用户, got %d", total)
	}
	for _, u := range users {
		if u.Username == "u_tutor" || u.Username == "root" {
			t.Fatalf("agent_admin 不应看到其它组/超管用户: %s", u.Username)
		}
	}
}

// TestCreateAgent_AndListAgents 阶段3·智能体管理：仅超管可创建；
// 列表按角色返回可见范围。
func TestCreateAgent_AndListAgents(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	rootID := root.ID
	plain, _ := svc.Register(context.Background(), "plain", testPassword, "")

	// 普通用户创建智能体 → 拒绝
	if _, err := svc.CreateAgent(context.Background(), plain.ID, "hack", "hack", "", "", "", "", "", "", 1); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("普通用户创建智能体应拒绝, got %v", err)
	}
	// 非法 ID
	if _, err := svc.CreateAgent(context.Background(), rootID, "bad/id", "bad", "", "", "", "", "", "", 2); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("非法 ID 应拒绝, got %v", err)
	}
	// 超管创建 math 智能体（先建域，后建用户并绑定 owner → 升级 agent_admin）
	if _, err := svc.CreateAgent(context.Background(), rootID, "math", "数学智能体", "", "", "", "", "", "", 0); err != nil {
		t.Fatalf("超管创建智能体应成功, got %v", err)
	}
	owner, err := svc.AdminCreateUser(context.Background(), rootID, "math_owner", testPassword, "user", "math", nil)
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	ownerID, _ := strconv.ParseInt(owner.ID, 10, 64)
	if _, err := svc.BindAgentOwner(context.Background(), rootID, "math", ownerID); err != nil {
		t.Fatalf("BindAgentOwner: %v", err)
	}
	// owner 被授予 agent_admin
	ownerFresh, _ := svc.repo.GetUserByUsername(context.Background(), "math_owner")
	if ownerFresh.Role != RoleAgentAdmin {
		t.Fatalf("owner 角色 = %s, want agent_admin", ownerFresh.Role)
	}

	// 超管列表：全部（newTestService 预播种 test/tutor，加上 math = 3 个）
	agents, err := svc.ListAgents(context.Background(), rootID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("超管应看到 3 个智能体, got %d", len(agents))
	}
	// 其它角色：只能看到自己归属
	member, _ := svc.AdminCreateUser(context.Background(), rootID, "math_member", testPassword, "admin", "math", nil)
	myAgents, err := svc.ListAgents(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("ListAgents(agent): %v", err)
	}
	if len(myAgents) != 1 || myAgents[0].ID != "math" {
		t.Fatalf("普通管理员应只看到自己的智能体, got %+v", myAgents)
	}
	// 无归属用户：看不到任何智能体
	none, err := svc.ListAgents(context.Background(), plain.ID)
	if err != nil {
		t.Fatalf("ListAgents(plain): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("无归属用户不应看到任何智能体, got %+v", none)
	}
}

// ---------------------------------------------------------------------------
// 标签工具函数
// ---------------------------------------------------------------------------

func TestMergeTags(t *testing.T) {
	in := []Tag{{Key: "agent", Value: "tutor"}, {Key: "plan", Value: "basic"}}
	out := mergeTags(in, []Tag{{Key: "plan", Value: "premium"}})
	got := map[string]string{}
	for _, tg := range out {
		got[tg.Key] = tg.Value
	}
	if got["agent"] != "tutor" || got["plan"] != "premium" {
		t.Fatalf("mergeTags 去重覆盖失败: %+v", out)
	}
	// 空 key 忽略
	out = mergeTags([]Tag{{Key: "", Value: "x"}}, nil)
	if len(out) != 0 {
		t.Fatalf("空 key 应被忽略, got %+v", out)
	}
}

func TestValidateAgentID(t *testing.T) {
	for _, ok := range []string{"", "tutor", "math-01", "A1"} {
		if err := validateAgentID(ok); err != nil {
			t.Fatalf("validateAgentID(%q) 应通过, got %v", ok, err)
		}
	}
	for _, bad := range []string{"a/b", "a b", "智能体", "a..b"} {
		if err := validateAgentID(bad); err == nil {
			t.Fatalf("validateAgentID(%q) 应拒绝", bad)
		}
	}
}
