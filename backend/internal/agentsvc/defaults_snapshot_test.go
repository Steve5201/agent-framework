// defaults_snapshot_test.go —— 智能体域默认配置"创建时快照固化"语义的服务级单测。
//
// 覆盖核心语义：
//   - 新建会话（CreateSession）把管理端默认配置一次性合并进会话 config 并落库（快照）；
//   - 管理端之后修改默认配置，旧会话读取仍返回创建时的快照（不受影响）；
//   - UpdateSessionConfig 全量替换用户可配字段，且保留快照中的管理员级字段；
//   - 管理端域（agentID==""）不受默认影响；
//   - 默认配置校验失败时跳过快照，不阻断会话创建。
package agentsvc

import (
	"context"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"go.uber.org/zap"
)

// stubDomainViewer 内存版 DomainViewer：Defaults 返回可变的默认配置表，
// 用于模拟管理端保存默认配置。
type stubDomainViewer struct {
	defaults map[string]AgentDefaults
}

func (d *stubDomainViewer) Skills(string) []ResourceInfo { return nil }
func (d *stubDomainViewer) McpTools(string) []ToolInfo   { return nil }
func (d *stubDomainViewer) Defaults(agentID string) AgentDefaults {
	if d == nil || d.defaults == nil {
		return AgentDefaults{}
	}
	return d.defaults[agentID]
}

// newTestServiceWithDomain 构造带 DomainViewer 的 Service（快照语义测试用）。
// 注册表用 stubRegistry（含 kb_search / mcp_github 等工具），保证默认配置里
// 引用的资源与 MCP server 能通过 validateConfig。
func newTestServiceWithDomain(repo Repository, p llm.Provider, dv DomainViewer) (*Service, error) {
	return NewService(Config{
		Repo:         repo,
		Provider:     p,
		Registry:     stubRegistry(),
		Log:          zap.NewNop(),
		Model:        "test-model",
		SystemPrompt: "你是测试助手。",
		MaxRounds:    8,
		MaxMessages:  20,
		DomainView:   dv,
	})
}

// TestService_Defaults_SnapshotOnCreate 创建时快照固化：
// 新建会话即把默认配置合并进 config 并落库；管理端后续修改默认配置，
// 旧会话 GetSession/ListSessions 仍返回创建时的快照（不受影响）。
func TestService_Defaults_SnapshotOnCreate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	dv := &stubDomainViewer{defaults: map[string]AgentDefaults{
		"agent-1": {
			EnabledResources:    []string{"search"},
			Thinking:            &ThinkingConfig{Enabled: true, ReasoningEffort: "low"},
			MCPServers:          []string{"github"},
			MaxRounds:           16,
			MaxMessages:         40,
			MaxThinkingRounds:   12,
		},
	}}
	svc, err := newTestServiceWithDomain(repo, &llm.MockProvider{}, dv)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// 创建会话：默认配置合并进 config，并落库固化（快照）。
	s, err := svc.CreateSession(ctx, 1, "agent-1", "快照")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(s.Config.EnabledResources) != 1 || s.Config.EnabledResources[0] != "search" {
		t.Fatalf("创建响应应合并默认资源: %+v", s.Config)
	}
	if s.Config.Thinking == nil || !s.Config.Thinking.Enabled || s.Config.Thinking.ReasoningEffort != "low" {
		t.Fatalf("创建响应应合并默认思考配置: %+v", s.Config.Thinking)
	}
	if len(s.Config.MCPServers) != 1 || s.Config.MCPServers[0] != "github" {
		t.Fatalf("创建响应应合并默认 MCP: %+v", s.Config.MCPServers)
	}
	if s.Config.MaxRounds != 16 || s.Config.MaxMessages != 40 || s.Config.MaxThinkingRounds != 12 {
		t.Fatalf("创建响应应合并管理员级默认: %+v", s.Config)
	}

	// 落库态 = 快照（默认配置已固化，不是空的）。
	raw, _ := repo.GetSession(ctx, s.ID)
	if len(raw.Config.EnabledResources) != 1 || raw.Config.Thinking == nil {
		t.Fatalf("落库态应固化默认配置快照: %+v", raw.Config)
	}

	// 管理端修改默认配置 → 旧会话读取仍返回创建时的快照（不受影响）。
	dv.defaults["agent-1"] = AgentDefaults{
		EnabledResources: []string{"search", "file"},
		KBIDs:            []string{"kb_1"},
	}
	got, err := svc.GetSession(ctx, 1, s.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.Config.EnabledResources) != 1 || got.Config.EnabledResources[0] != "search" {
		t.Fatalf("旧会话应保留创建时快照，不跟随默认变更: %+v", got.Config.EnabledResources)
	}
	if len(got.Config.KBIDs) != 0 {
		t.Fatalf("旧会话不应获得新增默认知识库: %+v", got.Config.KBIDs)
	}

	// 列表视图同样返回创建时的快照。
	if err := repo.AppendMessages(ctx, s.ID, []*Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	list, _, err := svc.ListSessions(ctx, 1, "agent-1", 1, 20)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 || len(list[0].Config.EnabledResources) != 1 {
		t.Fatalf("列表视图应返回快照配置: %+v", list)
	}

	// 新会话使用修改后的默认配置（管理端改默认只影响后续新建会话）。
	s2, err := svc.CreateSession(ctx, 1, "agent-1", "新会话")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(s2.Config.EnabledResources) != 2 || len(s2.Config.KBIDs) != 1 {
		t.Fatalf("新会话应使用最新默认配置: %+v", s2.Config)
	}
}

