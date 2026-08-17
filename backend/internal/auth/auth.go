// Package auth 提供认证与密码哈希能力（P2-12）：
//   - JWT 双令牌：access（短期，15m 默认）+ refresh（长期，7d 默认），
//     refresh 携带 family_id 用于令牌族轮换（见 auth 迁移 refresh_tokens.family_id）；
//   - 校验时强制校验 token_type，防止 access/refresh 互相冒充；
//   - bcrypt 密码哈希（cost=12），校验时自动检查格式与强度。
//
// 所有错误统一返回 *errors.Error（见 internal/errors），
// token 无效/过期一律映射为 CodeUnauthenticated。
//
// 使用示例（服务启动时）：
//
//	mgr, err := auth.New(auth.Config{
//	    Secret:     os.Getenv("JWT_SECRET"),   // 严禁硬编码
//	    AccessTTL:  15 * time.Minute,
//	    RefreshTTL: 7 * 24 * time.Hour,
//	    Issuer:     "agent-backend",
//	})
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Steve5201/agent-backend/internal/errors"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// TokenType 令牌类型，用于防串用。
type TokenType string

const (
	TokenTypeAccess  TokenType = "access"
	TokenTypeRefresh TokenType = "refresh"
)

// Claims 自定义载荷。除标准声明（sub=user_id、iat、exp、iss）外，
// 附带 token_type 与用户角色；access 额外携带 agent_id（资源域归属，
// 阶段3 多租户：agent_admin/admin 据此锁定自身智能体），
// refresh 额外携带 family_id（族轮换）。
type Claims struct {
	TokenType TokenType `json:"token_type"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	AgentID   string    `json:"agent_id,omitempty"`  // 仅 access：资源域归属（空 = 超管/未绑定）
	FamilyID  string    `json:"family_id,omitempty"` // 仅 refresh 使用
	jwt.RegisteredClaims
}

// Config 令牌配置。
type Config struct {
	Secret     string        // HMAC 签名密钥（环境变量注入，禁止硬编码）
	AccessTTL  time.Duration // access 令牌有效期
	RefreshTTL time.Duration // refresh 令牌有效期
	Issuer     string        // 签发者标识（如服务名）
}

// Manager 负责令牌的签发与校验。
type Manager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	issuer     string
}

// New 校验配置并创建 Manager。
// 校验规则：Secret 非空、两个 TTL 均大于 0；Issuer 为空时默认 "agent-backend"。
func New(cfg Config) (*Manager, error) {
	if cfg.Secret == "" {
		return nil, fmt.Errorf("auth: JWT_SECRET 不能为空")
	}
	if cfg.AccessTTL <= 0 || cfg.RefreshTTL <= 0 {
		return nil, fmt.Errorf("auth: AccessTTL/RefreshTTL 必须大于 0")
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "agent-backend"
	}
	return &Manager{
		secret:     []byte(cfg.Secret),
		accessTTL:  cfg.AccessTTL,
		refreshTTL: cfg.RefreshTTL,
		issuer:     cfg.Issuer,
	}, nil
}

// sign 统一签发入口：构造 Claims 并签名。
func (m *Manager) sign(claims Claims, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	// 每次签发注入随机 jti（JWT ID）：iat 序列化为秒级 Unix 时间戳，
	// 若不注入随机数，同一秒内对相同 (userID, familyID) 会签出完全相同的
	// token，破坏 refresh 轮换的"单次使用"语义（重放检测失效）。
	claims.ID = randomJTI()
	claims.Issuer = m.issuer
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.NotBefore = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, errors.Wrap(errors.CodeInternal, "签发 token 失败", err)
	}
	return signed, claims.ExpiresAt.Time, nil
}

// randomJTI 生成 128 位随机 JWT ID。crypto/rand 失败概率极低，
// 此时退回纳秒时间戳，仍保证"每次签发不同"。
func randomJTI() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("f%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// SignAccess 签发 access 令牌。
// 返回 (token, 过期时间, 错误)。
// agentID 为资源域归属（阶段3 多租户）：agent_admin/admin 绑定到自身智能体，
// super_admin/普通用户传空。
func (m *Manager) SignAccess(userID, role, agentID string) (string, time.Time, error) {
	return m.sign(Claims{
		TokenType: TokenTypeAccess,
		UserID:    userID,
		Role:      role,
		AgentID:   agentID,
	}, m.accessTTL)
}

// SignRefresh 签发 refresh 令牌。familyID 为令牌族标识（UUID），
// 用于刷新时的族轮换与吊销判断（对应 refresh_tokens.family_id 列）。
func (m *Manager) SignRefresh(userID, familyID string) (string, time.Time, error) {
	return m.sign(Claims{
		TokenType: TokenTypeRefresh,
		UserID:    userID,
		FamilyID:  familyID,
	}, m.refreshTTL)
}

// Verify 校验令牌并返回载荷。
//   - 签名/结构非法、过期、类型不符 → *errors.Error(CodeUnauthenticated)；
//   - wantType 为期望类型（TokenTypeAccess / TokenTypeRefresh）。
func (m *Manager) Verify(tokenStr string, wantType TokenType) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}))
	if err != nil {
		return nil, errors.New(errors.CodeUnauthenticated, "token 无效或已过期")
	}
	if !token.Valid {
		return nil, errors.New(errors.CodeUnauthenticated, "token 无效")
	}
	// 严格匹配令牌类型，防止 access/refresh 串用
	if claims.TokenType != wantType {
		return nil, errors.New(errors.CodeUnauthenticated, "token 类型不符")
	}
	return claims, nil
}

// hashPasswordCost bcrypt 计算成本。cost=12 在安全性与耗时（约 100ms）间平衡。
const hashPasswordCost = 12

// HashPassword 对密码做 bcrypt 哈希（用于注册/改密存储）。
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New(errors.CodeInvalidArgument, "密码不能为空")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), hashPasswordCost)
	if err != nil {
		return "", errors.Wrap(errors.CodeInternal, "密码哈希失败", err)
	}
	return string(hash), nil
}

// CheckPassword 校验明文密码与哈希是否匹配。
// 匹配返回 nil；不匹配返回 *errors.Error(CodeUnauthenticated, "密码错误")。
func CheckPassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return errors.New(errors.CodeUnauthenticated, "密码错误")
	}
	return nil
}
