package llm

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Steve5201/agent-framework/schema"
)

// TestOpenAICompatible_DeepSeekDefaults 验证用 DeepSeek 常量构造客户端的默认配置。
func TestOpenAICompatible_DeepSeekDefaults(t *testing.T) {
	c, err := NewOpenAICompatible(Config{
		Name:    "deepseek",
		BaseURL: DeepSeekBaseURL,
		APIKey:  "sk-test",
		Model:   DeepSeekFlashModel,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatible error = %v", err)
	}
	if c.Name() != "deepseek" {
		t.Errorf("Name() = %q, want deepseek", c.Name())
	}
	if c.baseURL != DeepSeekBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DeepSeekBaseURL)
	}
	if c.model != DeepSeekFlashModel {
		t.Errorf("model = %q, want %q", c.model, DeepSeekFlashModel)
	}
}

// TestOpenAICompatible_EmptyKey 验证空 APIKey 直接报错。
func TestOpenAICompatible_EmptyKey(t *testing.T) {
	if _, err := NewOpenAICompatible(Config{
		BaseURL: DeepSeekBaseURL,
		Model:   DeepSeekFlashModel,
	}); err == nil {
		t.Error("空 APIKey 应报错")
	}
}

// TestOpenAICompatible_Options 验证采样参数等选项覆盖生效。
func TestOpenAICompatible_Options(t *testing.T) {
	temp := 0.7
	topP := 0.9
	maxTok := 4096
	c, err := NewOpenAICompatible(Config{
		Name:        "deepseek",
		BaseURL:     DeepSeekBaseURL,
		APIKey:      "sk-test",
		Model:       DeepSeekProModel,
		Timeout:     10 * time.Second,
		MaxRetries:  5,
		Temperature: &temp,
		TopP:        &topP,
		MaxTokens:   &maxTok,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatible error = %v", err)
	}
	if c.model != DeepSeekProModel {
		t.Errorf("model = %q, want %q", c.model, DeepSeekProModel)
	}
	if c.timeout != 10*time.Second {
		t.Errorf("timeout = %v", c.timeout)
	}
	if c.maxRetries != 5 {
		t.Errorf("maxRetries = %d", c.maxRetries)
	}
	if c.temperature == nil || *c.temperature != 0.7 {
		t.Errorf("temperature = %v", c.temperature)
	}
	if c.topP == nil || *c.topP != 0.9 {
		t.Errorf("topP = %v", c.topP)
	}
	if c.maxTokens == nil || *c.maxTokens != 4096 {
		t.Errorf("maxTokens = %v", c.maxTokens)
	}
}

// TestMockProvider_Chat 验证 Mock 非流式。
func TestMockProvider_Chat(t *testing.T) {
	p := &MockProvider{Content: "模拟回答"}
	resp, err := p.Chat(t.Context(), &Request{})
	if err != nil {
		t.Fatalf("Chat error = %v", err)
	}
	if resp.Content != "模拟回答" {
		t.Errorf("Content = %q", resp.Content)
	}
}

// TestMockProvider_ChatStream 验证 Mock 流式。
func TestMockProvider_ChatStream(t *testing.T) {
	p := &MockProvider{
		Events: []StreamEvent{
			{Content: "你"},
			{Content: "好"},
			{ToolCalls: []ToolCallDelta{{Index: 0, Name: "calculator", Arguments: `{"a":1}`}}},
		},
	}
	st, err := p.ChatStream(t.Context(), &Request{})
	if err != nil {
		t.Fatalf("ChatStream error = %v", err)
	}
	defer st.Close()

	var text string
	var toolSeen bool
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next error = %v", err)
		}
		text += ev.Content
		if len(ev.ToolCalls) > 0 {
			toolSeen = true
		}
	}
	if text != "你好" {
		t.Errorf("拼接文本 = %q, want 你好", text)
	}
	if !toolSeen {
		t.Error("应观察到工具调用事件")
	}
}

// TestMockProvider_Error 验证 Mock 模拟错误。
func TestMockProvider_Error(t *testing.T) {
	p := &MockProvider{Err: errors.New("模拟失败")}
	if _, err := p.Chat(t.Context(), &Request{}); err == nil {
		t.Error("应返回模拟错误")
	}
	if _, err := p.ChatStream(t.Context(), &Request{}); err == nil {
		t.Error("ChatStream 应返回模拟错误")
	}
}

// TestSchemaMessage_Mapping 验证 schema 消息可直接转协议消息。
func TestSchemaMessage_Mapping(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是助手"},
		{Role: schema.RoleUser, Content: "你好"},
	}
	out := toOpenAIMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("长度 = %d", len(out))
	}
	if out[0].Role != "system" || out[1].Role != "user" {
		t.Errorf("角色映射错误: %+v", out)
	}
}
