package schema

import (
	"encoding/json"
	"testing"
)

// TestAgentConfig_Validate 验证配置合法性校验。
func TestAgentConfig_Validate(t *testing.T) {
	// 合法配置
	ok := AgentConfig{Model: "deepseek-v4-flash", MaxRounds: 5}
	if err := ok.Validate(); err != nil {
		t.Errorf("合法配置不应报错: %v", err)
	}

	// Model 为空
	if err := (AgentConfig{MaxRounds: 5}).Validate(); err == nil {
		t.Error("Model 为空应报错")
	}

	// MaxRounds <= 0
	if err := (AgentConfig{Model: "deepseek-v4-flash", MaxRounds: 0}).Validate(); err == nil {
		t.Error("MaxRounds=0 应报错")
	}
}

// TestAgentConfig_JSON 验证配置的序列化/反序列化往返完整。
func TestAgentConfig_JSON(t *testing.T) {
	cfg := AgentConfig{
		Model:        "deepseek-v4-flash",
		SystemPrompt: "你是教学助手",
		MaxRounds:    5,
		Memory:       MemoryConfig{MaxMessages: 20, UseLongTerm: true},
	}

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var got AgentConfig
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.Model != "deepseek-v4-flash" || got.MaxRounds != 5 {
		t.Errorf("配置反序列化异常: %+v", got)
	}
	if got.SystemPrompt != "你是教学助手" {
		t.Errorf("SystemPrompt = %q", got.SystemPrompt)
	}
	if !got.Memory.UseLongTerm || got.Memory.MaxMessages != 20 {
		t.Errorf("Memory 配置反序列化异常: %+v", got.Memory)
	}
}

// TestMemoryConfig_ZeroValue 验证零值内存配置不会导致 panic（向后兼容）。
func TestMemoryConfig_ZeroValue(t *testing.T) {
	var m MemoryConfig
	if m.MaxMessages != 0 || m.UseLongTerm {
		t.Errorf("零值 MemoryConfig 异常: %+v", m)
	}
}
