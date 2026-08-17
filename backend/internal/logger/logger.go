// Package logger 提供基于 zap 的统一结构化日志封装。
//
// 所有微服务必须通过本包创建日志实例，保证输出格式一致：
//   - dev 环境：可读的彩色 console 输出，便于本地调试；
//   - prod 环境：JSON 结构化输出，便于采集与检索。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New 创建应用级 zap.Logger。
//
// env 取值 "dev" 或 "prod"，其它值按 dev 处理；
// level 支持 debug/info/warn/error，解析失败时回退为 info 级别。
//
// 环境变量 LOG_FILE：非空时业务日志同时写入该文件（自动创建父目录），
// 与 stdout/stderr 双写。部署服务器时建议设为固定路径便于采集与排障，
// 例如 LOG_FILE=/logs/gateway.log。
//
// 文件句柄由本包统一跟踪，进程优雅关闭时调用 Close 释放（否则在 Windows
// 上会持续占用文件，导致日志轮转/删除失败）。
func New(env, level string) (*zap.Logger, error) {
	zapLevel, err := zapcore.ParseLevel(level)
	if err != nil {
		zapLevel = zapcore.InfoLevel
	}

	// 编码器：prod = JSON；其它 = dev 彩色 console。
	var encoder zapcore.Encoder
	opts := []zap.Option{}
	if env == "prod" {
		encoder = zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
		opts = append(opts, zap.AddStacktrace(zapcore.ErrorLevel))
	} else {
		cfg := zap.NewDevelopmentEncoderConfig()
		cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(cfg)
		opts = append(opts, zap.Development(), zap.AddStacktrace(zapcore.WarnLevel))
	}

	// LOG_FILE：打开文件句柄（追加），与 stdout 一起作为常规输出；
	// 同时加入内部错误输出（stderr），与 zap 的 OutputPaths/ErrorOutputPaths 语义一致。
	var file *os.File
	if f := os.Getenv("LOG_FILE"); f != "" {
		if dir := filepath.Dir(f); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("init logger: 创建 LOG_FILE 目录失败 %s: %w", dir, err)
			}
		}
		wf, err := os.OpenFile(f, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("init logger: 打开 LOG_FILE %s: %w", f, err)
		}
		file = wf
	}

	output := []zapcore.WriteSyncer{zapcore.AddSync(os.Stdout)}
	errOutput := []zapcore.WriteSyncer{zapcore.AddSync(os.Stderr)}
	if file != nil {
		output = append(output, zapcore.AddSync(file))
		errOutput = append(errOutput, zapcore.AddSync(file))
	}

	core := zapcore.NewCore(encoder, zapcore.NewMultiWriteSyncer(output...), zapLevel)
	opts = append(opts,
		zap.ErrorOutput(zapcore.NewMultiWriteSyncer(errOutput...)),
		zap.AddCaller(),
		zap.AddCallerSkip(1), // 跳过本包封装，caller 指向真正调用方
	)
	if file != nil {
		registerCloser(file.Close)
	}
	return zap.New(core, opts...), nil
}

// Must 是 New 的便捷版本：初始化失败时直接 panic。
// 仅允许在服务启动阶段调用，确保日志系统在业务逻辑之前就绪。
func Must(env, level string) *zap.Logger {
	l, err := New(env, level)
	if err != nil {
		panic("init logger: " + err.Error())
	}
	return l
}

// ---------------------------------------------------------------------------
// 文件句柄跟踪（供优雅关闭释放，Windows 上防止日志文件被占用）
// ---------------------------------------------------------------------------

var (
	closerMu sync.Mutex
	closers  []func() error
)

// registerCloser 登记一个文件关闭函数（进程内 logger 单例语义）。
func registerCloser(c func() error) {
	closerMu.Lock()
	defer closerMu.Unlock()
	closers = append(closers, c)
}

// Close 关闭本进程内 logger 打开的所有文件句柄（含 LOG_FILE）。
// 幂等；建议在服务优雅关闭时调用。
func Close() {
	closerMu.Lock()
	defer closerMu.Unlock()
	for _, c := range closers {
		_ = c()
	}
	closers = nil
}