// TestService_UpdateSessionConfig_Snapshot 快照语义下的配置更新：
// 全量替换用户可配字段；管理员级字段（max_rounds/max_messages/
// max_thinking_rounds）保留快照原值，用户传入的值被忽略（服务端权威）。
func TestService_UpdateSessionConfig_Snapshot(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	dv := &stubDomainViewer{defaults: map[string]AgentDefaults{
		"agent-1": {
			EnabledResources: []string{"search"},
			MaxRounds:        16,
			MaxMessages:      40,
		},
	}}
	svc, err := newTestServiceWithDomain(repo, &llm.MockProvider{}, dv)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s, err := svc.CreateSession(ctx, 1, "agent-1", "快照更新")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 用户更新资源选择（含试图篡改管理员级字段的值）→ 落库全量替换用户可配字段，
	// 管理员级字段仍保留快照原值。
	got, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledResources:    []string{"search", "file"},
		EnabledResourcesSet: true,
		MaxRounds:           99,
		MaxMessages:         99,
		MaxThinkingRounds:   99,
	})
	if err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if len(got.Config.EnabledResources) != 2 {
		t.Fatalf("用户资源选择应生效: %+v", got.Config)
	}
	if !got.Config.EnabledResourcesSet {
		t.Fatalf("set 标记应保留: %+v", got.Config)
	}
	if got.Config.MaxRounds != 16 || got.Config.MaxMessages != 40 || got.Config.MaxThinkingRounds != 0 {
		t.Fatalf("管理员级字段应保留快照原值: %+v", got.Config)
	}

	// 落库态与响应一致。
	raw, _ := repo.GetSession(ctx, s.ID)
	if raw.Config.MaxRounds != 16 || len(raw.Config.EnabledResources) != 2 {
		t.Fatalf("落库态应一致: %+v", raw.Config)
	}
}

// TestService_Defaults_AdminDomainUnaffected 管理端域（agentID==""）
// 不受智能体默认配置影响：创建不合并默认、不落库快照。
func TestService_Defaults_AdminDomainUnaffected(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	dv := &stubDomainViewer{defaults: map[string]AgentDefaults{
		"agent-1": {EnabledResources: []string{"search"}},
	}}
	svc, err := newTestServiceWithDomain(repo, &llm.MockProvider{}, dv)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s, err := svc.CreateSession(ctx, 1, "", "管理端会话")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(s.Config.EnabledResources) != 0 {
		t.Fatalf("管理端域不应合并智能体默认配置: %+v", s.Config)
	}
	// 管理端域更新配置应全量落库（无快照管理员级字段可保留）。
	if _, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledResources: []string{"search"},
	}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	raw, _ := repo.GetSession(ctx, s.ID)
	if len(raw.Config.EnabledResources) != 1 {
		t.Fatalf("管理端域更新应全量落库: %+v", raw.Config)
	}
}

