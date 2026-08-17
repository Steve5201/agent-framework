package mcp

import (
	"encoding/json"
	"testing"
)

// TestToolInfoListCompat 验证 discovered_tools 字段新旧两种格式的兼容性：
// 旧配置文件里是字符串数组 ["add","subtract"]，升级后按对象数组读取不应损坏配置。
func TestToolInfoListCompat(t *testing.T) {
	// 旧格式：字符串数组 → 自动迁移为 name，description 留空。
	oldJSON := `{"name":"calculator-mcp","transport":"stdio","command":"python3","args":["main.py"],"discovered_tools":["add","subtract"]}`
	var cfg ServerConfig
	if err := json.Unmarshal([]byte(oldJSON), &cfg); err != nil {
		t.Fatalf("旧格式 discovered_tools 解析失败: %v", err)
	}
	if len(cfg.DiscoveredTools) != 2 {
		t.Fatalf("迁移后应含 2 个工具，got %d", len(cfg.DiscoveredTools))
	}
	if cfg.DiscoveredTools[0].Name != "add" || cfg.DiscoveredTools[0].Description != "" {
		t.Fatalf("旧格式迁移结果不正确: %+v", cfg.DiscoveredTools[0])
	}

	// 新格式：对象数组。
	newJSON := `{"name":"greeting-mcp","discovered_tools":[{"name":"greet","description":"打招呼"},{"name":"farewell","description":"道别"}]}`
	if err := json.Unmarshal([]byte(newJSON), &cfg); err != nil {
		t.Fatalf("新格式 discovered_tools 解析失败: %v", err)
	}
	if cfg.DiscoveredTools[1].Description != "道别" {
		t.Fatalf("新格式 description 未保留: %+v", cfg.DiscoveredTools[1])
	}

	// 序列化统一输出新格式（旧格式迁移后落盘即为新格式）。
	cfg.DiscoveredTools = ToolInfoList{{Name: "add"}, {Name: "subtract"}}
	out, err := json.Marshal(cfg.DiscoveredTools)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	var round []map[string]any
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("Marshal 结果非法: %v", err)
	}
	_ = round // 结构由类型保证；若误输出字符串数组则 Unmarshal 到 []map 会失败（由上方 ok）

	// 非法类型（如数字）应报错而非静默通过。
	bad := []byte(`{"discovered_tools":[1,2]}`)
	var badCfg ServerConfig
	if err := json.Unmarshal(bad, &badCfg); err == nil {
		t.Fatal("非法 discovered_tools 类型应报错")
	}
}
