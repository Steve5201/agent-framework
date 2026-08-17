package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
)

// TestRegistry_RegisterAndGet 验证注册与查找。
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(CalculatorTool{}); err != nil {
		t.Fatalf("Register error = %v", err)
	}

	got, err := r.Get("calculator")
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if got.Schema().Name != "calculator" {
		t.Errorf("Schema().Name = %q", got.Schema().Name)
	}
}

// TestRegistry_RegisterDuplicate 验证重复注册报错。
func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(CalculatorTool{})
	if err := r.Register(CalculatorTool{}); err == nil {
		t.Error("重复注册应报错")
	}
}

// TestRegistry_GetNotFound 验证查找未注册工具报错。
func TestRegistry_GetNotFound(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Get("nonexistent"); err == nil {
		t.Error("查找未注册工具应报错")
	}
}

// TestRegistry_Schemas 验证 Schemas 返回全部说明书。
func TestRegistry_Schemas(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(EchoTool{})
	_ = r.Register(CalculatorTool{})

	schemas := r.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("Schemas 数量 = %d, want 2", len(schemas))
	}
	// 每个说明书必须有名字
	for _, s := range schemas {
		if s.Name == "" {
			t.Error("存在无名字的工具说明书")
		}
	}
}

// TestValidateArgs_MissingRequired 验证缺必填报错。
func TestValidateArgs_MissingRequired(t *testing.T) {
	ts := CalculatorTool{}.Schema()
	// 缺 op
	err := ValidateArgs(ts, json.RawMessage(`{"a":1,"b":2}`))
	if err == nil {
		t.Error("缺少必填参数应报错")
	}
}

// TestValidateArgs_TypeMismatch 验证类型不匹配报错。
func TestValidateArgs_TypeMismatch(t *testing.T) {
	ts := CalculatorTool{}.Schema()
	// a 传了字符串
	err := ValidateArgs(ts, json.RawMessage(`{"a":"x","b":2,"op":"+"}`))
	if err == nil {
		t.Error("类型不匹配应报错")
	}
}

// TestValidateArgs_OK 验证合法参数通过。
func TestValidateArgs_OK(t *testing.T) {
	ts := CalculatorTool{}.Schema()
	err := ValidateArgs(ts, json.RawMessage(`{"a":1,"b":2,"op":"+"}`))
	if err != nil {
		t.Errorf("合法参数不应报错: %v", err)
	}
}

// TestValidateArgs_Integer 验证整数类型判断。
func TestValidateArgs_Integer(t *testing.T) {
	ts := schema.ToolSchema{
		Name:       "t",
		Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
		Required:   []string{"n"},
	}
	if err := ValidateArgs(ts, json.RawMessage(`{"n":5}`)); err != nil {
		t.Errorf("整数参数应通过: %v", err)
	}
	if err := ValidateArgs(ts, json.RawMessage(`{"n":5.5}`)); err == nil {
		t.Error("小数不应通过 integer 校验")
	}
}

// TestExecute_Calculator 验证 Registry.Execute 完整闭环。
func TestExecute_Calculator(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(CalculatorTool{})

	result, err := r.Execute(context.Background(), schema.ToolCall{
		ID:        "call_1",
		Name:      "calculator",
		Arguments: json.RawMessage(`{"a":12,"b":13,"op":"*"}`),
	}, false) // L0 工具不需要确认
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.Content != "156" {
		t.Errorf("Content = %q, want 156", result.Content)
	}
	if result.ToolCallID != "call_1" {
		t.Errorf("ToolCallID = %q, want call_1（结果必须与调用配对）", result.ToolCallID)
	}
	if result.IsError {
		t.Error("成功执行不应标记 IsError")
	}
}

// TestExecute_UnknownTool 验证调用未注册工具报错。
func TestExecute_UnknownTool(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Execute(context.Background(), schema.ToolCall{Name: "nope"}, false); err == nil {
		t.Error("未注册工具应报错")
	}
}

// TestExecute_RequiresApproval 验证 L2 以上工具未确认时拒绝。
func TestExecute_RequiresApproval(t *testing.T) {
	// 构造一个 L3 危险工具（模拟未来宿主层的写文件工具）
	dangerTool := &permissionTool{name: "rm_file", perm: schema.PermissionL3Dangerous}
	r := NewRegistry()
	_ = r.Register(dangerTool)

	call := schema.ToolCall{ID: "c1", Name: "rm_file", Arguments: json.RawMessage(`{}`)}
	if _, err := r.Execute(context.Background(), call, false); err == nil {
		t.Error("L3 工具未经确认应拒绝")
	}
	if _, err := r.Execute(context.Background(), call, true); err != nil {
		t.Errorf("L3 工具经确认后应可执行: %v", err)
	}
}

// TestExecute_ToolFailure 验证工具执行失败时返回 IsError 结果而非中断。
func TestExecute_ToolFailure(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(CalculatorTool{})

	// 除零会触发工具内部错误
	result, err := r.Execute(context.Background(), schema.ToolCall{
		ID:        "c2",
		Name:      "calculator",
		Arguments: json.RawMessage(`{"a":1,"b":0,"op":"/"}`),
	}, false)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !result.IsError {
		t.Error("执行失败应标记 IsError=true")
	}
	if result.Content == "" {
		t.Error("失败结果应包含错误说明（回填给 LLM）")
	}
}

// TestExecute_OversizeArguments 验证超限参数明确报错（防静默截断/内存耗尽）。
func TestExecute_OversizeArguments(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(CalculatorTool{})

	huge := strings.Repeat("a", maxToolArguments+1)
	if _, err := r.Execute(context.Background(), schema.ToolCall{
		ID:        "c4",
		Name:      "calculator",
		Arguments: json.RawMessage(huge),
	}, false); err == nil {
		t.Fatal("超限参数应明确报错")
	} else if !strings.Contains(err.Error(), "参数过大") {
		t.Errorf("错误信息应可诊断: %v", err)
	}

	// 边界内正常执行不受影响。
	if _, err := r.Execute(context.Background(), schema.ToolCall{
		ID:        "c5",
		Name:      "calculator",
		Arguments: json.RawMessage(`{"a":1,"b":2,"op":"*"}`),
	}, false); err != nil {
		t.Errorf("正常参数不应受影响: %v", err)
	}
}

// TestEchoTool 验证 echo 工具。
func TestEchoTool(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(EchoTool{})

	result, err := r.Execute(context.Background(), schema.ToolCall{
		ID:        "c3",
		Name:      "echo",
		Arguments: json.RawMessage(`{"text":"你好世界"}`),
	}, false)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.Content != "你好世界" {
		t.Errorf("Content = %q, want 你好世界", result.Content)
	}
}

// permissionTool 测试专用：带任意权限级别的工具。
type permissionTool struct {
	name string
	perm schema.PermissionLevel
}

func (p *permissionTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:       p.name,
		Permission: p.perm,
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (p *permissionTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "done", nil
}
