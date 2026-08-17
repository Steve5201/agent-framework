package schema

import (
	"encoding/json"
	"testing"
)

// TestRole_Values 验证四个角色值与 LLM 协议字符串一致。
func TestRole_Values(t *testing.T) {
	cases := []struct {
		role Role
		want string
	}{
		{RoleSystem, "system"},
		{RoleUser, "user"},
		{RoleAssistant, "assistant"},
		{RoleTool, "tool"},
	}
	for _, c := range cases {
		if string(c.role) != c.want {
			t.Errorf("Role = %q, want %q", c.role, c.want)
		}
	}
}

// TestMessage_JSON 验证消息的 JSON 序列化格式（与协议对齐）。
func TestMessage_JSON(t *testing.T) {
	// 普通用户消息
	m := Message{Role: RoleUser, Content: "你好"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	want := `{"role":"user","content":"你好"}`
	if string(b) != want {
		t.Errorf("JSON = %s, want %s", b, want)
	}

	// 工具结果消息必须携带 tool_call_id
	tr := Message{Role: RoleTool, Content: "42", ToolCallID: "call_1"}
	b, err = json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	want = `{"role":"tool","content":"42","tool_call_id":"call_1"}`
	if string(b) != want {
		t.Errorf("JSON = %s, want %s", b, want)
	}

	// 无 ToolCallID 的普通消息不应输出该字段（omitempty）
	b, _ = json.Marshal(Message{Role: RoleAssistant, Content: "ok"})
	if string(b) != `{"role":"assistant","content":"ok"}` {
		t.Errorf("普通消息不应带 tool_call_id: %s", b)
	}
}

// TestMessage_IsToolResult 验证角色判断。
func TestMessage_IsToolResult(t *testing.T) {
	if !(Message{Role: RoleTool}).IsToolResult() {
		t.Error("tool 角色应被识别为工具结果")
	}
	if (Message{Role: RoleUser}).IsToolResult() {
		t.Error("user 角色不应被识别为工具结果")
	}
}
