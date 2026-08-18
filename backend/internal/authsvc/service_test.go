package authsvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Steve5201/agent-backend/internal/auth"
	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

const (
	testSecret   = "test-secret-key-for-authsvc"
	testPassword = "Passw0rd-2026"
)

// newTestService 组装带 fake repo 的测试服务。
// 登录失败阈值设 3 次，便于测试限流。
func newTestService(t *testing.T) (*Service, *fakeRepo) {
	t.Helper()
	mgr, err := auth.New(auth.Config{
		Secret:     testSecret,
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
		Issuer:     "test",
	})
	if err != nil {
		t.Fatalf("auth.New error = %v", err)
	}
	repo := newFakeRepo()
	// 严格多租户：登录/注册/建号均校验智能体域存在。默认播种测试常用域，
	// 避免各用例重复前置；使用其它域的用例自行 seedAgent。
	repo.seedAgent("test")
	repo.seedAgent("tutor")
	svc := NewService(Config{
		Repo:             repo,
		JWT:              mgr,
		AccessTTL:        15 * time.Minute,
		RefreshTTL:       24 * time.Hour,
		MaxLoginAttempts: 3,
		LoginLockWindow:  5 * time.Minute,
	})
	return svc, repo
}

// register 测试辅助：注册一个用户并返回。
func register(t *testing.T, svc *Service, username string) *User {
	t.Helper()
	u, err := svc.Register(context.Background(), username, testPassword, "")
	if err != nil {
		t.Fatalf("Register(%s) error = %v", username, err)
	}
	return u
}

// login 测试辅助：以普通用户身份登录并返回结果。
// 走智能体门户入口（agentID="test"）——普通账号经管理端入口（agentID 为空）
// 会被拒绝（见 TestLogin_AdminEntryRoleCheck），这里统一模拟正常用户路径。
func login(t *testing.T, svc *Service, username, password string) *LoginResult {
	t.Helper()
	res, err := svc.Login(context.Background(), username, password, "test")
	if err != nil {
		t.Fatalf("Login(%s) error = %v", username, err)
	}
	return res
}

// ---------------------------------------------------------------------------
// Register（P2-20）
// ---------------------------------------------------------------------------

func TestRegister_Success(t *testing.T) {
	svc, _ := newTestService(t)
	u := register(t, svc, "alice")

	if u.ID == "" {
		t.Error("新用户应有 ID")
	}
	if u.Role != RoleUser {
		t.Errorf("默认角色 = %q, want user", u.Role)
	}
	if u.PasswordHash == testPassword {
		t.Fatal("密码不应以明文存储")
	}
}

func TestRegister_DuplicateUsername(t *testing.T) {
	svc, _ := newTestService(t)
	register(t, svc, "alice")

	_, err := svc.Register(context.Background(), "ALICE", testPassword, "")
	if apperr.CodeOf(err) != apperr.CodeAlreadyExists {
		t.Errorf("重复用户名应返回 ALREADY_EXISTS，got %q", apperr.CodeOf(err))
	}
}

func TestRegister_InvalidCredentials(t *testing.T) {
	svc, _ := newTestService(t)
	cases := []struct {
		name, user, pass string
	}{
		{"用户名过短", "ab", "Passw0rd-2026"},
		{"用户名含非法字符", "alice@x", "Passw0rd-2026"},
		{"密码过短", "alice", "A1bc"},
		{"密码缺数字", "alice", "OnlyLetters"},
		{"密码缺字母", "alice", "12345678"},
	}
	for _, c := range cases {
		_, err := svc.Register(context.Background(), c.user, c.pass, "")
		if apperr.CodeOf(err) != apperr.CodeInvalidArgument {
			t.Errorf("%s: 应返回 INVALID_ARGUMENT，got %q", c.name, apperr.CodeOf(err))
		}
	}
}

// ---------------------------------------------------------------------------
// Login（P2-21）
// ---------------------------------------------------------------------------

