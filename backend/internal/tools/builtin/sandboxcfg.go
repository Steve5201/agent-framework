// sandboxcfg.go —— 工具执行上下文的沙盒配置（会话级，agent_admin 设定）。
//
// agentsvc 在 agent.Run / Stream 前用 runCtx 把会话的沙盒配置（网络开关、
// 资源限制覆盖）注入工具执行上下文；沙盒类工具（code_executor /
// fetch_url_render）经 sandboxConfigFromContext 读出，填入 sandbox 请求，
// 实现"同一沙盒服务按会话动态开网/限资源"。
package builtin

import (
	"context"
)

// SandboxConfig 会话级沙盒配置（供沙盒工具构造 ExecRequest 覆盖字段）。
type SandboxConfig struct {
	// NetworkEnabled 是否允许沙盒联网（默认禁网）。仅 agent_admin 可开启。
	NetworkEnabled bool
	// MemoryMB 虚拟内存上限（MB），0 = 回退沙盒服务实例默认。
	MemoryMB int64
	// CPUSeconds CPU 时间上限（秒），0 = 回退实例默认。
	CPUSeconds int64
	// NofileLimit 最大打开文件数，0 = 回退实例默认。
	NofileLimit int64
	// MaxTimeoutSecs 单次执行最大超时（秒），0 = 回退实例默认。
	MaxTimeoutSecs int64
}

type sandboxConfigKey struct{}

// WithSandboxConfig 把会话沙盒配置注入上下文（框架 Run/RunStream 透传给工具）。
func WithSandboxConfig(ctx context.Context, cfg SandboxConfig) context.Context {
	return context.WithValue(ctx, sandboxConfigKey{}, cfg)
}

// SandboxConfigFromContext 从工具执行上下文读取会话沙盒配置。
// 未注入时返回零值（全部回退沙盒实例默认，网络保持禁网）。
func SandboxConfigFromContext(ctx context.Context) SandboxConfig {
	if v, ok := ctx.Value(sandboxConfigKey{}).(SandboxConfig); ok {
		return v
	}
	return SandboxConfig{}
}
