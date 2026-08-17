// passwd 运维小工具：重置任意用户的密码（万不得已时使用）。
//
// 说明：
//   - 密码以 bcrypt 哈希存储（users.password_hash），无法逆向还原明文。
//     忘记密码时只能用本工具（或管理端建用户接口）重置。
//   - 直接操作 auth 库的 users 表，绕过业务层（auth 服务本身没有"管理员改密"接口）。
//   - 强度规则与业务层一致：≥8 位且同时包含字母与数字。
//
// 用法（需先提供数据库密码，从 deploy/.env 读取或手动设置）：
//
//	$env:DB_PASSWORD='221434'
//	go run ./cmd/passwd -username admin -password 'NewPass-2026'   # 显式设置
//	go run ./cmd/passwd -username admin -generate                  # 生成随机强密码并打印
//	go run ./cmd/passwd -list                                      # 仅列出用户名（不修改）
//	go run ./cmd/passwd -username admin                            # 交互输入新密码（不落终端历史）
//
// 数据库连接默认 localhost:5432/auth（postgres），可用 DB_HOST / DB_PORT /
// DB_USER / DB_NAME 环境变量覆盖（与后端其它服务一致）。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func main() {
	var (
		username = flag.String("username", "", "要重置的用户名（与 -generate 组合；-list 时忽略）")
		password = flag.String("password", "", "新密码（≥8 位且含字母与数字；与 -generate 互斥）")
		generate = flag.Bool("generate", false, "生成随机强密码并打印到终端")
		hashOnly = flag.Bool("hash-only", false, "只计算并输出 bcrypt 哈希，不连数据库（供 reset-password.ps1 配合 psql 使用）")
		list     = flag.Bool("list", false, "列出全部用户名（只读，不做任何修改）")
	)
	flag.Parse()

	if *generate && *password != "" {
		fmt.Fprintln(os.Stderr, "[错误] -generate 与 -password 互斥，只能二选一")
		os.Exit(2)
	}
	if *list && (*username != "" || *password != "" || *generate || *hashOnly) {
		fmt.Fprintln(os.Stderr, "[错误] -list 为只读模式，不能与其它参数组合")
		os.Exit(2)
	}

	// 纯计算模式：不依赖数据库，stdout 只输出哈希（脚本捕获用）。
	// -generate 的随机明文仅打印到 stderr，避免污染 stdout 捕获。
	if *hashOnly {
		newPwd := *password
		if *generate {
			gen, err := randomPassword()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[错误] 生成随机密码失败：%v\n", err)
				os.Exit(1)
			}
			newPwd = gen
			fmt.Fprintf(os.Stderr, "新密码（请立即妥善保存，仅显示这一次）: %s\n", gen)
		}
		if newPwd == "" {
			pwd, err := readPasswordFromStdin()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[错误] 读取新密码失败：%v\n", err)
				os.Exit(1)
			}
			newPwd = pwd
		}
		if err := validatePassword(newPwd); err != nil {
			fmt.Fprintf(os.Stderr, "[错误] %v\n", err)
			os.Exit(2)
		}
		hash, err := auth.HashPassword(newPwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[错误] 生成 bcrypt 哈希失败：%v\n", err)
			os.Exit(1)
		}
		fmt.Println(hash)
		return
	}

	cfg, err := config.Load("auth", 8081)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[错误] 读取配置失败：%v\n", err)
		os.Exit(1)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)
	defer func() { _ = log.Sync() }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DB.DSN())
	if err != nil {
		log.Fatal("连接数据库失败", zap.Error(err))
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal("数据库不可达", zap.Error(err))
	}

	if *list {
		runList(ctx, pool, log)
		return
	}
	if *username == "" {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\n[错误] 必须指定 -username（或用 -list 查看用户）")
		os.Exit(2)
	}
	newPwd := *password
	if *generate {
		gen, err := randomPassword()
		if err != nil {
			log.Fatal("生成随机密码失败", zap.Error(err))
		}
		newPwd = gen
	}
	if newPwd == "" {
		pwd, err := readPasswordFromStdin()
		if err != nil {
			log.Fatal("读取新密码失败", zap.Error(err))
		}
		newPwd = pwd
	}
	if err := runReset(ctx, pool, log, *username, newPwd); err != nil {
		log.Fatal("重置密码失败", zap.Error(err))
	}
	if *generate {
		// 随机密码只打印这一次；之后无法再找回（哈希不可逆）。
		fmt.Fprintf(os.Stdout, "\n新密码（请立即妥善保存）: %s\n", newPwd)
	} else {
		fmt.Fprintln(os.Stdout, "密码已重置")
	}
}

