package auth

import (
	"testing"
	"time"

	"github.com/Steve5201/agent-backend/internal/errors"
	"github.com/golang-jwt/jwt/v5"
)

// testManager 返回测试用 Manager（TTL 缩短以便快速验证过期逻辑）。
func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Config{
		Secret:     "test-secret-key-please-change",
		AccessTTL:  time.Minute,
		RefreshTTL: 24 * time.Hour,
		Issuer:     "test-issuer",
	})
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// JWT
// ---------------------------------------------------------------------------

// TestSignVerifyAccess_RoundTrip 验证 access 令牌签发后能通过校验并还原载荷。
func TestSignVerifyAccess_RoundTrip(t *testing.T) {
	m := testManager(t)
	token, exp, err := m.SignAccess("user-1", "admin", "tutor")
	if err != nil {
		t.Fatalf("SignAccess error = %v", err)
	}
	if exp.Before(time.Now().Add(30 * time.Second)) {
		t.Error("access 过期时间应 > 30s")
	}

	claims, err := m.Verify(token, TokenTypeAccess)
	if err != nil {
		t.Fatalf("Verify error = %v", err)
	}
	if claims.UserID != "user-1" {
		t.Errorf("UserID = %q, want user-1", claims.UserID)
	}
	if claims.Role != "admin" {
		t.Errorf("Role = %q, want admin", claims.Role)
	}
	if claims.AgentID != "tutor" {
		t.Errorf("AgentID = %q, want tutor", claims.AgentID)
	}
	if claims.TokenType != TokenTypeAccess {
		t.Errorf("TokenType = %q, want access", claims.TokenType)
	}
	if claims.Issuer != "test-issuer" {
		t.Errorf("Issuer = %q, want test-issuer", claims.Issuer)
	}
}

// TestSignRefresh_RoundTrip 验证 refresh 令牌携带 family_id。
func TestSignRefresh_RoundTrip(t *testing.T) {
	m := testManager(t)
	token, _, err := m.SignRefresh("user-1", "family-abc")
	if err != nil {
		t.Fatalf("SignRefresh error = %v", err)
	}

	claims, err := m.Verify(token, TokenTypeRefresh)
	if err != nil {
		t.Fatalf("Verify error = %v", err)
	}
	if claims.FamilyID != "family-abc" {
		t.Errorf("FamilyID = %q, want family-abc", claims.FamilyID)
	}
	if claims.TokenType != TokenTypeRefresh {
		t.Errorf("TokenType = %q, want refresh", claims.TokenType)
	}
}

// TestVerify_WrongType 验证 access/refresh 不能互相冒充。
func TestVerify_WrongType(t *testing.T) {
	m := testManager(t)
	access, _, _ := m.SignAccess("user-1", "user", "")
	refresh, _, _ := m.SignRefresh("user-1", "family-1")

	if _, err := m.Verify(access, TokenTypeRefresh); CodeOf(err) != errors.CodeUnauthenticated {
		t.Errorf("access 冒充 refresh 应失败，got %q", CodeOf(err))
	}
	if _, err := m.Verify(refresh, TokenTypeAccess); CodeOf(err) != errors.CodeUnauthenticated {
		t.Errorf("refresh 冒充 access 应失败，got %q", CodeOf(err))
	}
}

// TestVerify_TamperedToken 验证篡改的令牌无法通过校验。
func TestVerify_TamperedToken(t *testing.T) {
	m := testManager(t)
	token, _, _ := m.SignAccess("user-1", "user", "")
	tampered := token[:len(token)-3] + "xyz" // 破坏签名

	if _, err := m.Verify(tampered, TokenTypeAccess); CodeOf(err) != errors.CodeUnauthenticated {
		t.Errorf("篡改令牌应校验失败，got %q", CodeOf(err))
	}
}

