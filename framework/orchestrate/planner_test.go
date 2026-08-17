package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
)

func TestFixedPlanner(t *testing.T) {
	p, err := NewFixedPlanner([]TaskSpec{spec("a"), spec("b", "a")})
	if err != nil {
		t.Fatalf("NewFixedPlanner err: %v", err)
	}
	g, err := p.Plan(context.Background(), "任意目标")
	if err != nil {
		t.Fatalf("Plan err: %v", err)
	}
	if g.Len() != 2 {
		t.Fatalf("图任务数 = %d, want 2", g.Len())
	}
}

func TestFixedPlanner_GoalPlaceholder(t *testing.T) {
	// 模板 Goal 里的 {goal} 应在 Plan 时替换为用户目标（尤其无依赖入口任务）。
	p, err := NewFixedPlanner([]TaskSpec{
		{ID: "research", Role: "research", Goal: "围绕「{goal}」收集资料"},
		{ID: "outline", Role: "outline", Goal: "围绕「{goal}」设计大纲", Deps: []string{"research"}},
	})
	if err != nil {
		t.Fatalf("NewFixedPlanner err: %v", err)
	}
	g, err := p.Plan(context.Background(), "FFT 原理")
	if err != nil {
		t.Fatalf("Plan err: %v", err)
	}
	r, _ := g.Task("research")
	if r.Goal != "围绕「FFT 原理」收集资料" {
		t.Fatalf("research.Goal = %q, 未替换占位符", r.Goal)
	}
	o, _ := g.Task("outline")
	if o.Goal != "围绕「FFT 原理」设计大纲" {
		t.Fatalf("outline.Goal = %q, 未替换占位符", o.Goal)
	}
}

func TestFixedPlanner_InputPlaceholder(t *testing.T) {
	p, _ := NewFixedPlanner([]TaskSpec{
		{ID: "a", Role: "worker", Goal: "干活", Input: "用户要：{goal}"},
	})
	g, err := p.Plan(context.Background(), "写报告")
	if err != nil {
		t.Fatalf("Plan err: %v", err)
	}
	a, _ := g.Task("a")
	if a.Input != "用户要：写报告" {
		t.Fatalf("a.Input = %q, 未替换占位符", a.Input)
	}
}

func TestLLMPlanner_ParseValid(t *testing.T) {
	planJSON := `{"tasks":[
		{"id":"research","role":"research","goal":"收集资料","deps":[],"output_schema":{"type":"object","required":["summary"]}},
		{"id":"content","role":"content","goal":"写正文","deps":["research"]}
	]}`
	p := &llm.MockProvider{
		Name_:  "mock",
		ChatFn: func(*llm.Request) (*llm.Response, error) { return &llm.Response{Content: planJSON}, nil },
	}
	pl := NewLLMPlanner(p, "")
	g, err := pl.Plan(context.Background(), "做个方案")
	if err != nil {
		t.Fatalf("Plan err: %v", err)
	}
	if g.Len() != 2 {
		t.Fatalf("任务数 = %d, want 2", g.Len())
	}
	if _, err := g.Task("content"); err != nil {
		t.Fatalf("content 任务缺失: %v", err)
	}
}

func TestLLMPlanner_InvalidJSON(t *testing.T) {
	p := &llm.MockProvider{
		Name_:  "mock",
		ChatFn: func(*llm.Request) (*llm.Response, error) { return &llm.Response{Content: "我无法回答"}, nil },
	}
	pl := NewLLMPlanner(p, "")
	if _, err := pl.Plan(context.Background(), "做个方案"); err == nil {
		t.Fatal("非法输出应报错")
	}
}

// TestParsePlanJSON_EmptyPlan 验证显式空计划（{"tasks":[]}）→ ErrNoPlan：
// 上层应回退直接应答，而不是当作"未产出任务"的错误中断。
func TestParsePlanJSON_EmptyPlan(t *testing.T) {
	if _, err := parsePlanJSON(`{"tasks":[]}`); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("空计划应返回 ErrNoPlan，实际 %v", err)
	}
	if _, err := parsePlanJSON(`{"tasks": []}`); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("空计划（带空格）应返回 ErrNoPlan，实际 %v", err)
	}
}

// TestParsePlanJSON_ProseFallback 验证模型未按 JSON 输出（简单问题直接回
// 自然语言）→ ErrNoPlan：宁可回退直接应答，也不让"简单问题"中断报错。
func TestParsePlanJSON_ProseFallback(t *testing.T) {
	for _, s := range []string{
		"这个简单，不需要编排",
		"可以，我能看到文档生成工具。",
		"```json\n{\"tasks\":[]}\n```",
	} {
		if _, err := parsePlanJSON(s); !errors.Is(err, ErrNoPlan) {
			t.Errorf("%q 应返回 ErrNoPlan，实际 %v", s, err)
		}
	}
}

func TestLLMPlanner_TooManyTasks(t *testing.T) {
	var tasks []TaskSpec
	for i := 0; i < 20; i++ {
		tasks = append(tasks, spec(string(rune('a'+i))))
	}
	raw, _ := json.Marshal(map[string]any{"tasks": tasks})
	p := &llm.MockProvider{
		Name_:  "mock",
		ChatFn: func(*llm.Request) (*llm.Response, error) { return &llm.Response{Content: string(raw)}, nil },
	}
	pl := NewLLMPlanner(p, "")
	if _, err := pl.Plan(context.Background(), "目标"); err == nil {
		t.Fatal("超过任务数上限应报错")
	}
}

func TestLLMPlanner_CyclicDeps(t *testing.T) {
	planJSON := `{"tasks":[
		{"id":"a","role":"worker","goal":"A","deps":["b"]},
		{"id":"b","role":"worker","goal":"B","deps":["a"]}
	]}`
	p := &llm.MockProvider{
		Name_:  "mock",
		ChatFn: func(*llm.Request) (*llm.Response, error) { return &llm.Response{Content: planJSON}, nil },
	}
	pl := NewLLMPlanner(p, "")
	if _, err := pl.Plan(context.Background(), "目标"); err == nil {
		t.Fatal("模型输出环依赖应报错")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{`解释如下：{"a":1} 这就是结果`, `{"a":1}`},
	}
	for _, c := range cases {
		if got := extractJSON(c.in); got != c.want {
			t.Fatalf("extractJSON(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
