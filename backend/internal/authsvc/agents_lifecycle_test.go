package authsvc

import (
	"context"
	"strconv"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// seedAgents 测试辅助：root 超管 + math 智能体（owner 绑定为 agent_admin）。
// 返回 rootID 与 ownerUserID（int64）。
func seedAgents(t *testing.T, svc *Service) (string, int64) {
	t.Helper()
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	// 严格多租户：建号需域已注册。先建智能体（暂不绑定 owner），再建组内用户，
	// 最后绑定 owner（升级为 agent_admin）——解耦"建号需域、建域可无用户"。
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	if _, err := svc.CreateAgent(context.Background(), root.ID, "math", "数学智能体", "desc", "model-x", "🦉", "你好", "你是数学助手", "high", 0); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	owner, err := svc.AdminCreateUser(context.Background(), root.ID, "math_owner", testPassword, "user", "math", nil)
	if err != nil {
		t.Fatalf("AdminCreateUser: %v", err)
	}
	ownerID, err := strconv.ParseInt(owner.ID, 10, 64)
	if err != nil {
		t.Fatalf("ParseInt(owner.ID): %v", err)
	}
	if _, err := svc.BindAgentOwner(context.Background(), root.ID, "math", ownerID); err != nil {
		t.Fatalf("BindAgentOwner: %v", err)
	}
	return root.ID, ownerID
}

// ---------------------------------------------------------------------------
// GetAgent（P2-AI）：super_admin 任意 / agent_admin 仅自身域
// ---------------------------------------------------------------------------

func TestGetAgent_RBAC(t *testing.T) {
	svc, _ := newTestService(t)
	rootID, ownerID := seedAgents(t, svc)

	// 超管任意域可见
	a, err := svc.GetAgent(context.Background(), rootID, "math")
	if err != nil {
		t.Fatalf("超管 GetAgent: %v", err)
	}
	if a.Name != "数学智能体" || a.SystemPrompt != "你是数学助手" || a.ReasoningEffort != "high" {
		t.Fatalf("GetAgent 字段透出异常: %+v", a)
	}

	// owner（agent_admin，绑定 math）读自身域
	if _, err := svc.GetAgent(context.Background(), idString(ownerID), "math"); err != nil {
		t.Fatalf("agent_admin 读自身域应成功, got %v", err)
	}
	// owner 读他人域（tutor）被拒
	if _, err := svc.GetAgent(context.Background(), idString(ownerID), "tutor"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("agent_admin 越权读应 PERMISSION_DENIED, got %v", err)
	}
	// 普通用户被拒
	plain := register(t, svc, "plain")
	if _, err := svc.GetAgent(context.Background(), plain.ID, "math"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("普通用户读应 PERMISSION_DENIED, got %v", err)
	}
	// 不存在
	if _, err := svc.GetAgent(context.Background(), rootID, "nope"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("不存在应 NOT_FOUND, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetAgentPublic（按智能体 system_prompt 注入用）：任意登录用户可查，
// 仅白名单字段，管理字段不外泄。
// ---------------------------------------------------------------------------

func TestGetAgentPublic_Whitelist(t *testing.T) {
	svc, _ := newTestService(t)
	seedAgents(t, svc) // math 智能体：system_prompt="你是数学助手"，avatar/welcome/reasoning_effort 有值
	plain := register(t, svc, "plain2")

	a, err := svc.GetAgentPublic(context.Background(), plain.ID, "math")
	if err != nil {
		t.Fatalf("普通用户 GetAgentPublic 应成功: %v", err)
	}
	if a.Name != "数学智能体" || a.SystemPrompt != "你是数学助手" || a.ReasoningEffort != "high" || a.Avatar != "🦉" || a.Welcome != "你好" {
		t.Fatalf("白名单字段透出异常: %+v", a)
	}
	// 管理字段不外泄（owner/默认模型）；status 启停位公开（严格多租户域校验用）
	if a.OwnerUserID != 0 || a.Model != "" {
		t.Fatalf("管理字段应被剔除: %+v", a)
	}
	if a.Status != 1 {
		t.Fatalf("启用中的智能体 status 应透出 1, got %d", a.Status)
	}
	// 不存在
	if _, err := svc.GetAgentPublic(context.Background(), plain.ID, "nope"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("不存在应 NOT_FOUND, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateAgent（P2-AI）：全量覆盖语义 + 空串=清空 + reasoning_effort 校验
// ---------------------------------------------------------------------------

func TestUpdateAgent_FieldsAndRBAC(t *testing.T) {
	svc, _ := newTestService(t)
	rootID, ownerID := seedAgents(t, svc)

	// 超管更新：全字段覆盖
	upd, err := svc.UpdateAgent(context.Background(), rootID, Agent{
		ID: "math", Name: "数学学霸", Description: "", Model: "", Avatar: "", Welcome: "", SystemPrompt: "", ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if upd.Name != "数学学霸" || upd.Description != "" || upd.Model != "" || upd.SystemPrompt != "" || upd.ReasoningEffort != "max" {
		t.Fatalf("全量覆盖语义失败: %+v", upd)
	}

	// 空 name 拒绝
	if _, err := svc.UpdateAgent(context.Background(), rootID, Agent{ID: "math", Name: "  "}); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("空 name 应 INVALID_ARGUMENT, got %v", err)
	}
	// 非法 reasoning_effort 拒绝
	if _, err := svc.UpdateAgent(context.Background(), rootID, Agent{ID: "math", Name: "m", ReasoningEffort: "turbo"}); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("非法 reasoning_effort 应 INVALID_ARGUMENT, got %v", err)
	}

	// owner（agent_admin）只能编辑自身域
	if _, err := svc.UpdateAgent(context.Background(), idString(ownerID), Agent{ID: "math", Name: "数学"}); err != nil {
		t.Fatalf("agent_admin 编辑自身域应成功, got %v", err)
	}
	if _, err := svc.UpdateAgent(context.Background(), idString(ownerID), Agent{ID: "tutor", Name: "改"}); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("agent_admin 编辑他人域应 PERMISSION_DENIED, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SetAgentStatus（P2-AI）：仅最高超管；status 仅 0/1
// ---------------------------------------------------------------------------

func TestSetAgentStatus_RBAC(t *testing.T) {
	svc, _ := newTestService(t)
	rootID, ownerID := seedAgents(t, svc)

	// 停用 → 启用
	st, err := svc.SetAgentStatus(context.Background(), rootID, "math", 0)
	if err != nil || st.Status != 0 {
		t.Fatalf("停用应成功 status=0, got %+v err=%v", st, err)
	}
	st, err = svc.SetAgentStatus(context.Background(), rootID, "math", 1)
	if err != nil || st.Status != 1 {
		t.Fatalf("启用应成功 status=1, got %+v err=%v", st, err)
	}
	// 非法 status
	if _, err := svc.SetAgentStatus(context.Background(), rootID, "math", 2); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("status=2 应 INVALID_ARGUMENT, got %v", err)
	}
	// agent_admin 无权启停
	if _, err := svc.SetAgentStatus(context.Background(), idString(ownerID), "math", 0); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("agent_admin 启停应 PERMISSION_DENIED, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DeleteAgent（P2-AI）：仅最高超管；tutor 保护
// ---------------------------------------------------------------------------

func TestDeleteAgent_RBACAndTutor(t *testing.T) {
	svc, _ := newTestService(t)
	rootID, ownerID := seedAgents(t, svc)

	// tutor 不可删
	if err := svc.DeleteAgent(context.Background(), rootID, "tutor"); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("删除 tutor 应 INVALID_ARGUMENT, got %v", err)
	}
	// agent_admin 无权删
	if err := svc.DeleteAgent(context.Background(), idString(ownerID), "math"); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("agent_admin 删除应 PERMISSION_DENIED, got %v", err)
	}
	// 超管软删除成功（保留记录）
	if err := svc.DeleteAgent(context.Background(), rootID, "math"); err != nil {
		t.Fatalf("超管删除 math: %v", err)
	}
	// 软删除 = 置 status=0（记录仍在，列表仍可见以便重新启用）
	got, err := svc.repo.GetAgent(context.Background(), "math")
	if err != nil || got.Status != 0 {
		t.Fatalf("软删除后应 status=0, got %+v err=%v", got, err)
	}
}

// ---------------------------------------------------------------------------
// CreateAgent 新字段与校验（P2-AI）
// ---------------------------------------------------------------------------

func TestCreateAgent_MetadataAndValidation(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	owner := register(t, svc, "agent_owner")
	ownerID, err := strconv.ParseInt(owner.ID, 10, 64)
	if err != nil {
		t.Fatalf("ParseInt(owner.ID): %v", err)
	}

	// 元数据字段落库
	a, err := svc.CreateAgent(context.Background(), root.ID, "chem", "化学", "desc", "m1", "🧪", "hi", "你是化学老师", "low", ownerID)
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if a.Avatar != "🧪" || a.Welcome != "hi" || a.SystemPrompt != "你是化学老师" || a.ReasoningEffort != "low" {
		t.Fatalf("元数据字段落库失败: %+v", a)
	}
	// 非法推理强度拒绝
	if _, err := svc.CreateAgent(context.Background(), root.ID, "phys", "物理", "", "", "", "", "", "ultra", ownerID); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("非法 reasoning_effort 应 INVALID_ARGUMENT, got %v", err)
	}
	// owner 被授予 agent_admin 并绑定该智能体域
	ownerAfter, _ := svc.repo.GetUserByUsername(context.Background(), "agent_owner")
	if ownerAfter.Role != RoleAgentAdmin {
		t.Fatalf("owner 应升级为 agent_admin, got %s", ownerAfter.Role)
	}
	if !ownerAfter.HasTag(tagKeyAgent, "chem") {
		t.Fatalf("owner 应绑定 chem 标签, got %+v", ownerAfter.Tags)
	}
}

func idString(v int64) string { return strconv.FormatInt(v, 10) }

// ---------------------------------------------------------------------------
// owner 可选 + 绑定/更换/解绑（鸡生蛋解耦）
// ---------------------------------------------------------------------------

// TestCreateAgent_OwnerOptional 创建智能体可不绑定 owner。
func TestCreateAgent_OwnerOptional(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")

	a, err := svc.CreateAgent(context.Background(), root.ID, "orphan", "孤儿智能体", "", "", "", "", "", "", 0)
	if err != nil {
		t.Fatalf("CreateAgent(owner=0) 应成功, got %v", err)
	}
	if a.OwnerUserID != 0 {
		t.Fatalf("owner 应为 0, got %d", a.OwnerUserID)
	}
	// 再次读取确认未绑定
	got, err := svc.GetAgent(context.Background(), root.ID, "orphan")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.OwnerUserID != 0 {
		t.Fatalf("owner 应为 0, got %d", got.OwnerUserID)
	}
	// 超管不能作为 owner（防降权）
	su, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	suID, _ := strconv.ParseInt(su.ID, 10, 64)
	if _, err := svc.CreateAgent(context.Background(), root.ID, "bad", "坏", "", "", "", "", "", "", suID); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超管作 owner 应 INVALID_ARGUMENT, got %v", err)
	}
}

// TestBindAgentOwner 绑定/更换/解绑智能体超管。
func TestBindAgentOwner(t *testing.T) {
	svc, _ := newTestService(t)
	rootID, _ := seedAgents(t, svc) // math 的 owner = math_owner

	// 更换 owner：新用户 alice 接管，旧 owner 回收
	alice := register(t, svc, "alice")
	aliceID, _ := strconv.ParseInt(alice.ID, 10, 64)
	a, err := svc.BindAgentOwner(context.Background(), rootID, "math", aliceID)
	if err != nil {
		t.Fatalf("BindAgentOwner 更换失败: %v", err)
	}
	if a.OwnerUserID != aliceID {
		t.Fatalf("owner 应为 alice, got %d", a.OwnerUserID)
	}
	aliceAfter, _ := svc.repo.GetUserByUsername(context.Background(), "alice")
	if aliceAfter.Role != RoleAgentAdmin {
		t.Fatalf("alice 应升级为 agent_admin, got %s", aliceAfter.Role)
	}
	if !aliceAfter.HasTag(tagKeyAgent, "math") {
		t.Fatalf("alice 应绑定 math 标签, got %+v", aliceAfter.Tags)
	}
	oldOwner, _ := svc.repo.GetUserByUsername(context.Background(), "math_owner")
	if oldOwner.Role != RoleUser {
		t.Fatalf("旧 owner 应降为 user, got %s", oldOwner.Role)
	}
	if oldOwner.HasTag(tagKeyAgent, "math") {
		t.Fatalf("旧 owner 应移除 math 标签, got %+v", oldOwner.Tags)
	}

	// 解绑：alice 的 owner 权限被回收
	a, err = svc.BindAgentOwner(context.Background(), rootID, "math", 0)
	if err != nil {
		t.Fatalf("BindAgentOwner 解绑失败: %v", err)
	}
	if a.OwnerUserID != 0 {
		t.Fatalf("解绑后 owner 应为 0, got %d", a.OwnerUserID)
	}
	aliceAfter2, _ := svc.repo.GetUserByUsername(context.Background(), "alice")
	if aliceAfter2.Role != RoleUser {
		t.Fatalf("解绑后 alice 应降为 user, got %s", aliceAfter2.Role)
	}
	if aliceAfter2.HasTag(tagKeyAgent, "math") {
		t.Fatalf("解绑后 alice 应移除 math 标签, got %+v", aliceAfter2.Tags)
	}
}

// TestBindAgentOwner_RBAC 越权与非法入参。
func TestBindAgentOwner_RBAC(t *testing.T) {
	svc, _ := newTestService(t)
	rootID, _ := seedAgents(t, svc)

	// 非超管不可绑定
	plain := register(t, svc, "plain")
	alice := register(t, svc, "alice2")
	aliceID, _ := strconv.ParseInt(alice.ID, 10, 64)
	if _, err := svc.BindAgentOwner(context.Background(), plain.ID, "math", aliceID); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("普通用户绑定应 PERMISSION_DENIED, got %v", err)
	}

	// 智能体不存在
	if _, err := svc.BindAgentOwner(context.Background(), rootID, "nope", aliceID); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("绑定不存在智能体应 NOT_FOUND, got %v", err)
	}

	// 新 owner 不存在
	if _, err := svc.BindAgentOwner(context.Background(), rootID, "math", 999999); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("绑定不存在用户应 INVALID_ARGUMENT, got %v", err)
	}

	// 超管不能作 owner
	su, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	suID, _ := strconv.ParseInt(su.ID, 10, 64)
	if _, err := svc.BindAgentOwner(context.Background(), rootID, "math", suID); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超管作 owner 应 INVALID_ARGUMENT, got %v", err)
	}
}
