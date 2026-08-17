// Package db 提供 PostgreSQL 连接池与迁移能力（P2-15）：
//   - Connect：基于 pgx/v5 创建带健康默认值的连接池（可覆盖）；
//   - MigrateUp / MigrateDown：对嵌入的迁移文件（见 migrations 包）执行迁移；
//   - HealthCheck：连接池 Ping + 轻量查询，供 /healthz 使用。
//
// 连接串（DSN）由各服务 config 生成（见 internal/config），密码一律来自
// 环境变量，严禁硬编码。
//
// 使用示例（服务启动阶段）：
//
//	pool, err := db.Connect(ctx, cfg.DB.DSN())
//	if err := db.MigrateUp(ctx, cfg.DB.DSN(), "auth", migrations.FS); err != nil { ... }
//	defer pool.Close()
package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // postgres 驱动
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options 连接池可选项。零值字段使用默认值。
type Options struct {
	MaxConns        int32         // 最大连接数（默认 20）
	MinConns        int32         // 最小空闲连接（默认 2）
	MaxConnLifetime time.Duration // 连接最长复用时长（默认 1h）
	ConnTimeout     time.Duration // 建立连接超时（默认 5s）

	// AfterConnect 每个新连接建立后执行的回调（如注册 pgvector 类型）。
	// 注意：池建立时会预建 MinConns 个连接，回调对每个连接都会执行，
	// 因此依赖数据库对象（如 extension vector）的回调要求迁移已先行完成。
	AfterConnect func(context.Context, *pgx.Conn) error
}

// defaults 返回带默认值的 Options。
func (o Options) defaults() Options {
	if o.MaxConns == 0 {
		o.MaxConns = 20
	}
	if o.MinConns == 0 {
		o.MinConns = 2
	}
	if o.MaxConnLifetime == 0 {
		o.MaxConnLifetime = time.Hour
	}
	if o.ConnTimeout == 0 {
		o.ConnTimeout = 5 * time.Second
	}
	return o
}

// Connect 创建 PostgreSQL 连接池并验证连通性（Ping）。
func Connect(ctx context.Context, dsn string, opt Options) (*pgxpool.Pool, error) {
	opt = opt.defaults()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: 解析 DSN 失败: %w", err)
	}
	cfg.MaxConns = opt.MaxConns
	cfg.MinConns = opt.MinConns
	cfg.MaxConnLifetime = opt.MaxConnLifetime
	cfg.ConnConfig.ConnectTimeout = opt.ConnTimeout
	if opt.AfterConnect != nil {
		cfg.AfterConnect = opt.AfterConnect
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: 创建连接池失败: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, opt.ConnTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: 数据库不可达: %w", err)
	}
	return pool, nil
}

// MigrateUp 对 fsys 下 dir 目录中的迁移文件执行 up，直到全部应用。
// 幂等：已应用的迁移会被 golang-migrate 自动跳过。
func MigrateUp(ctx context.Context, dsn, dir string, fsys fs.FS) error {
	return run(ctx, dsn, dir, fsys, func(m *migrate.Migrate) error {
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	})
}

// MigrateDown 回滚 dir 目录中的最近 1 个迁移版本。
func MigrateDown(ctx context.Context, dsn, dir string, fsys fs.FS) error {
	return run(ctx, dsn, dir, fsys, func(m *migrate.Migrate) error {
		if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	})
}

// run 组装 migrate 实例并执行 fn。
func run(ctx context.Context, dsn, dir string, fsys fs.FS, fn func(*migrate.Migrate) error) error {
	src, err := iofs.New(fsys, dir)
	if err != nil {
		return fmt.Errorf("db: 迁移源不可用 (%s): %w", dir, err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("db: 初始化迁移失败: %w", err)
	}
	defer m.Close()

	done := make(chan error, 1)
	go func() { done <- fn(m) }()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("db: 迁移执行失败 (%s): %w", dir, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("db: 迁移被取消: %w", ctx.Err())
	}
}

// HealthCheck 对连接池做健康检查：Ping + 轻量查询。
// 返回 nil 表示数据库健康。
func HealthCheck(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("db: ping 失败: %w", err)
	}
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("db: 查询失败: %w", err)
	}
	if one != 1 {
		return fmt.Errorf("db: 健康检查返回异常值 %d", one)
	}
	return nil
}