// TestService_Defaults_InvalidSnapshotSkipped 域默认配置合并后校验失败时，
// 跳过快照固化，不阻断会话创建（默认值可选，显式配置优先）。
func TestService_Defaults_InvalidSnapshotSkipped(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	dv := &stubDomainViewer{defaults: map[string]AgentDefaults{
		"agent-1": {EnabledResources: []string{"not-a-skill"}},
	}}
	svc, err := newTestServiceWithDomain(repo, &llm.MockProvider{}, dv)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s, err := svc.CreateSession(ctx, 1, "agent-1", "回退")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(s.Config.EnabledResources) != 0 {
		t.Fatalf("非法默认合并应跳过快照: %+v", s.Config)
	}
}

// TestService_UpdateSessionConfig_PreservesMissingResources 防御性合并：
// 更新配置时若未提交资源类字段（enabled_resources 为 nil 且 set 未置位，
// 模拟其它弹窗只改单一字段 / 历史 base 不完整），资源白名单应保留快照现值，
// 不被意外清空——修复"配置保存可能清空 enabled_resources"的稳定性问题。
func TestService_UpdateSessionConfig_PreservesMissingResources(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	dv := &stubDomainViewer{defaults: map[string]AgentDefaults{
		"agent-1": {
			EnabledResources:       []string{"search", "file"},
			EnabledResourcesSet:    true,
			EnabledCapabilitiesSet: true,
			EnabledSkillsSet:       true,
		},
	}}
	svc, err := newTestServiceWithDomain(repo, &llm.MockProvider{}, dv)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s, err := svc.CreateSession(ctx, 1, "agent-1", "合并保资源")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(s.Config.EnabledResources) != 2 {
		t.Fatalf("快照应含默认资源: %+v", s.Config)
	}

	// 只改 model，不提交资源字段 → 资源白名单应保留（不因全量替换清空）。
	got, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{Model: "m2"})
	if err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if len(got.Config.EnabledResources) != 2 {
		t.Fatalf("未提交资源字段应保留快照白名单: %+v", got.Config)
	}
	if got.Config.Model != "m2" {
		t.Fatalf("model 应生效: %+v", got.Config)
	}

	// 显式全不选（set=true + 空）→ 清空资源白名单（用户主动关闭，仍应生效）。
	got2, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledResources:    []string{},
		EnabledResourcesSet: true,
	})
	if err != nil {
		t.Fatalf("UpdateSessionConfig 全不选: %v", err)
	}
	if len(got2.Config.EnabledResources) != 0 || !got2.Config.EnabledResourcesSet {
		t.Fatalf("显式全不选应清空白名单: %+v", got2.Config)
	}

	// 显式全选（非空列表 + set false）→ 覆盖为全量白名单。
	got3, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledResources: []string{"search", "file", "calculate"},
	})
	if err != nil {
		t.Fatalf("UpdateSessionConfig 全选: %v", err)
	}
	if len(got3.Config.EnabledResources) != 3 {
		t.Fatalf("全选非空列表应覆盖白名单: %+v", got3.Config)
	}
}

