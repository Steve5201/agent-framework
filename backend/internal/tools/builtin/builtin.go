// Package builtin 智能体内置工具集：calculator / web_search / file_ops / code_executor。
//
// 所有工具实现 framework/tool.Tool 接口（Schema + Execute），由本包的
// Builtin 提供者统一交给 internal/tools.RegisterProviders 装配进 Registry。
//
// 权限分级（framework/schema.PermissionLevel）：
//   - calculator    L0 纯计算：无副作用，直接执行；
//   - web_search    L1 只读：只发网络查询，不修改任何本地状态；
//   - file_ops      L2 写：可写文件（含读/列目录/查看信息），需用户确认；
//   - code_executor L3 危险：执行代码，需用户确认 + 黑名单过滤 + 超时隔离。
package builtin

import (
	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-framework/tool"
)

// Builtin 内置工具提供者：唯一 Name = "builtin"，提供四个内置工具。
//
// 字段为可选运行时配置（空 = 默认值）：
//   - WebSearchBackend：web_search 搜索后端（bing 默认 | duckduckgo）；
//   - CodeExecAllowlist：code_executor 命令白名单正则（非空时仅白名单内命令可执行）；
//   - SandboxURL：code_executor 沙盒服务地址（阶段2，空 = 进程内本地执行）。
type Builtin struct {
	WebSearchBackend  string
	CodeExecAllowlist []string
	SandboxURL        string
	// SkillsRoot 技能根目录（注入给 file_ops，用于 @skills/ 只读资源访问）；
	// 空 = file_ops 按默认 <工作目录>/skills 解析（与 skill Provider 默认一致）。
	SkillsRoot string
	// DiskQuota 写 protected/ 前的磁盘配额校验回调（模块三·保护区配额），
	// 透传给 file_ops；nil = 不校验（历史行为）。
	DiskQuota CheckDiskQuota
}

// Name 实现 ToolProvider 接口。
func (Builtin) Name() string { return "builtin" }

// Tools 实现 ToolProvider 接口：返回全部内置工具。
//
// 指针接收器（&WebSearchTool 等）用于携带可注入的运行时配置
// （BaseURL / Root / Timeout / Backend / Allowlist），测试与容器部署时可覆盖默认值。
func (b Builtin) Tools() []tool.Tool {
	return []tool.Tool{
		CalculatorTool{},
		&WebSearchTool{Backend: b.WebSearchBackend},
		&FileOpsTool{SkillsRoot: b.SkillsRoot, SandboxURL: b.SandboxURL, DiskQuota: b.DiskQuota},
		&CodeExecutorTool{Allowlist: b.CodeExecAllowlist, SandboxURL: b.SandboxURL},
	}
}

// 编译期断言：Builtin 实现 ToolProvider 接口。
var _ tools.ToolProvider = Builtin{}
