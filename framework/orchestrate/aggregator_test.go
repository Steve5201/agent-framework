package orchestrate

import (
	"context"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
)

func TestAggregate_OK(t *testing.T) {
	p := &llm.MockProvider{
		Name_: "mock",
		ChatFn: func(*llm.Request) (*llm.Response, error) {
			return &llm.Response{Content: "整合后的最终回答"}, nil
		},
	}
	a := NewAggregator(p, "")
	out, err := a.Aggregate(context.Background(), "做个方案", []TaskResult{
		{TaskID: "research", Status: TaskCompleted, Content: "资料摘要"},
		{TaskID: "content", Status: TaskCompleted, Content: "正文内容"},
	})
	if err != nil {
		t.Fatalf("Aggregate err: %v", err)
	}
	if out != "整合后的最终回答" {
		t.Fatalf("out = %q, want 整合后的最终回答", out)
	}
}

func TestAggregate_NoOutput(t *testing.T) {
	// P4-L 部分失败透明合并：无成功成果不再报错，聚合器"尽力而为"输出。
	p := &llm.MockProvider{
		Name_: "mock",
		ChatFn: func(*llm.Request) (*llm.Response, error) {
			return &llm.Response{Content: "尽力而为的回答"}, nil
		},
	}
	a := NewAggregator(p, "")
	out, err := a.Aggregate(context.Background(), "目标", []TaskResult{
		{TaskID: "a", Status: TaskFailed, Error: "llm: HTTP 504, 上游模型服务响应超时"},
		{TaskID: "b", Status: TaskSkipped},
	})
	if err != nil {
		t.Fatalf("Aggregate err: %v", err)
	}
	if out != "尽力而为的回答" {
		t.Fatalf("out = %q", out)
	}
}

func TestOrchestrator_EndToEnd(t *testing.T) {
	// 固定模板 + 同步 Runner + mock 合并器 → 完整跑一遍
	planner, _ := NewFixedPlanner([]TaskSpec{spec("a"), spec("b", "a")})
	runner := func(_ context.Context, task TaskSpec, upstream string) (TaskResult, error) {
		content := "产出:" + task.ID
		if task.ID == "b" && upstream == "" {
			t.Fatalf("任务 b 应收到上游 a 的成果摘要, got 空")
		}
		return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: content,
			Usage: llm.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}}, nil
	}
	executor := NewExecutor(runner)

	mockAgg := &llm.MockProvider{
		Name_: "mock",
		ChatFn: func(*llm.Request) (*llm.Response, error) {
			return &llm.Response{Content: "最终合并结果"}, nil
		},
	}
	agg := NewAggregator(mockAgg, "")

	o := NewOrchestrator(planner, executor, agg)
	res, err := o.Run(context.Background(), "做个方案")
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if res.Final != "最终合并结果" {
		t.Fatalf("Final = %q", res.Final)
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("Tasks = %d, want 2", len(res.Tasks))
	}
	if res.TotalUsage.TotalTokens != 6 {
		t.Fatalf("TotalUsage.TotalTokens = %d, want 6", res.TotalUsage.TotalTokens)
	}
}