// TestVerify_ExpiredToken 验证过期令牌被拒绝。
func TestVerify_ExpiredToken(t *testing.T) {
	m := testManager(t)
	// 构造一个 1 秒前已过期的 access 令牌
	claims := Claims{
		TokenType: TokenTypeAccess,
		UserID:    "user-1",
		Role:      "user",
	}
	now := time.Now()
	claims.IssuedAt = jwtNumericDate(now.Add(-2 * time.Minute))
	claims.ExpiresAt = jwtNumericDate(now.Add(-1 * time.Minute))
	signed := mustSign(t, m, claims)

	if _, err := m.Verify(signed, TokenTypeAccess); CodeOf(err) != errors.CodeUnauthenticated {
		t.Errorf("过期令牌应被拒绝，got %q", CodeOf(err))
	}
}

// TestManager_ConfigValidation 验证非法配置被拒绝。
func TestManager_ConfigValidation(t *testing.T) {
	if _, err := New(Config{Secret: "", AccessTTL: time.Minute, RefreshTTL: time.Hour}); err == nil {
		t.Error("空 Secret 应报错")
	}
	if _, err := New(Config{Secret: "s", AccessTTL: 0, RefreshTTL: time.Hour}); err == nil {
		t.Error("AccessTTL<=0 应报错")
	}
	if _, err := New(Config{Secret: "s", AccessTTL: time.Minute, RefreshTTL: 0}); err == nil {
		t.Error("RefreshTTL<=0 应报错")
	}
	// 合法配置 + 空 Issuer 应使用默认值
	m, err := New(Config{Secret: "s", AccessTTL: time.Minute, RefreshTTL: time.Hour})
	if err != nil {
		t.Fatalf("合法配置应通过，error = %v", err)
	}
	if m.issuer != "agent-backend" {
		t.Errorf("默认 issuer = %q, want agent-backend", m.issuer)
	}
}

// ---------------------------------------------------------------------------
// 密码哈希
// ---------------------------------------------------------------------------

// TestHashCheckPassword_Ok 验证正确密码通过、错误密码被拒绝。
func TestHashCheckPassword_Ok(t *testing.T) {
	hash, err := HashPassword("P@ssw0rd-2026")
	if err != nil {
		t.Fatalf("HashPassword error = %v", err)
	}
	if hash == "P@ssw0rd-2026" {
		t.Fatal("哈希不应等于明文")
	}

	if err := CheckPassword(hash, "P@ssw0rd-2026"); err != nil {
		t.Errorf("正确密码应通过，got %v", err)
	}
	if err := CheckPassword(hash, "wrong-password"); CodeOf(err) != errors.CodeUnauthenticated {
		t.Errorf("错误密码应返回 UNAUTHENTICATED，got %q", CodeOf(err))
	}
}

// TestHashPassword_Empty 验证空密码被拒绝。
func TestHashPassword_Empty(t *testing.T) {
	if _, err := HashPassword(""); CodeOf(err) != errors.CodeInvalidArgument {
		t.Errorf("空密码应返回 INVALID_ARGUMENT，got %q", CodeOf(err))
	}
}

// TestCheckPassword_InvalidHash 验证非法哈希不 panic 且返回错误。
func TestCheckPassword_InvalidHash(t *testing.T) {
	if err := CheckPassword("not-a-bcrypt-hash", "whatever"); err == nil {
		t.Error("非法哈希应返回错误")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// CodeOf 提取错误码（测试辅助，收敛包引用）。
func CodeOf(err error) errors.ErrorCode {
	return errors.CodeOf(err)
}

// jwtNumericDate 构造 jwt.NumericDate（测试辅助）。
func jwtNumericDate(t time.Time) *jwt.NumericDate {
	return jwt.NewNumericDate(t)
}

// mustSign 直接构造并签名（测试辅助，用于制造过期等特殊载荷）。
func mustSign(t *testing.T, m *Manager, claims Claims) string {
	t.Helper()
	claims.Issuer = m.issuer
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwt.NewNumericDate(time.Now())
	}
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		t.Fatalf("mustSign error = %v", err)
	}
	return signed
}
