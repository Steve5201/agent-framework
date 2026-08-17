package adminsvc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Steve5201/agent-backend/internal/agentsvc"
)

func TestDefaultsFileFor(t *testing.T) {
	mcpDir := t.TempDir()
	s := &Service{mcp: newMcpStore(filepath.Join(mcpDir, "mcp_servers.json"), filepath.Join(mcpDir, "mcp-servers"))}
	got := s.defaultsFileFor("math")
	want := filepath.Join(mcpDir, "math", "agent_defaults.json")
	if got != want {
		t.Fatalf("defaultsFileFor = %q, want %q", got, want)
	}
}

func TestAgentDefaultsRoundtrip(t *testing.T) {
	s := newDefaultsService(t)

	// 未配置 = 零值（非 404）
	def, err := s.readAgentDefaults("math")
	if err != nil || !def.IsEmpty() {
		t.Fatalf("初始 read = %+v, %v", def, err)
	}

	want := agentsvc.AgentDefaults{
		EnabledTools:     []string{"calculator"},
		EnabledResources: []string{"cap_kb_search"},
		Thinking:         &agentsvc.ThinkingConfig{Enabled: true, ReasoningEffort: "high"},
		KBIDs:            []string{"kb1"},
		MCPServers:       []string{"github"},
	}
	if err := s.writeAgentDefaults("math", want); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := s.readAgentDefaults("math")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Thinking == nil || !got.Thinking.Enabled || got.Thinking.ReasoningEffort != "high" {
		t.Fatalf("thinking 往返异常: %+v", got.Thinking)
	}
	if len(got.KBIDs) != 1 || got.KBIDs[0] != "kb1" {
		t.Fatalf("kb_ids 往返异常: %+v", got.KBIDs)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0] != "github" {
		t.Fatalf("mcp_servers 往返异常: %+v", got.MCPServers)
	}
	if len(got.EnabledTools) != 1 || got.EnabledTools[0] != "calculator" {
		t.Fatalf("enabled_tools 往返异常: %+v", got.EnabledTools)
	}

	// 写零值 = 删除文件（无默认）
	if err := s.writeAgentDefaults("math", agentsvc.AgentDefaults{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(s.defaultsFileFor("math")); !os.IsNotExist(err) {
		t.Fatalf("清空后文件应删除, stat err = %v", err)
	}
	def, err = s.readAgentDefaults("math")
	if err != nil || !def.IsEmpty() {
		t.Fatalf("清空后 read = %+v, %v", def, err)
	}
}

func TestAgentDefaultsCorruptFile(t *testing.T) {
	s := newDefaultsService(t)
	file := s.defaultsFileFor("math")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readAgentDefaults("math"); err == nil {
		t.Fatal("损坏文件应返回错误（提示人工修复，而非静默吞掉）")
	}
}

// newDefaultsService 构造文件态默认配置测试服务（不依赖 auth gRPC）。
func newDefaultsService(t *testing.T) *Service {
	t.Helper()
	mcpDir := t.TempDir()
	return &Service{mcp: newMcpStore(filepath.Join(mcpDir, "mcp_servers.json"), filepath.Join(mcpDir, "mcp-servers"))}
}
