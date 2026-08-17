package authsvc

import (
	"context"
	"strconv"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// TestAdminGetUsersByIds 批量查询核心行为：去重、顺序保持、静默跳过不存在、
// 剥离密码哈希、空请求、超限拒绝、非管理员拒绝。
func TestAdminGetUsersByIds(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	actorID := root.ID

	ids := make([]int64, 0, 3)
	for _, name := range []string{"alice", "bob", "carol"} {
		u, err := svc.AdminCreateUser(context.Background(), actorID, name, testPassword, "user", "tutor", nil)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		n, _ := strconv.ParseInt(u.ID, 10, 64)
		ids = append(ids, n)
	}

	// 乱序 + 重复 + 不存在的 ID：期望去重、按首次出现顺序返回、跳过不存在
	query := []int64{ids[2], ids[0], ids[0], 999999, ids[1]}
	users, err := svc.AdminGetUsersByIds(context.Background(), actorID, query)
	if err != nil {
		t.Fatalf("AdminGetUsersByIds: %v", err)
	}
	want := []string{"carol", "alice", "bob"}
	if len(users) != len(want) {
		t.Fatalf("应返回 %d 个用户, got %d", len(want), len(users))
	}
	for i, u := range users {
		if u.Username != want[i] {
			t.Fatalf("users[%d].Username = %s, want %s", i, u.Username, want[i])
		}
		if u.PasswordHash != "" {
			t.Fatalf("响应不应携带密码哈希")
		}
	}

	// 空请求返回空列表（不报错）
	if empty, err := svc.AdminGetUsersByIds(context.Background(), actorID, nil); err != nil || len(empty) != 0 {
		t.Fatalf("空请求应返回空列表, got err=%v len=%d", err, len(empty))
	}

	// 超过上限拒绝
	tooMany := make([]int64, maxUsersByIDs+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	if _, err := svc.AdminGetUsersByIds(context.Background(), actorID, tooMany); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超限应拒绝, got %v", err)
	}

	// 普通用户调用应拒绝
	alice, _ := svc.repo.GetUserByUsername(context.Background(), "alice")
	if _, err := svc.AdminGetUsersByIds(context.Background(), alice.ID, []int64{ids[0]}); apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Fatalf("普通用户应拒绝, got %v", err)
	}
}

// TestAdminGetUsersByIds_ScopeFilter agent_admin 按管辖范围过滤：只返回本智能体组用户。
func TestAdminGetUsersByIds_ScopeFilter(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	root, _ := svc.repo.GetUserByUsername(context.Background(), "root")
	rootID := root.ID

	owner, err := svc.AdminCreateUser(context.Background(), rootID, "math_owner", testPassword, "user", "math", nil)
	if err != nil {
		t.Fatalf("create math_owner: %v", err)
	}
	ownerID, _ := strconv.ParseInt(owner.ID, 10, 64)
	if _, err := svc.CreateAgent(context.Background(), rootID, "math", "数学智能体", "", "", "", "", "", "", ownerID); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	uTutor, _ := svc.AdminCreateUser(context.Background(), rootID, "u_tutor", testPassword, "user", "tutor", nil)
	uMath, _ := svc.AdminCreateUser(context.Background(), rootID, "u_math", testPassword, "user", "math", nil)
	idT, _ := strconv.ParseInt(uTutor.ID, 10, 64)
	idM, _ := strconv.ParseInt(uMath.ID, 10, 64)

	users, err := svc.AdminGetUsersByIds(context.Background(), owner.ID, []int64{idT, idM})
	if err != nil {
		t.Fatalf("agent_admin 批量查询: %v", err)
	}
	if len(users) != 1 || users[0].Username != "u_math" {
		t.Fatalf("agent_admin 应只看到本组用户, got %+v", users)
	}
}