// TestService_OperationLogs 操作日志（P6 排查）：UpdateSessionConfig 每次变更
// 落一条改前/改后快照；newAgentWithConfig 每次注入落一条工具名快照。
func TestService_OperationLogs(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	dv := &stubDomainViewer{defaults: map[string]AgentDefaults{
		"agent-1": {
			EnabledResources:       []string{"search", "file"},
			EnabledResourcesSet:    true,
			EnabledCapabilitiesSet: true,
			EnabledSkillsSet:       true,
		},
	}}
	svc, err := newTestServiceWithDomain(repo, &llm.MockProvider{}, dv)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s, err := svc.CreateSession(ctx, 1, "agent-1", "操作日志")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 变更前无日志；第一次变更落一条改前/改后。
	if _, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{Model: "m2"}); err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	if len(repo.configLogs) != 1 {
		t.Fatalf("首次变更应落 1 条配置日志，实际 %d", len(repo.configLogs))
	}
	cl := repo.configLogs[0]
	if cl.SessionID != s.ID || cl.UserID != 1 {
		t.Fatalf("配置日志归属错误: %+v", cl)
	}
	if len(cl.Before.EnabledResources) != 2 || len(cl.After.EnabledResources) != 2 {
		t.Fatalf("配置日志应记录资源白名单（改前/改后均 2 项）: before=%+v after=%+v", cl.Before, cl.After)
	}
	if cl.After.Model != "m2" {
		t.Fatalf("配置日志改后应含新 model: %+v", cl.After)
	}

	// 第二次变更（显式全不选）→ 再落一条。
	if _, err := svc.UpdateSessionConfig(ctx, 1, s.ID, SessionConfig{
		EnabledResources:    []string{},
		EnabledResourcesSet: true,
	}); err != nil {
		t.Fatalf("UpdateSessionConfig 全不选: %v", err)
	}
	if len(repo.configLogs) != 2 {
		t.Fatalf("二次变更应落 2 条配置日志，实际 %d", len(repo.configLogs))
	}
	if len(repo.configLogs[1].Before.EnabledResources) != 2 || len(repo.configLogs[1].After.EnabledResources) != 0 {
		t.Fatalf("全不选日志改前应 2 项、改后应 0 项: before=%+v after=%+v",
			repo.configLogs[1].Before, repo.configLogs[1].After)
	}

	// 工具注入快照：创建会话不会触发（无对话）；newAgentWithHistory 触发一次。
	beforeSnap := len(repo.toolSnapshots)
	if _, err := svc.newAgentWithHistory(ctx, s.ID); err != nil {
		t.Fatalf("newAgentWithHistory: %v", err)
	}
	if len(repo.toolSnapshots) != beforeSnap+1 {
		t.Fatalf("应新增 1 条工具快照，实际 %d→%d", beforeSnap, len(repo.toolSnapshots))
	}
	last := repo.toolSnapshots[len(repo.toolSnapshots)-1]
	if last.SessionID != s.ID || last.UserID != 1 {
		t.Fatalf("工具快照归属错误: %+v", last)
	}
	// 全不选后：能力 set=true 且空 → 能力工具为空；技能 set=true 且空 → 技能工具为空。
	if len(last.Tools) != 0 {
		t.Fatalf("全不选后工具快照应为空，实际 %v", last.Tools)
	}
}

// TestService_Defaults_MCPAllNone 快照 + MCP 全不选（set=true 空列表）：
// 会话装配时 mcp_ 工具全部过滤（只保留非 MCP 工具）。
func TestService_Defaults_MCPAllNone(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	dv := &stubDomainViewer{defaults: map[string]AgentDefaults{
		"agent-1": {MCPServersSet: true},
	}}
	svc, err := newTestServiceWithDomain(repo, &llm.MockProvider{}, dv)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s, err := svc.CreateSession(ctx, 1, "agent-1", "MCP全不选")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !s.Config.MCPServersSet {
		t.Fatalf("快照应含 MCP set 标记: %+v", s.Config)
	}
	// 装配层面：filterSessionTools 在 MCP 全不选时丢弃全部 mcp_ 工具。
	reg, err := DefaultToolSet()
	if err != nil {
		t.Fatalf("DefaultToolSet: %v", err)
	}
	got := svc.filterSessionTools(reg, s.Config)
	for _, ts := range got.Schemas() {
		if _, isMcp := mcpServerOf(ts.Name); isMcp {
			t.Fatalf("MCP 全不选不应装配 mcp_ 工具: %s", ts.Name)
		}
	}
}
