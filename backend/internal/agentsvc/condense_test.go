package agentsvc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/memory"
	"github.com/Steve5201/agent-framework/schema"
)

// TestSerializeForSummary 验证摘要输入序列化：角色标签、忽略思考内容与空正文、
// 旧摘要参与再压缩、条数上限截断。
func TestSerializeForSummary(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "我想学 Go"},
		{Role: schema.RoleAssistant, Content: "建议从官方文档开始", Reasoning: "思考过程应被忽略"},
		{Role: schema.RoleTool, Content: "ok", ToolCallID: "c1"},
		{Role: schema.RoleAssistant, Content: ""}, // 空正文跳过
		{Role: schema.RoleSystem, Content: "旧摘要"},
	}
	out := serializeForSummary(msgs, 10)
	for _, want := range []string{
		"[用户] 我想学 Go",
		"[助手] 建议从官方文档开始",
		"[工具结果] ok",
		"[摘要] 旧摘要",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("摘要输入应含 %q，实际：\n%s", want, out)
		}
	}
	if strings.Contains(out, "思考过程应被忽略") {
		t.Errorf("思考内容不应进入摘要输入：\n%s", out)
	}

	// 条数上限：超过后出现省略提示
	truncated := serializeForSummary(msgs, 2)
	if !strings.Contains(truncated, "其余历史从略") {
		t.Errorf("超过 maxCount 应有省略提示：\n%s", truncated)
	}
}

// TestSerializeForSummary_TruncatesLongContent 超长正文按 rune 截断（中文不乱码）。
func TestSerializeForSummary_TruncatesLongContent(t *testing.T) {
	long := strings.Repeat("汉字", 300) // 600 rune > 200 cap
	out := serializeForSummary([]schema.Message{{Role: schema.RoleUser, Content: long}}, 10)
	if strings.Contains(out, strings.Repeat("汉字", 250)) {
		t.Error("超长正文应被截断")
	}
	if !strings.Contains(out, "汉字") {
		t.Error("截断后应保留正文开头（rune 安全）")
	}
}

// TestMakeCondenser 验证压缩器调用 LLM：正确模型、system 压缩指令 + 序列化
// 输入、返回摘要正文。
func TestMakeCondenser(t *testing.T) {
	var gotModel string
	var gotMsgs []schema.Message
	svc := &Service{
		provider: &llm.MockProvider{
			Content: "用户想学Go，建议从官方文档开始",
			ChatFn: func(req *llm.Request) (*llm.Response, error) {
				gotModel = req.Model
				gotMsgs = req.Messages
				return &llm.Response{Content: "用户想学Go，建议从官方文档开始"}, nil
			},
		},
		model: "deepseek-v4-flash",
	}
	cond := svc.makeCondenser("deepseek-v4-flash")
	summary, err := cond(context.Background(), []schema.Message{
		{Role: schema.RoleUser, Content: "我想学 Go"},
		{Role: schema.RoleAssistant, Content: "建议从官方文档开始"},
	})
	if err != nil {
		t.Fatalf("Condense error = %v", err)
	}
	if summary != "用户想学Go，建议从官方文档开始" {
		t.Errorf("summary = %q", summary)
	}
	if gotModel != "deepseek-v4-flash" {
		t.Errorf("model = %q, want deepseek-v4-flash", gotModel)
	}
	if len(gotMsgs) != 2 {
		t.Fatalf("请求应含 system+user 两条消息，实际 %d", len(gotMsgs))
	}
	if gotMsgs[0].Role != schema.RoleSystem {
		t.Errorf("首条应为压缩指令 system，实际 %+v", gotMsgs[0])
	}
	if !strings.Contains(gotMsgs[1].Content, "[用户] 我想学 Go") {
		t.Errorf("user 消息应为序列化对话：%q", gotMsgs[1].Content)
	}
}

// TestMakeCondenser_EmptyInput 无可压缩内容时不调用 LLM，直接返回空串。
func TestMakeCondenser_EmptyInput(t *testing.T) {
	provider := &llm.MockProvider{Content: "x"}
	provider.ChatFn = func(req *llm.Request) (*llm.Response, error) {
		t.Fatal("无可压缩内容时不应调用 LLM")
		return nil, nil
	}
	svc := &Service{provider: provider, model: "m"}
	cond := svc.makeCondenser("m")
	out, err := cond(context.Background(), []schema.Message{
		{Role: schema.RoleAssistant, Content: ""},
		{Role: schema.RoleAssistant, Content: "  "},
	})
	if err != nil {
		t.Fatalf("空输入不应报错：%v", err)
	}
	if out != "" {
		t.Errorf("空输入应返回空串，实际 %q", out)
	}
}

// TestMakeCondenser_ProviderError 上游失败返回错误（framework 据此退化为普通裁剪）。
func TestMakeCondenser_ProviderError(t *testing.T) {
	svc := &Service{
		provider: &llm.MockProvider{Err: errors.New("上游故障")},
		model:    "m",
	}
	cond := svc.makeCondenser("m")
	if _, err := cond(context.Background(), []schema.Message{
		{Role: schema.RoleUser, Content: "内容"},
	}); err == nil {
		t.Error("上游失败应返回错误")
	}
}

// TestBuildCondensationMessage 压缩记录消息：system 角色 + __condense_v1__ 前缀
// + JSON {dropped,count}，roundNo 透传（前端据此定位"哪个节点压缩过"）。
// system 角色由 loadHistory 统一过滤，压缩记录永不进模型上下文。
func TestBuildCondensationMessage(t *testing.T) {
	m := buildCondensationMessage(7, memory.CondenseInfo{Dropped: 5, Count: 2})
	if m.Role != string(schema.RoleSystem) {
		t.Errorf("Role = %q, want system", m.Role)
	}
	if m.RoundNo != 7 {
		t.Errorf("RoundNo = %d, want 7", m.RoundNo)
	}
	if !strings.HasPrefix(m.Content, condensationMarker) {
		t.Errorf("内容应以 %q 前缀：%q", condensationMarker, m.Content)
	}
	if !strings.Contains(m.Content, `"dropped":5`) || !strings.Contains(m.Content, `"count":2`) {
		t.Errorf("内容应含 JSON 记录 dropped/count：%q", m.Content)
	}
}
