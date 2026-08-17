package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestNew_Dev 验证 dev 环境能正常创建并写日志。
func TestNew_Dev(t *testing.T) {
	l, err := New("dev", "debug")
	if err != nil {
		t.Fatalf("New(dev, debug) error = %v", err)
	}
	if l == nil {
		t.Fatal("New(dev, debug) 返回了 nil logger")
	}
	l.Info("logger dev 测试")
}

// TestNew_Prod 验证 prod 环境（JSON 输出）能正常创建。
func TestNew_Prod(t *testing.T) {
	l, err := New("prod", "info")
	if err != nil {
		t.Fatalf("New(prod, info) error = %v", err)
	}
	l.Info("logger prod 测试", zap.String("k", "v"))
}

// TestNew_InvalidLevel 验证非法级别回退到 info 而不报错。
func TestNew_InvalidLevel(t *testing.T) {
	l, err := New("prod", "not-a-level")
	if err != nil {
		t.Fatalf("New 对非法级别应回退而非报错，实际 error = %v", err)
	}
	l.Info("fallback 测试")
}

// TestMust_Ok 验证合法参数下 Must 不 panic。
func TestMust_Ok(t *testing.T) {
	l := Must("dev", "info")
	if l == nil {
		t.Fatal("Must 返回了 nil logger")
	}
}

// TestNew_LogFile 验证 LOG_FILE 环境变量生效：日志同时写入文件（自动建父目录）。
func TestNew_LogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	t.Setenv("LOG_FILE", path)

	l, err := New("prod", "info")
	if err != nil {
		t.Fatalf("New 带 LOG_FILE error = %v", err)
	}
	l.Info("logfile 双写测试", zap.String("k", "v"))
	_ = l.Sync()
	// 关闭文件句柄，避免 Windows 上 TempDir 清理时文件被占用。
	Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("LOG_FILE 文件未生成: %v", err)
	}
	if !strings.Contains(string(data), "logfile 双写测试") {
		t.Fatalf("日志文件内容不含预期消息: %s", data)
	}
}