func TestLogin_Success(t *testing.T) {
	svc, repo := newTestService(t)
	register(t, svc, "alice")

	res := login(t, svc, "alice", testPassword)
	if res.AccessToken == "" || res.RefreshToken == "" {
		t.Fatal("应签发双令牌")
	}
	if res.ExpiresIn != int64((15 * time.Minute).Seconds()) {
		t.Errorf("ExpiresIn = %d, want %d", res.ExpiresIn, int64((15 * time.Minute).Seconds()))
	}
	if res.User == nil || res.User.Username != "alice" {
		t.Errorf("应返回用户资料，got %+v", res.User)
	}
	// refresh 应入库（按哈希可查到）
	if _, err := repo.GetRefreshTokenByHash(context.Background(), tokenHash(res.RefreshToken)); err != nil {
		t.Errorf("refresh 未入库: %v", err)
	}
	// 用户名归一化（大小写/空白）
	res2 := login(t, svc, "  ALICE ", testPassword)
	if res2.AccessToken == "" {
		t.Error("归一化用户名应能登录")
	}
}

func TestLogin_WrongCredentials(t *testing.T) {
	svc, _ := newTestService(t)
	register(t, svc, "alice")

	// 错误密码与不存在用户应返回同一错误（不泄露账号是否存在）
	_, err := svc.Login(context.Background(), "alice", "WrongPass-1", "")
	if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("错误密码应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
	_, err = svc.Login(context.Background(), "nobody", testPassword, "")
	if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("不存在用户应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
}

func TestLogin_DisabledAccount(t *testing.T) {
	svc, repo := newTestService(t)
	register(t, svc, "alice")

	// 禁用账号
	repo.mu.Lock()
	repo.users["alice"].Status = 0
	repo.mu.Unlock()

	_, err := svc.Login(context.Background(), "alice", testPassword, "")
	if apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Errorf("禁用账号应返回 PERMISSION_DENIED，got %q", apperr.CodeOf(err))
	}
}

// TestLogin_AdminEntryRoleCheck 管理员入口（无 agent_id）只允许管理员登录：
//   - 普通账号走管理端入口 → PERMISSION_DENIED（防越权/角色混淆）；
//   - 普通账号走智能体门户（带 agent_id）→ 正常放行；
//   - 管理员账号走管理端入口 → 正常放行。
func TestLogin_AdminEntryRoleCheck(t *testing.T) {
	svc, _ := newTestService(t)
	register(t, svc, "alice") // 普通用户

	// 1. 普通用户登录管理端入口：拒绝
	_, err := svc.Login(context.Background(), "alice", testPassword, "")
	if apperr.CodeOf(err) != apperr.CodePermissionDenied {
		t.Errorf("普通用户登录管理端入口应返回 PERMISSION_DENIED，got %q", apperr.CodeOf(err))
	}
	if msg := err.Error(); !strings.Contains(msg, "仅限管理员") {
		t.Errorf("错误信息应明确提示仅限管理员，got %q", msg)
	}

	// 2. 普通用户经智能体门户登录：放行
	if _, err := svc.Login(context.Background(), "alice", testPassword, "tutor"); err != nil {
		t.Errorf("普通用户经智能体门户登录应成功，got %v", err)
	}

	// 3. 管理员登录管理端入口：放行
	if _, err := svc.EnsureAdmin(context.Background(), "root", testPassword); err != nil {
		t.Fatalf("EnsureAdmin error = %v", err)
	}
	if _, err := svc.Login(context.Background(), "root", testPassword, ""); err != nil {
		t.Errorf("管理员登录管理端入口应成功，got %v", err)
	}
}

// TestLogin_Throttle 连续失败达到阈值后锁定，成功登录清零计数。
func TestLogin_Throttle(t *testing.T) {
	svc, _ := newTestService(t)
	register(t, svc, "alice")

	// 3 次失败
	for i := 0; i < 3; i++ {
		_, err := svc.Login(context.Background(), "alice", "WrongPass-1", "")
		if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
			t.Fatalf("第 %d 次失败应返回 UNAUTHENTICATED，got %q", i+1, apperr.CodeOf(err))
		}
	}
	// 第 4 次被锁定
	_, err := svc.Login(context.Background(), "alice", testPassword, "")
	if apperr.CodeOf(err) != apperr.CodeResourceExhausted {
		t.Errorf("锁定期间应返回 RESOURCE_EXHAUSTED，got %q", apperr.CodeOf(err))
	}
	if msg := err.Error(); !strings.Contains(msg, "秒后重试") {
		t.Errorf("错误信息应提示重试时间，got %q", msg)
	}

	// 锁定期结束（注入时钟）后恢复
	svc.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	res, err := svc.Login(context.Background(), "alice", testPassword, "test")
	if err != nil {
		t.Fatalf("锁定期结束后应能登录: %v", err)
	}
	if res.AccessToken == "" {
		t.Error("恢复后应签发令牌")
	}
}

