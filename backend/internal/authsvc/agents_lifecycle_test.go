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
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	owner, err := svc.AdminCreateUser(context.Background(), root.ID, "math_owner", testPassword, "user", "math", nil)
	if err != nil {
		t.Fatalf("AdminCreateUser: %v", err)
	}
	ownerID, err := strconv.ParseInt(owner.ID, 10, 64)
	if err != nil {
		t.Fatalf("ParseInt(owner.ID): %v", err)
	}
	if _, err := svc.CreateAgent(context.Background(), root.ID, "math", "数学智能体", "desc", "model-x", "🦉", "你好", "你是数学助手", "high", ownerID); err != nil {
		t.Fatalf("CreateAgent: %v", err)
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
