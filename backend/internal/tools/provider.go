// Package tools 提供智能体工具的"装配层"：ToolProvider 接口 + 注册工具函数。
//
// 职责边界：
//   - framework/tool 包：定义"一个工具长什么样"（Tool 接口）与注册表（Registry）；
//   - 本包：定义"谁能提供一组工具"（ToolProvider），把多种能力源统一装进 Registry；
//   - tools/builtin 子包：内置工具的具体实现（web_search/file_ops/code_executor/calculator）。
//
// 预留扩展（MCP / Skill）：未来接入 MCP 服务器或 Skill 时，只需实现 ToolProvider
// 接口（如 mcpProvider / skillProvider），再交给 RegisterProviders 装配即可，
// 不需要改 framework 与 agentsvc 的注册逻辑。
package tools

import (
	"fmt"

	"github.com/Steve5201/agent-framework/tool"
)

// ToolProvider 工具提供者：任何能力源（内置工具、MCP 服务器、Skill）都实现
// 该接口，由装配方（RegisterProviders）统一把工具注入 Registry。
//
// 设计动机：一个 Agent 的工具集 = 内置工具 + MCP 工具 + Skill 工具的叠加。
// 用"提供者"抽象后，能力按源聚合、按需组合，注册路径单一可控。
type ToolProvider interface {
	// Name 提供者唯一标识（如 "builtin"、"mcp:github"、"skill:code-review"）。
	// 用于日志溯源与排障：某工具注册失败时能定位到是哪个提供者的锅。
	Name() string

	// Tools 返回该提供者提供的全部工具（实现 framework/tool.Tool 接口）。
	Tools() []tool.Tool
}

// RegisterProviders 把多个提供者的工具统一注册进 Registry。
// 任一工具注册失败（如重名冲突）立即返回错误，并标明来源提供者。
func RegisterProviders(reg *tool.Registry, providers ...ToolProvider) error {
	for _, p := range providers {
		if p == nil {
			return fmt.Errorf("tools: 存在 nil 提供者，拒绝注册")
		}
		for _, t := range p.Tools() {
			if err := reg.Register(t); err != nil {
				return fmt.Errorf("tools: 注册提供者 %q 的工具 %q 失败: %w",
					p.Name(), t.Schema().Name, err)
			}
		}
	}
	return nil
}