// ---------------------------------------------------------------------------
// Refresh（P2-22）
// ---------------------------------------------------------------------------

func TestRefresh_Rotation_SingleUse(t *testing.T) {
	svc, repo := newTestService(t)
	register(t, svc, "alice")
	old := login(t, svc, "alice", testPassword)

	// 刷新成功：旧 refresh 应被吊销（单次使用）
	newRes, err := svc.Refresh(context.Background(), old.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh error = %v", err)
	}
	if newRes.AccessToken == "" || newRes.RefreshToken == "" {
		t.Fatal("应签发新双令牌")
	}
	if newRes.RefreshToken == old.RefreshToken {
		t.Error("新 refresh 不应与旧 refresh 相同")
	}
	// 旧 token 应标记为已吊销
	oldRec, _ := repo.GetRefreshTokenByHash(context.Background(), tokenHash(old.RefreshToken))
	if oldRec.RevokedAt == nil {
		t.Error("旧 refresh 应被吊销（单次使用）")
	}
	// 新 token 应入库且未吊销
	newRec, err := repo.GetRefreshTokenByHash(context.Background(), tokenHash(newRes.RefreshToken))
	if err != nil {
		t.Fatalf("新 refresh 应入库: %v", err)
	}
	if newRec.RevokedAt != nil {
		t.Error("新 refresh 不应处于吊销状态")
	}
}

