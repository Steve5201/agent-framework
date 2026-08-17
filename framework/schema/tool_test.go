package schema

import (
	"encoding/json"
	"testing"
)

// TestPermissionLevel_String 验证权限级别的可读名称。
func TestPermissionLevel_String(t *testing.T) {
	cases := []struct {
		p    PermissionLevel
		want string
	}{
		{PermissionL0Pure, "L0_pure"},
		{PermissionL1Read, "L1_read"},
		{PermissionL2Write, "L2_write"},
		{PermissionL3Dangerous, "L3_dangerous"},
	}
	for _, c := range cases {
		if c.p.String() != c.want {
			t.Errorf("String() = %q, want %q", c.p.String(), c.want)
		}
	}
}

// TestPermissionLevel_RequiresApproval 验证 L2/L3 需要确认、L0/L1 直接执行。
func TestPermissionLevel_RequiresApproval(t *testing.T) {
	if PermissionL0Pure.RequiresApproval() {
		t.Error("L0 不需要确认")
	}
	if PermissionL1Read.RequiresApproval() {
		t.Error("L1 不需要确认")
	}
	if !PermissionL2Write.RequiresApproval() {
		t.Error("L2 需要确认")
	}
	if !PermissionL3Dangerous.RequiresApproval() {
		t.Error("L3 需要确认")
	}
}

// TestToolSchema_JSON 验证工具描述的序列化/反序列化往返完整。
func TestToolSchema_JSON(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"}}}`)
	ts := ToolSchema{
		Name:        "calculator",
		Description: "加法计算器",
		Parameters:  params,
		Required:    []string{"a"},
		Permission:  PermissionL0Pure,
	}

	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var got ToolSchema
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.Name != "calculator" || got.Description != "加法计算器" {
		t.Errorf("反序列化字段丢失: %+v", got)
	}
	if len(got.Required) != 1 || got.Required[0] != "a" {
		t.Errorf("Required = %v, want [a]", got.Required)
	}
	if got.Permission != PermissionL0Pure {
		t.Errorf("Permission = %v, want L0", got.Permission)
	}
	if len(got.Parameters) == 0 {
		t.Error("Parameters 不应为空")
	}
}

// TestToolSchema_RequiresApproval 验证按权限级别联动判断。
func TestToolSchema_RequiresApproval(t *testing.T) {
	l0 := ToolSchema{Permission: PermissionL0Pure}
	l3 := ToolSchema{Permission: PermissionL3Dangerous}
	if l0.RequiresApproval() {
		t.Error("L0 工具不应需要确认")
	}
	if !l3.RequiresApproval() {
		t.Error("L3 工具需要确认")
	}
}

// TestToolCall_JSON 验证工具调用指令的往返。
func TestToolCall_JSON(t *testing.T) {
	tc := ToolCall{
		ID:        "call_1",
		Name:      "calculator",
		Arguments: json.RawMessage(`{"a":1,"b":2}`),
	}
	b, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var got ToolCall
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.ID != "call_1" || got.Name != "calculator" {
		t.Errorf("ToolCall 反序列化异常: %+v", got)
	}
	if string(got.Arguments) != `{"a":1,"b":2}` {
		t.Errorf("Arguments = %s", got.Arguments)
	}
}

// TestToolResult_JSON 验证工具结果的往返。
func TestToolResult_JSON(t *testing.T) {
	tr := ToolResult{ToolCallID: "call_1", Name: "calculator", Content: "3"}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var got ToolResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.ToolCallID != "call_1" || got.Content != "3" || got.IsError {
		t.Errorf("ToolResult 反序列化异常: %+v", got)
	}

	// 错误结果应带 IsError=true 且可序列化
	errResult := ToolResult{ToolCallID: "call_2", Name: "x", Content: "boom", IsError: true}
	b, _ = json.Marshal(errResult)
	if string(b) != `{"tool_call_id":"call_2","name":"x","content":"boom","is_error":true}` {
		t.Errorf("错误结果序列化异常: %s", b)
	}
}
