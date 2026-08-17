package llmsvc

import (
	"encoding/json"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

func TestToLLMRequest_Basic(t *testing.T) {
	temp := 0.7
	maxTok := 100
	req := &chatCompletionRequest{
		Model: "deepseek-v4-flash",
		Messages: []openAIMessage{
			{Role: "system", Content: "你是一名助手"},
			{Role: "user", Content: "你好"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	}
	lreq, err := toLLMRequest(req, "fallback-model")
	if err != nil {
		t.Fatalf("toLLMRequest error = %v", err)
	}
	if lreq.Model != "deepseek-v4-flash" {
		t.Errorf("Model = %q, want deepseek-v4-flash", lreq.Model)
	}
	if len(lreq.Messages) != 2 || lreq.Messages[0].Role != schema.RoleSystem || lreq.Messages[1].Content != "你好" {
		t.Errorf("Messages 转换异常: %+v", lreq.Messages)
	}
	if lreq.Temperature == nil || *lreq.Temperature != 0.7 {
		t.Error("Temperature 未透传")
	}
	if lreq.MaxTokens == nil || *lreq.MaxTokens != 100 {
		t.Error("MaxTokens 未透传")
	}
}

func TestToLLMRequest_DefaultModel(t *testing.T) {
	req := &chatCompletionRequest{
		Messages: []openAIMessage{{Role: "user", Content: "hi"}},
	}
	lreq, err := toLLMRequest(req, "default-model")
	if err != nil {
		t.Fatalf("toLLMRequest error = %v", err)
	}
	if lreq.Model != "default-model" {
		t.Errorf("Model = %q, want default-model", lreq.Model)
	}
}

func TestToLLMRequest_ToolCalls(t *testing.T) {
	req := &chatCompletionRequest{
		Messages: []openAIMessage{
			{Role: "assistant", Content: "", ToolCalls: []openAIToolCall{{
				ID: "call_1", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "get_weather", Arguments: `{"city":"成都"}`},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: "晴 25°C"},
		},
	}
	lreq, err := toLLMRequest(req, "m")
	if err != nil {
		t.Fatalf("toLLMRequest error = %v", err)
	}
	as := lreq.Messages[0].ToolCalls
	if len(as) != 1 || as[0].ID != "call_1" || as[0].Name != "get_weather" {
		t.Errorf("assistant tool_calls 转换异常: %+v", as)
	}
	if string(as[0].Arguments) != `{"city":"成都"}` {
		t.Errorf("Arguments = %s", as[0].Arguments)
	}
	if lreq.Messages[1].ToolCallID != "call_1" {
		t.Errorf("tool 消息 tool_call_id 未透传: %+v", lreq.Messages[1])
	}
}

func TestToLLMRequest_Tools(t *testing.T) {
	req := &chatCompletionRequest{
		Messages: []openAIMessage{{Role: "user", Content: "hi"}},
		Tools: []openAITool{{
			Type: "function",
			Function: openAIFunction{
				Name:        "get_weather",
				Description: "查天气",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			},
		}},
	}
	lreq, err := toLLMRequest(req, "m")
	if err != nil {
		t.Fatalf("toLLMRequest error = %v", err)
	}
	if len(lreq.Tools) != 1 || lreq.Tools[0].Name != "get_weather" {
		t.Fatalf("Tools 转换异常: %+v", lreq.Tools)
	}
	if lreq.Tools[0].Description != "查天气" {
		t.Errorf("Description = %q", lreq.Tools[0].Description)
	}
	var params map[string]any
	if err := json.Unmarshal(lreq.Tools[0].Parameters, &params); err != nil {
		t.Fatalf("Parameters 不是合法 JSON: %v", err)
	}
	if _, ok := params["properties"]; !ok {
		t.Error("Parameters 缺少 properties")
	}
}

func TestToLLMRequest_Validation(t *testing.T) {
	// 空 messages
	if _, err := toLLMRequest(&chatCompletionRequest{}, "m"); err == nil {
		t.Error("空 messages 应报错")
	}
	// 缺 role
	if _, err := toLLMRequest(&chatCompletionRequest{
		Messages: []openAIMessage{{Content: "x"}},
	}, "m"); err == nil {
		t.Error("缺 role 应报错")
	}
}

// TestToLLMRequest_Reasoning 工具轮思考内容（reasoning_content）须原样
// 回传给上游厂商（DeepSeek 官方要求：工具轮必须携带，否则 400）。
func TestToLLMRequest_Reasoning(t *testing.T) {
	req := &chatCompletionRequest{
		Messages: []openAIMessage{
			{Role: "assistant", Content: "", ReasoningContent: "先查天气",
				ToolCalls: []openAIToolCall{{ID: "call_1", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "get_weather", Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: "call_1", Content: "晴"},
		},
	}
	lreq, err := toLLMRequest(req, "m")
	if err != nil {
		t.Fatalf("toLLMRequest error = %v", err)
	}
	if lreq.Messages[0].Reasoning != "先查天气" {
		t.Errorf("Reasoning 未透传: %+v", lreq.Messages[0])
	}
}

// TestToLLMRequest_Thinking 思考模式透传：thinking.type + reasoning_effort。
func TestToLLMRequest_Thinking(t *testing.T) {
	req := &chatCompletionRequest{
		Messages:        []openAIMessage{{Role: "user", Content: "hi"}},
		Thinking:        &thinkingBody{Type: "disabled"},
		ReasoningEffort: "high",
	}
	lreq, err := toLLMRequest(req, "m")
	if err != nil {
		t.Fatalf("toLLMRequest error = %v", err)
	}
	if lreq.Thinking == nil || lreq.Thinking.Enabled {
		t.Errorf("thinking 未透传/方向错误: %+v", lreq.Thinking)
	}
	if lreq.Thinking.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high", lreq.Thinking.ReasoningEffort)
	}
}

func TestBuildChatCompletionResponse(t *testing.T) {
	resp := &llm.Response{
		Content:      "hi",
		Reasoning:    "先想了一下",
		FinishReason: "stop",
		Usage:        llm.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}
	out := buildChatCompletionResponse("chatcmpl-1", "deepseek-v4-flash", resp)
	if out.Object != "chat.completion" {
		t.Errorf("Object = %q", out.Object)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "hi" {
		t.Errorf("Choices 异常: %+v", out.Choices)
	}
	if out.Choices[0].Message.ReasoningContent != "先想了一下" {
		t.Errorf("ReasoningContent 未输出: %+v", out.Choices[0].Message)
	}
	if out.Usage.TotalTokens != 8 {
		t.Errorf("Usage = %+v", out.Usage)
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q", out.Choices[0].FinishReason)
	}
}
