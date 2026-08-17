package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Steve5201/agent-framework/schema"
)

// 本文件提供两个示例工具：
//   - echo：验证"工具调用链路"是否打通（LLM→ToolCall→执行→回填）；
//   - calculator：一个真正有用的 L0 纯计算工具，P1-52 真实联调将用到。
// 它们也作为"如何写一个工具"的模板：实现 Schema() + Execute() 两个方法。

// ---- Echo 工具 ----

// EchoTool 原样返回输入的文本。
type EchoTool struct{}

// Schema 实现 Tool 接口。
func (EchoTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "echo",
		Description: "原样返回输入的文本。用于测试工具调用链路是否打通。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{"text":{"type":"string"}}
		}`),
		Required:   []string{"text"},
		Permission: schema.PermissionL0Pure,
	}
}

// Execute 实现 Tool 接口。
func (EchoTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("echo: 参数解析失败: %w", err)
	}
	return p.Text, nil
}

// ---- Calculator 工具 ----

// CalculatorTool 四则运算计算器（L0 纯计算，无副作用）。
type CalculatorTool struct{}

// CalculatorArgs 计算器参数：a 与 b 为操作数，op 为运算符。
type CalculatorArgs struct {
	A  float64 `json:"a"`
	B  float64 `json:"b"`
	Op string  `json:"op"`
}

// Schema 实现 Tool 接口。
func (CalculatorTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "calculator",
		Description: "四则运算计算器，支持加(+)、减(-)、乘(*)、除(/)。a 和 b 是操作数，op 是运算符。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"a":{"type":"number"},
				"b":{"type":"number"},
				"op":{"type":"string"}
			}
		}`),
		Required:   []string{"a", "b", "op"},
		Permission: schema.PermissionL0Pure,
	}
}

// Execute 实现 Tool 接口：执行四则运算并返回结果文本。
func (CalculatorTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p CalculatorArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("calculator: 参数解析失败: %w", err)
	}

	var result float64
	switch p.Op {
	case "+":
		result = p.A + p.B
	case "-":
		result = p.A - p.B
	case "*":
		result = p.A * p.B
	case "/":
		if p.B == 0 {
			return "", fmt.Errorf("calculator: 除数不能为 0")
		}
		result = p.A / p.B
	default:
		return "", fmt.Errorf("calculator: 不支持的运算符 %q（仅支持 + - * /）", p.Op)
	}

	// 'f' 格式 + -1 精度：去掉浮点数尾部多余的 0（3 而不是 3.000000）
	return strconv.FormatFloat(result, 'f', -1, 64), nil
}

// 编译期断言：确保示例工具实现了 Tool 接口。
var (
	_ Tool = (*EchoTool)(nil)
	_ Tool = (*CalculatorTool)(nil)
)