// runList 列出全部用户名（便于确认 -username 拼写正确）。
func runList(ctx context.Context, pool *pgxpool.Pool, log *zap.Logger) {
	rows, err := pool.Query(ctx, `SELECT username FROM users ORDER BY id`)
	if err != nil {
		log.Fatal("查询用户列表失败", zap.Error(err))
	}
	defer rows.Close()
	fmt.Println("用户名：")
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			log.Fatal("扫描用户列表失败", zap.Error(err))
		}
		fmt.Printf("  - %s\n", u)
	}
	if err := rows.Err(); err != nil {
		log.Fatal("遍历用户列表失败", zap.Error(err))
	}
}

// runReset 重置指定用户的密码哈希（幂等：同名更新，不存在则报错）。
func runReset(ctx context.Context, pool *pgxpool.Pool, log *zap.Logger, username, newPwd string) error {
	if err := validatePassword(newPwd); err != nil {
		return err
	}
	hash, err := auth.HashPassword(newPwd)
	if err != nil {
		return fmt.Errorf("生成 bcrypt 哈希失败: %w", err)
	}
	// updated_at 显式刷新；role/tags/status 均不动（仅改密码）。
	tag, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE username = $2`,
		hash, username,
	)
	if err != nil {
		return fmt.Errorf("更新数据库失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("用户 %q 不存在（可用 -list 查看全部用户名）", username)
	}
	log.Info("密码已重置",
		zap.String("username", username),
		zap.Time("updated_at", time.Now()))
	return nil
}

// validatePassword 与 authsvc.validateCredentials 的密码规则保持一致。
func validatePassword(pw string) error {
	if len(pw) < 8 {
		return fmt.Errorf("密码须不少于 8 位，且同时包含字母与数字")
	}
	var hasLetter, hasDigit bool
	for _, r := range pw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return fmt.Errorf("密码须不少于 8 位，且同时包含字母与数字")
	}
	return nil
}

// readPasswordFromStdin 无回显读取新密码（避免密码出现在终端历史 / shell 进程列表）。
func readPasswordFromStdin() (string, error) {
	fmt.Fprint(os.Stderr, "新密码（输入不可见）: ")
	r := bufio.NewReader(os.Stdin)
	s, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

const (
	letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits  = "0123456789"
	pool    = letters + digits + "!@#$%^&*-_=+"
)

// randomPassword 生成 16 位随机强密码：在随机位置安放一个字母与一个数字，
// 其余位从全字符集（含符号）抽取，保证同时满足"含字母 + 含数字"强度规则。
func randomPassword() (string, error) {
	const n = 16
	buf := make([]byte, n)
	for i := range buf {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
		if err != nil {
			return "", err
		}
		buf[i] = pool[v.Int64()]
	}
	// 用两个随机位置兜底字母与数字，避免极端情况下全随机结果不满足规则。
	li, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return "", err
	}
	di, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return "", err
	}
	if li.Int64() == di.Int64() { // 位置冲突时错开一位
		di = big.NewInt((li.Int64() + 1) % n)
	}
	lv, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
	if err != nil {
		return "", err
	}
	dv, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
	if err != nil {
		return "", err
	}
	buf[li.Int64()] = letters[lv.Int64()]
	buf[di.Int64()] = digits[dv.Int64()]
	return string(buf), nil
}