func TestRefresh_Replay_RevokesFamily(t *testing.T) {
	svc, repo := newTestService(t)
	register(t, svc, "alice")
	old := login(t, svc, "alice", testPassword)

	// 正常刷新一次（旧 token 被吊销，族内新增新 token）
	first, err := svc.Refresh(context.Background(), old.RefreshToken)
	if err != nil {
		t.Fatalf("首次 Refresh error = %v", err)
	}

	// 重放旧 token：应整族吊销
	_, err = svc.Refresh(context.Background(), old.RefreshToken)
	if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("重放应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
	// 族内新 token 也应被连带吊销
	firstRec, _ := repo.GetRefreshTokenByHash(context.Background(), tokenHash(first.RefreshToken))
	if firstRec.RevokedAt == nil {
		t.Error("重放后族内新 refresh 应被整族吊销")
	}
}

func TestRefresh_Expired(t *testing.T) {
	svc, repo := newTestService(t)
	register(t, svc, "alice")
	res := login(t, svc, "alice", testPassword)

	// 把库中记录的过期时间改为过去（JWT 本身仍有效，验证服务端库校验兜底）
	repo.setTokenExpired(tokenHash(res.RefreshToken))

	_, err := svc.Refresh(context.Background(), res.RefreshToken)
	if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("过期 refresh 应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
}

func TestRefresh_InvalidToken(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Refresh(context.Background(), "not-a-real-token")
	if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("无效 refresh 应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
}

// ---------------------------------------------------------------------------
// Logout（P2-23）
// ---------------------------------------------------------------------------

func TestLogout_RevokesWholeFamily(t *testing.T) {
	svc, repo := newTestService(t)
	register(t, svc, "alice")
	res := login(t, svc, "alice", testPassword)
	// 族内再轮换一次，产生同族第二条 token
	rotated, err := svc.Refresh(context.Background(), res.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh error = %v", err)
	}

	if err := svc.Logout(context.Background(), rotated.RefreshToken); err != nil {
		t.Fatalf("Logout error = %v", err)
	}
	// 整族（两条）都应被吊销
	for _, hash := range []string{tokenHash(res.RefreshToken), tokenHash(rotated.RefreshToken)} {
		rec, _ := repo.GetRefreshTokenByHash(context.Background(), hash)
		if rec.RevokedAt == nil {
			t.Error("登出后族内所有 refresh 应被吊销")
		}
	}
}

func TestLogout_InvalidToken(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Logout(context.Background(), "garbage"); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("无效 refresh 登出应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
}

// ---------------------------------------------------------------------------
// Me（P2-24）
// ---------------------------------------------------------------------------

func TestMe_Success(t *testing.T) {
	svc, _ := newTestService(t)
	u := register(t, svc, "alice")

	got, err := svc.Me(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("Me error = %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("Username = %q, want alice", got.Username)
	}
	if got.PasswordHash != "" {
		t.Error("Me 不应返回密码哈希")
	}
}

func TestMe_UnknownUser(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Me(context.Background(), "999999")
	if apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("未知用户应返回 NOT_FOUND，got %q", apperr.CodeOf(err))
	}
}

func TestMe_EmptyUserID(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Me(context.Background(), "")
	if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("空 user_id 应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
}

// ---------------------------------------------------------------------------
// ChangePassword（用户自助修改密码）
// ---------------------------------------------------------------------------

func TestChangePassword_Success(t *testing.T) {
	svc, repo := newTestService(t)
	u := register(t, svc, "alice")
	res := login(t, svc, "alice", testPassword)
	oldHash := repo.users["alice"].PasswordHash

	// 改密成功：旧密码校验 + 新密码落库
	if err := svc.ChangePassword(context.Background(), u.ID, testPassword, "NewPass-2026"); err != nil {
		t.Fatalf("ChangePassword error = %v", err)
	}
	if repo.users["alice"].PasswordHash == oldHash {
		t.Error("改密后密码哈希应变化")
	}
	// 旧密码已不可用，新密码可登录
	if _, err := svc.Login(context.Background(), "alice", testPassword, "test"); apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("旧密码登录应失败，got %q", apperr.CodeOf(err))
	}
	login(t, svc, "alice", "NewPass-2026")
	// 改密应吊销旧 refresh token（强制下线）
	rec, _ := repo.GetRefreshTokenByHash(context.Background(), tokenHash(res.RefreshToken))
	if rec == nil || rec.RevokedAt == nil {
		t.Error("改密后旧 refresh token 应被吊销")
	}
}

func TestChangePassword_WrongOldPassword(t *testing.T) {
	svc, repo := newTestService(t)
	u := register(t, svc, "alice")
	oldHash := repo.users["alice"].PasswordHash

	err := svc.ChangePassword(context.Background(), u.ID, "Wrong-Old-1", "NewPass-2026")
	if apperr.CodeOf(err) != apperr.CodeUnauthenticated {
		t.Errorf("原密码错误应返回 UNAUTHENTICATED，got %q", apperr.CodeOf(err))
	}
	if repo.users["alice"].PasswordHash != oldHash {
		t.Error("原密码错误时不应修改密码")
	}
}

func TestChangePassword_WeakNewPassword(t *testing.T) {
	svc, repo := newTestService(t)
	u := register(t, svc, "alice")
	oldHash := repo.users["alice"].PasswordHash

	for _, pw := range []string{"short", "onlyletters", "12345678"} {
		err := svc.ChangePassword(context.Background(), u.ID, testPassword, pw)
		if apperr.CodeOf(err) != apperr.CodeInvalidArgument {
			t.Errorf("弱密码 %q 应返回 INVALID_ARGUMENT，got %q", pw, apperr.CodeOf(err))
		}
	}
	if repo.users["alice"].PasswordHash != oldHash {
		t.Error("新密码不合规时不应修改密码")
	}
}

func TestChangePassword_UnknownUser(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.ChangePassword(context.Background(), "999999", testPassword, "NewPass-2026"); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("未知用户应返回 NOT_FOUND，got %q", apperr.CodeOf(err))
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

func TestTokenHash_StableAndIrreversible(t *testing.T) {
	h1 := tokenHash("abc")
	h2 := tokenHash("abc")
	if h1 != h2 {
		t.Error("相同输入应产生相同哈希")
	}
	if len(h1) != 64 { // SHA-256 输出 32 字节 → 64 位 hex
		t.Errorf("SHA-256 应输出 64 位 hex，got %d", len(h1))
	}
}

func TestNewUUID_Format(t *testing.T) {
	u := newUUID()
	parts := strings.Split(u, "-")
	if len(parts) != 5 {
		t.Fatalf("UUID 应含 5 段，got %q", u)
	}
	// version 位应为 4
	if parts[2][0] != '4' {
		t.Errorf("UUID version 应为 4，got %q", parts[2])
	}
	// 两次生成不同
	if newUUID() == newUUID() {
		t.Error("两次生成的 UUID 不应相同")
	}
}
