package config

import (
	"strings"
	"testing"
	"time"
)

// TestLoad_Defaults 验证默认值正确。
func TestLoad_Defaults(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")

	cfg, err := Load("auth", 8081)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ServiceName != "auth" {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "auth")
	}
	if cfg.Env != "dev" {
		t.Errorf("Env = %q, want dev", cfg.Env)
	}
	if cfg.HTTPPort != 8081 {
		t.Errorf("HTTPPort = %d, want 8081（默认端口=传入值）", cfg.HTTPPort)
	}
	if cfg.DB.Host != "localhost" || cfg.DB.Port != 5432 || cfg.DB.User != "postgres" {
		t.Errorf("DB 默认值异常: %+v", cfg.DB)
	}
	if cfg.DB.Name != "auth" {
		t.Errorf("DB.Name = %q, want auth（默认库名=服务名）", cfg.DB.Name)
	}
	if !strings.Contains(cfg.DB.DSN(), "sslmode=disable") {
		t.Errorf("DSN 应包含 sslmode=disable，实际: %s", cfg.DB.DSN())
	}
}

// TestLoad_EnvOverride 验证环境变量覆盖默认值。
func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("HTTP_PORT", "8081")
	t.Setenv("DB_HOST", "192.168.1.10")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load("agent", 8082)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPPort != 8081 {
		t.Errorf("HTTPPort = %d, want 8081（环境变量覆盖默认 8082）", cfg.HTTPPort)
	}
	if cfg.DB.Host != "192.168.1.10" {
		t.Errorf("DB.Host = %q, want 192.168.1.10", cfg.DB.Host)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

// TestLoad_GRPCPort 验证 gRPC 端口默认与覆盖。
func TestLoad_GRPCPort(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")
	cfg, err := Load("auth", 8081)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GRPCPort != 8081 {
		t.Errorf("GRPCPort 默认 = %d, want 8081", cfg.GRPCPort)
	}

	t.Setenv("GRPC_PORT", "18081")
	cfg2, err := Load("auth", 8081)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg2.GRPCPort != 18081 {
		t.Errorf("GRPCPort 覆盖 = %d, want 18081", cfg2.GRPCPort)
	}
}

// TestLoad_JWTDefaults 验证 JWT 默认时长（15m / 7d）。
func TestLoad_JWTDefaults(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")
	cfg, err := Load("auth", 8081)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JWT.AccessTTL != 15*time.Minute {
		t.Errorf("JWT.AccessTTL = %v, want 15m", cfg.JWT.AccessTTL)
	}
	if cfg.JWT.RefreshTTL != 7*24*time.Hour {
		t.Errorf("JWT.RefreshTTL = %v, want 168h", cfg.JWT.RefreshTTL)
	}
	if cfg.JWT.Secret != "" {
		t.Errorf("JWT.Secret 默认应为空，got %q", cfg.JWT.Secret)
	}
}

// TestLoad_JWTEnvOverride 验证 JWT 环境变量覆盖。
func TestLoad_JWTEnvOverride(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("JWT_SECRET", "my-jwt-secret")
	t.Setenv("JWT_ACCESS_TTL", "5m")
	t.Setenv("JWT_REFRESH_TTL", "48h")

	cfg, err := Load("auth", 8081)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JWT.Secret != "my-jwt-secret" {
		t.Errorf("JWT.Secret = %q, want my-jwt-secret", cfg.JWT.Secret)
	}
	if cfg.JWT.AccessTTL != 5*time.Minute {
		t.Errorf("JWT.AccessTTL = %v, want 5m", cfg.JWT.AccessTTL)
	}
	if cfg.JWT.RefreshTTL != 48*time.Hour {
		t.Errorf("JWT.RefreshTTL = %v, want 48h", cfg.JWT.RefreshTTL)
	}
}

// TestLoad_RateLimitDefaults 验证限流默认参数。
func TestLoad_RateLimitDefaults(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")
	cfg, err := Load("gateway", 8080)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RateLimit.Rate != 100.0 {
		t.Errorf("RateLimit.Rate = %v, want 100", cfg.RateLimit.Rate)
	}
	if cfg.RateLimit.Burst != 50 {
		t.Errorf("RateLimit.Burst = %d, want 50", cfg.RateLimit.Burst)
	}
}

// TestLoad_RateLimitEnvOverride 验证限流环境变量覆盖。
func TestLoad_RateLimitEnvOverride(t *testing.T) {
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("RATE_LIMIT_RATE", "30")
	t.Setenv("RATE_LIMIT_BURST", "10")

	cfg, err := Load("gateway", 8080)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RateLimit.Rate != 30.0 {
		t.Errorf("RateLimit.Rate = %v, want 30", cfg.RateLimit.Rate)
	}
	if cfg.RateLimit.Burst != 10 {
		t.Errorf("RateLimit.Burst = %d, want 10", cfg.RateLimit.Burst)
	}
}

// TestLoad_MissingPassword 验证缺少 DB_PASSWORD 时报错。
func TestLoad_MissingPassword(t *testing.T) {
	t.Setenv("DB_PASSWORD", "")

	if _, err := Load("auth", 8081); err == nil {
		t.Fatal("缺少 DB_PASSWORD 时应当返回错误")
	}
}

// TestDBConfig_DSN 验证 DSN 格式。
func TestDBConfig_DSN(t *testing.T) {
	d := DBConfig{
		Host: "localhost", Port: 5432, User: "u",
		Password: "p", Name: "n", SSLMode: "disable",
	}
	want := "postgres://u:p@localhost:5432/n?sslmode=disable"
	if got := d.DSN(); got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}
