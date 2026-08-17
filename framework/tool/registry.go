package tool

import (
	"context"
	"fmt"
	"sync"

	"github.com/Steve5201/agent-framework/schema"
)

// Registry 工具注册表：管理一组工具，供 Agent 查询与执行。
//
// 并发安全：使用 sync.RWMutex。因为 LLM 可能并行发起多个工具调用
// （一次回复里带多个 tool_calls），读写锁保证并发安全且读多写少时性能好。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// maxToolArguments 单次工具调用参数上限（32MB）。
// 通用防内存耗尽安全阀：正常工具参数远小于此（render_html 直传上限 300KB），
// 超限即异常——明确报错而非静默截断，保证失败可诊断。
const maxToolArguments = 32 << 20

// Register 注册一个工具。
// 规则：工具名必须非空且唯一，重复注册直接报错——
// 防止意外覆盖已有工具（覆盖往往是配置错误的信号）。
func (r *Registry) Register(t Tool) error {
	ts := t.Schema()
	if ts.Name == "" {
		return fmt.Errorf("tool: 工具名不能为空")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[ts.Name]; ok {
		return fmt.Errorf("tool: 工具 %q 已注册，不允许重复注册", ts.Name)
	}
	r.tools[ts.Name] = t
	return nil
}

// Get 按名称查找工具。未注册时返回明确错误。
func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool: 工具 %q 未注册", name)
	}
	return t, nil
}

// SchemaByName 按名称返回工具说明书（不执行）。
// 供 agent 循环在执行前判断执行模式（如 External 外部代理工具）。
func (r *Registry) SchemaByName(name string) (schema.ToolSchema, error) {
	t, err := r.Get(name)
	if err != nil {
		return schema.ToolSchema{}, err
	}
	return t.Schema(), nil
}

// Schemas 返回所有工具的说明书，供 agent 组装后发给 LLM。
// （LLM 看到这些说明才知道有哪些工具可用、长什么样。）
func (r *Registry) Schemas() []schema.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]schema.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Schema())
	}
	return out
}

// Names 返回已注册工具名列表（日志与调试用）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	return out
}

// Execute 执行一次工具调用（B3 的核心闭环）。
//
// 流程：查工具 → 权限确认检查 → 参数校验 → 执行 → 包装结果。
// 入参：
//   - call：LLM 发来的调用指令（ToolCall）；
//   - approved：用户是否已确认本次调用（L2/L3 工具必须确认）。
//
// 返回值约定：
//   - 校验/权限错误：返回 error（调用方应中止流程）；
//   - 工具本身执行失败：返回 IsError=true 的 ToolResult（失败原因回填给
//     LLM，让它能调整策略重试），此时 error 为 nil。
func (r *Registry) Execute(ctx context.Context, call schema.ToolCall, approved bool) (*schema.ToolResult, error) {
	t, err := r.Get(call.Name)
	if err != nil {
		return nil, err
	}
	ts := t.Schema()

	// 安全钩子：需要用户确认的工具，未经确认直接拒绝。
	// （L2/L3 高级别工具的执行与沙盒在 P4 由宿主层完善。）
	if ts.RequiresApproval() && !approved {
		return nil, fmt.Errorf("tool: 工具 %q 需要用户确认后才能执行（权限级别 %s）", call.Name, ts.Permission)
	}

	// 参数总量上限：防止极端超大参数耗尽内存（工具参数动辄数百 KB，
	// 但不应无界增长；超限明确报错而非静默截断，保证失败可诊断）。
	if len(call.Arguments) > maxToolArguments {
		return nil, fmt.Errorf("tool: 工具 %q 参数过大（%d 字节，上限 %d 字节）", call.Name, len(call.Arguments), maxToolArguments)
	}

	// 参数校验：必填项是否存在、类型是否匹配。
	if err := ValidateArgs(ts, call.Arguments); err != nil {
		return nil, err
	}

	// 执行真实逻辑。
	content, err := t.Execute(ctx, call.Arguments)
	if err != nil {
		// 失败也回填给 LLM，让它知道工具没跑通。
		return &schema.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("工具 %s 执行失败: %v", call.Name, err),
			IsError:    true,
		}, nil
	}

	return &schema.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    content,
	}, nil
}
