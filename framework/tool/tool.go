// Package tool 提供 Agent 的"能力单元"——工具的注册、校验与执行。
//
// 概念回顾：LLM 不能直接运行代码，它只能"看"到工具的说明书
// （schema.ToolSchema），然后在需要时发出调用请求（schema.ToolCall）。
// 本包负责：
//   - Tool 接口：定义一个工具 = 实现 Schema()（说明书）+ Execute()（执行）；
//   - Registry：工具注册表，注册/查找/执行，并做参数校验与权限确认；
//   - ValidateArgs：按 JSON Schema 校验 LLM 传来的参数（必填/类型）。
//
// 与上层的关系：
//   - B2 的 llm 包把 ToolSchema 发给 LLM，收到 ToolCall；
//   - 本包把 ToolCall 变成真实的执行结果（ToolResult）；
//   - B5 的 agent 循环把二者串起来。
//
// 设计要点：
//  1. 权限安全：ToolSchema.Permission 为 L2/L3 的工具，未经用户确认
//     一律拒绝执行（RequiresApproval 钩子），高级别工具留给 P4 宿主层；
//  2. 失败也回填：工具执行出错时返回 IsError 的 ToolResult 而非中断，
//     让 LLM 知道"没跑通"，从而调整策略；
//  3. 并发安全：Registry 内部用读写锁，支持并行工具调用。
package tool

import (
	"context"
	"encoding/json"

	"github.com/Steve5201/agent-framework/schema"
)

// Tool 一个可被 Agent 调用的能力单元。
//
// 实现一个工具 = 实现两个方法：
//   - Schema()：返回给 LLM 看的说明书（名称/描述/参数/权限）；
//   - Execute()：真正执行的逻辑，入参是 LLM 生成的参数 JSON，
//     返回结果文本（将作为 role=tool 消息回填给 LLM）。
//
// 注意：Execute 应尽量做成"纯函数"，避免副作用；有副作用或需写
// 外部状态的工具，请在 Schema() 中声明更高的 Permission 级别。
type Tool interface {
	Schema() schema.ToolSchema
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// 编译期断言：无；接口由具体类型实现（Go 鸭子类型）。
