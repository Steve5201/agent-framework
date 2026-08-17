package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Steve5201/agent-backend/migrations"
)

// ---------------------------------------------------------------------------
// 连接池配置
// ---------------------------------------------------------------------------

// TestDefaults_Applied 验证默认值与覆盖逻辑。
func TestDefaults_Applied(t *testing.T) {
	d := Options{}.defaults()
	if d.MaxConns != 20 || d.MinConns != 2 {
		t.Errorf("默认连接数错误: MaxConns=%d MinConns=%d, want 20/2", d.MaxConns, d.MinConns)
	}
	if d.MaxConnLifetime != time.Hour || d.ConnTimeout != 5*time.Second {
		t.Errorf("默认时长错误: %v / %v", d.MaxConnLifetime, d.ConnTimeout)
	}

	o := Options{MaxConns: 5, ConnTimeout: time.Second}.defaults()
	if o.MaxConns != 5 {
		t.Errorf("MaxConns 应保留覆盖值 5, got %d", o.MaxConns)
	}
	if o.MinConns != 2 {
		t.Errorf("MinConns 应使用默认 2, got %d", o.MinConns)
	}
	if o.ConnTimeout != time.Second {
		t.Errorf("ConnTimeout 应保留覆盖值, got %v", o.ConnTimeout)
	}
}

// TestParseConfig_InvalidDSN 验证非法 DSN 在连接前就被拒绝。
func TestParseConfig_InvalidDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := Connect(ctx, "not-a-valid-dsn", Options{})
	if err == nil {
		t.Fatal("非法 DSN 应返回错误")
	}
}

// ---------------------------------------------------------------------------
// 迁移
// ---------------------------------------------------------------------------

// TestMigrate_BadDir 验证不存在的迁移目录在无需数据库连接时即报错。
func TestMigrate_BadDir(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := MigrateUp(ctx, "postgres://x:x@localhost:5432/x", "nonexistent", migrations.FS)
	if err == nil {
		t.Fatal("不存在的迁移目录应报错")
	}
}

// TestMigrations_EmbeddedDirs 验证嵌入的迁移目录结构完整可读。
func TestMigrations_EmbeddedDirs(t *testing.T) {
	for _, dir := range []string{"auth", "agent", "llm"} {
		entries, err := migrations.FS.ReadDir(dir)
		if err != nil {
			t.Fatalf("迁移目录 %s 不可读: %v", dir, err)
		}
		if len(entries) == 0 {
			t.Errorf("迁移目录 %s 为空", dir)
		}
	}
}

// ---------------------------------------------------------------------------
// 集成测试（需要真实 PostgreSQL，通过环境变量启用）
// ---------------------------------------------------------------------------

// TestIntegration_ConnectMigrateHealth 端到端验证：连接池 → 迁移 → 健康检查。
// 仅在设置 DB_TEST_DSN 时运行，例如：
//
//	$env:DB_TEST_DSN="postgres://postgres:密码@localhost:5432/auth?sslmode=disable"
//	go test ./internal/db/... -run TestIntegration -v
func TestIntegration_ConnectMigrateHealth(t *testing.T) {
	dsn := os.Getenv("DB_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 DB_TEST_DSN，跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := Connect(ctx, dsn, Options{ConnTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Connect error = %v", err)
	}
	defer pool.Close()

	// 对 auth 目录做幂等迁移（已应用会自动跳过）
	if err := MigrateUp(ctx, dsn, "auth", migrations.FS); err != nil {
		t.Fatalf("MigrateUp error = %v", err)
	}

	if err := HealthCheck(ctx, pool); err != nil {
		t.Fatalf("HealthCheck error = %v", err)
	}
}
