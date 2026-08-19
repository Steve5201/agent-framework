// resources_test.go —— 阶段1·资源层单测。
//
// 覆盖：resourceToTools 翻译（能力/技能/去重）、能力索引、ListResources
// 资源清单（不含工具名与代码）、validateConfig 资源校验、auditObserver
// 审计回调（成功/失败字段落库）。
package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// mockSkillTool 模拟管理端上传的技能工具（工具名 = skill_<技能名>）。
type mockSkillTool struct{ name string }

func (t *mockSkillTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        t.name,
		Description: "模拟技能：" + t.name,
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *mockSkillTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "mock skill ok", nil
}

// newSvcWithSkill 构造带技能工具的 Service（技能 = 注册表里 skill_ 前缀工具）。
func newSvcWithSkill(t *testing.T) *Service {
	t.Helper()
	reg, err := DefaultToolSet()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&mockSkillTool{name: "skill_emoji-helper"}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(Config{
		Repo:         newFakeRepo(),
		Provider:     &llm.MockProvider{},
		Registry:     reg,
		Log:          zap.NewNop(),
		Model:        "test-model",
		SystemPrompt: "测试",
		MaxRounds:    8,
		MaxMessages:  20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestResourceToTools(t *testing.T) {
	cases := []struct {
		name      string
		resources []string
		want      []string
	}{
		{"能力翻译", []string{"search"}, []string{"web_search", "fetch_url", "fetch_url_render"}},
		{"能力去重", []string{"search", "file", "search"}, []string{"web_search", "fetch_url", "fetch_url_render", "file_ops"}},
		{"识图能力翻译", []string{"vision"}, []string{"describe_image"}},
		{"文档解析能力翻译", []string{"doc"}, []string{"read_document"}},
		{"网页文档能力翻译", []string{"webdoc"}, []string{"render_html"}},
		{"Office文档能力翻译", []string{"officedoc"}, []string{"render_document"}},
		{"技能翻译", []string{"emoji-helper"}, []string{"skill_emoji-helper"}},
		{"能力+技能混合", []string{"calculate", "vision", "doc", "emoji-helper"}, []string{"calculator", "describe_image", "read_document", "skill_emoji-helper"}},
		{"空列表", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resourceToTools(c.resources)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("resourceToTools(%v) = %v, want %v", c.resources, got, c.want)
			}
		})
	}
}

func TestCapabilityByID(t *testing.T) {
	if c, ok := capabilityByID("search"); !ok || c.name != "搜索" {
		t.Fatalf("capabilityByID(search) = %+v, %v", c, ok)
	}
	if c, ok := capabilityByID("vision"); !ok || c.name != "识图" {
		t.Fatalf("capabilityByID(vision) = %+v, %v", c, ok)
	}
	if c, ok := capabilityByID("doc"); !ok || c.name != "文档解析" {
		t.Fatalf("capabilityByID(doc) = %+v, %v", c, ok)
	}
	if _, ok := capabilityByID("not-a-capability"); ok {
		t.Fatal("不应找到不存在的能力")
	}
}

// TestSplitResourceTools 能力/技能按类别拆分（独立 presence 语义的前置步骤）。
func TestSplitResourceTools(t *testing.T) {
	cases := []struct {
		name      string
		resources []string
		wantCaps  []string
		wantSk    []string
	}{
		{"纯能力", []string{"search", "file"}, []string{"search", "file"}, nil},
		{"纯技能", []string{"emoji-helper", "data-plot"}, nil, []string{"emoji-helper", "data-plot"}},
		{"混合", []string{"calculate", "emoji-helper", "time"}, []string{"calculate", "time"}, []string{"emoji-helper"}},
		{"空列表", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps, skills := splitResourceTools(c.resources)
			if strings.Join(caps, ",") != strings.Join(c.wantCaps, ",") {
				t.Fatalf("caps = %v, want %v", caps, c.wantCaps)
			}
			if strings.Join(skills, ",") != strings.Join(c.wantSk, ",") {
				t.Fatalf("skills = %v, want %v", skills, c.wantSk)
			}
		})
	}
}

// TestAllCapabilityTools 全部内置能力的工具并集（能力类别"未设置"时的全量白名单）。
func TestAllCapabilityTools(t *testing.T) {
	got := allCapabilityTools()
	for _, c := range defaultCapabilities {
		for _, toolName := range c.tools {
			if !containsStr(got, toolName) {
				t.Fatalf("allCapabilityTools 缺少能力 %s 的工具 %s: %v", c.id, toolName, got)
			}
		}
	}
	// 去重断言：工具并集不应有重复项。
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Fatalf("allCapabilityTools 存在重复工具 %s: %v", n, got)
		}
		seen[n] = true
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestBuildResourceTools_PresenceSemantics 能力/技能独立 presence 标记的装配语义：
//   - 类别标记 true → 白名单 = 该类别的项（空 = 全不选，置 restrictEmpty）；
//   - 类别标记 false → 注入实例全量（能力全量 / 全部已注册 skill_ 工具）；
//   - 两个标记都未设置 → 走调用方 legacy 分支（本函数直接调用 = 视作全量）。
func TestBuildResourceTools_PresenceSemantics(t *testing.T) {
	svc := newSvcWithSkill(t) // 注册表含 skill_emoji-helper
	allCaps := allCapabilityTools()
	allSkills := svc.allDomainSkillTools()
	if !containsStr(allSkills, "skill_emoji-helper") {
		t.Fatalf("allDomainSkillTools 应含 skill_emoji-helper: %v", allSkills)
	}

	t.Run("能力全不选+技能跟随全量", func(t *testing.T) {
		var restrict bool
		tools := svc.buildResourceTools(SessionConfig{EnabledCapabilitiesSet: true}, &restrict)
		if !restrict {
			t.Fatal("能力类别显式全不选应置 restrictEmpty=true")
		}
		want := append([]string(nil), allSkills...)
		if !sameStringSet(tools, want) {
			t.Fatalf("tools = %v, want(全部技能) = %v", tools, want)
		}
	})
	t.Run("技能全不选+能力跟随全量", func(t *testing.T) {
		var restrict bool
		tools := svc.buildResourceTools(SessionConfig{EnabledSkillsSet: true}, &restrict)
		if !restrict {
			t.Fatal("技能类别显式全不选应置 restrictEmpty=true")
		}
		if !sameStringSet(tools, allCaps) {
			t.Fatalf("tools = %v, want(全部能力) = %v", tools, allCaps)
		}
	})
	t.Run("两个类别都全不选", func(t *testing.T) {
		var restrict bool
		tools := svc.buildResourceTools(SessionConfig{EnabledCapabilitiesSet: true, EnabledSkillsSet: true}, &restrict)
		if !restrict {
			t.Fatal("两类别全不选应置 restrictEmpty=true")
		}
		if len(tools) != 0 {
			t.Fatalf("两类别全不选白名单应为空: %v", tools)
		}
	})
	t.Run("能力白名单+技能白名单", func(t *testing.T) {
		var restrict bool
		tools := svc.buildResourceTools(SessionConfig{
			EnabledCapabilitiesSet: true,
			EnabledSkillsSet:       true,
			EnabledResources:       []string{"search", "calculate", "emoji-helper"},
		}, &restrict)
		if restrict {
			t.Fatal("非空白名单不应置 restrictEmpty")
		}
		want := []string{"web_search", "fetch_url", "fetch_url_render", "calculator", "skill_emoji-helper"}
		if !sameStringSet(tools, want) {
			t.Fatalf("tools = %v, want %v", tools, want)
		}
	})
	t.Run("仅能力白名单", func(t *testing.T) {
		var restrict bool
		tools := svc.buildResourceTools(SessionConfig{
			EnabledCapabilitiesSet: true,
			EnabledResources:       []string{"search"},
		}, &restrict)
		if restrict {
			t.Fatal("非空能力白名单不应置 restrictEmpty")
		}
		want := append([]string{"web_search", "fetch_url", "fetch_url_render"}, allSkills...)
		if !sameStringSet(tools, want) {
			t.Fatalf("tools = %v, want(web_search+全部技能) = %v", tools, want)
		}
	})
}

// sameStringSet 无序集合相等（去重比较）。
func sameStringSet(a, b []string) bool {
	ma, mb := map[string]bool{}, map[string]bool{}
	for _, s := range a {
		ma[s] = true
	}
	for _, s := range b {
		mb[s] = true
	}
	if len(ma) != len(mb) {
		return false
	}
	for k := range ma {
		if !mb[k] {
			return false
		}
	}
	return true
}

func TestListResources(t *testing.T) {
	svc := newSvcWithSkill(t)
	resources := svc.ListResources("")
	if !hasResource(resources, "search", "capability") {
		t.Fatalf("缺少能力 search: %+v", resources)
	}
	if !hasResource(resources, "file", "capability") {
		t.Fatalf("缺少能力 file: %+v", resources)
	}
	if !hasResource(resources, "vision", "capability") {
		t.Fatalf("缺少能力 vision: %+v", resources)
	}
	if !hasResource(resources, "doc", "capability") {
		t.Fatalf("缺少能力 doc: %+v", resources)
	}
	if !hasResource(resources, "webdoc", "capability") {
		t.Fatalf("缺少能力 webdoc（网页文档，P5-HTML）: %+v", resources)
	}
	if !hasResource(resources, "officedoc", "capability") {
		t.Fatalf("缺少能力 officedoc（Office 文档，P4-I 可配置流程管线）: %+v", resources)
	}
	// 技能：id 为技能名（不带 skill_ 前缀），type=skill
	if !hasResource(resources, "emoji-helper", "skill") {
		t.Fatalf("缺少技能 emoji-helper: %+v", resources)
	}
	// 不泄露底层工具名：id 不得带 skill_ 前缀，type 必须区分能力/技能
	for _, r := range resources {
		if strings.Contains(r.ID, "skill_") || r.Type == "" {
			t.Fatalf("资源项泄露底层工具名或缺少类型: %+v", r)
		}
	}
}

func hasResource(list []ResourceInfo, id, typ string) bool {
	for _, r := range list {
		if r.ID == id && r.Type == typ {
			return true
		}
	}
	return false
}

// TestDocGenEnabled Office 文档生成能力（officedoc）开关语义（P4-I）：
//   - 会话未显式设置能力 → 默认启用（向后兼容）；
//   - 显式设置且白名单含 officedoc → 启用；
//   - 显式设置且白名单不含 officedoc → 禁用。
func TestDocGenEnabled(t *testing.T) {
	svc := newSvcWithSkill(t)

	if !svc.docGenEnabled(&Session{Config: SessionConfig{}}) {
		t.Error("未显式设置能力时应默认启用文档生成")
	}
	// 显式白名单含 officedoc → 启用
	if !svc.docGenEnabled(&Session{Config: SessionConfig{
		EnabledCapabilitiesSet: true,
		EnabledResources:       []string{"search", "officedoc"},
	}}) {
		t.Error("白名单含 officedoc 时应启用文档生成")
	}
	// 显式白名单仅含 webdoc → 禁用（编排自动产出只跟 officedoc）
	if svc.docGenEnabled(&Session{Config: SessionConfig{
		EnabledCapabilitiesSet: true,
		EnabledResources:       []string{"search", "webdoc"},
	}}) {
		t.Error("白名单不含 officedoc 时应禁用文档生成")
	}
	// 显式白名单不含任何文档能力 → 禁用
	if svc.docGenEnabled(&Session{Config: SessionConfig{
		EnabledCapabilitiesSet: true,
		EnabledResources:       []string{"search", "file"},
	}}) {
		t.Error("白名单不含 officedoc 时应禁用文档生成")
	}
}

func TestValidateConfig_Resources(t *testing.T) {
	svc := newSvcWithSkill(t)

	if err := svc.validateConfig(SessionConfig{EnabledResources: []string{"search"}}); err != nil {
		t.Fatalf("能力 search 应通过校验: %v", err)
	}
	if err := svc.validateConfig(SessionConfig{EnabledResources: []string{"vision"}}); err != nil {
		t.Fatalf("能力 vision 应通过校验: %v", err)
	}
	if err := svc.validateConfig(SessionConfig{EnabledResources: []string{"emoji-helper"}}); err != nil {
		t.Fatalf("已注册技能应通过校验: %v", err)
	}
	if err := svc.validateConfig(SessionConfig{EnabledResources: []string{"no-such-skill"}}); err == nil {
		t.Fatal("未注册技能应报错")
	}
	if err := svc.validateConfig(SessionConfig{}); err != nil {
		t.Fatalf("空配置（全部启用）应通过: %v", err)
	}
	// 兼容旧数据：enabled_tools 校验仍生效
	if err := svc.validateConfig(SessionConfig{EnabledTools: []string{"calculator"}}); err != nil {
		t.Fatalf("enabled_tools 合法工具应通过: %v", err)
	}
	if err := svc.validateConfig(SessionConfig{EnabledTools: []string{"ghost_tool"}}); err == nil {
		t.Fatal("enabled_tools 未注册工具应报错")
	}
}

func TestAuditObserver(t *testing.T) {
	repo := newFakeRepo()
	svc, err := newTestService(repo, &llm.MockProvider{})
	if err != nil {
		t.Fatal(err)
	}
	obs := svc.auditObserver(42, 7)

	// 成功路径：参数原样、结果与耗时正确
	obs(schema.ToolCall{ID: "call_1", Name: "file_ops", Arguments: json.RawMessage(`{"path":"docs/report.md"}`)},
		&schema.ToolResult{ToolCallID: "call_1", Content: "ok"}, nil, 12*time.Millisecond)
	// 失败路径：错误信息进入 result、is_error=true
	obs(schema.ToolCall{ID: "call_2", Name: "file_ops"},
		nil, errors.New("模拟失败"), 5*time.Millisecond)

	if len(repo.audits) != 2 {
		t.Fatalf("审计条数 = %d, want 2", len(repo.audits))
	}
	a0 := repo.audits[0]
	if a0.UserID != 42 || a0.SessionID != 7 || a0.AgentName != defaultAgentName ||
		a0.Tool != "file_ops" || a0.ToolCallID != "call_1" {
		t.Fatalf("审计1 身份字段异常: %+v", a0)
	}
	if a0.IsError || a0.Result != "ok" || a0.DurationMs != 12 {
		t.Fatalf("审计1 结果字段异常: %+v", a0)
	}
	if string(a0.Arguments) != `{"path":"docs/report.md"}` {
		t.Fatalf("审计1 参数未原样保留: %s", a0.Arguments)
	}
	a1 := repo.audits[1]
	if !a1.IsError || a1.Result != "模拟失败" || a1.DurationMs != 5 {
		t.Fatalf("审计2 结果字段异常: %+v", a1)
	}
}

// 编译期断言：mockSkillTool 实现 tool.Tool。
var _ tool.Tool = (*mockSkillTool)(nil)
