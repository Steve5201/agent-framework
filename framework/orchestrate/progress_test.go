package orchestrate

import (
	"context"
	"sync"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
)

// TestExecutor_ProgressEvents 验证任务开始/结束进度事件按序触发。
func TestExecutor_ProgressEvents(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{spec("a"), spec("b", "a")})
	var mu sync.Mutex
	var events []ProgressEvent
	ex := NewExecutor(
		func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
			return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
		},
		WithProgress(func(ev ProgressEvent) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}),
	)
	if _, err := ex.Run(context.Background(), g); err != nil {
		t.Fatalf("Run err: %v", err)
	}
	// 期望顺序：a开始 → a结束 → b开始 → b结束（共4个任务级事件）
	wantTypes := []ProgressType{
		ProgressTaskStarted, ProgressTaskFinished,
		ProgressTaskStarted, ProgressTaskFinished,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数 = %d, want %d", len(events), len(wantTypes))
	}
	for i, wt := range wantTypes {
		if events[i].Type != wt {
			t.Fatalf("事件[%d].Type = %s, want %s", i, events[i].Type, wt)
		}
	}
	if events[0].TaskID != "a" || events[2].TaskID != "b" {
		t.Fatalf("事件 taskID 顺序错误: %s, %s", events[0].TaskID, events[2].TaskID)
	}
	if events[1].Result == nil || events[1].Result.Status != TaskCompleted {
		t.Fatal("task_finished 事件应携带 Result")
	}
}

// TestOrchestrator_RunEvents 验证 run 级事件（completed）。
func TestOrchestrator_RunEvents(t *testing.T) {
	planner, _ := NewFixedPlanner([]TaskSpec{spec("a")})
	runner := func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
		return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
	}
	executor := NewExecutor(runner)
	agg := NewAggregator(&llm.MockProvider{Name_: "m",
		ChatFn: func(*llm.Request) (*llm.Response, error) { return &llm.Response{Content: "final"}, nil }}, "")

	var mu sync.Mutex
	var types []ProgressType
	o := NewOrchestrator(planner, executor, agg)
	o.SetProgress(func(ev ProgressEvent) {
		mu.Lock()
		types = append(types, ev.Type)
		mu.Unlock()
	})
	if _, err := o.Run(context.Background(), "目标"); err != nil {
		t.Fatalf("Run err: %v", err)
	}
	// 最后一个事件应是 run_completed
	if len(types) == 0 || types[len(types)-1] != ProgressRunCompleted {
		t.Fatalf("最后事件 = %v, want run_completed", types)
	}
}

// TestOrchestrator_RunFailedEvent 验证 run 失败事件。
func TestOrchestrator_RunFailedEvent(t *testing.T) {
	// planner 失败（非法模板）→ Run 应发 run_failed
	planner, _ := NewFixedPlanner([]TaskSpec{{ID: "a", Role: "worker", Goal: "x"}})
	executor := NewExecutor(func(_ context.Context, t TaskSpec, _ string) (TaskResult, error) {
		return TaskResult{}, nil
	})
	agg := NewAggregator(&llm.MockProvider{Name_: "m"}, "")

	var mu sync.Mutex
	var failed *ProgressEvent
	o := NewOrchestrator(planner, executor, agg)
	o.SetProgress(func(ev ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		if ev.Type == ProgressRunFailed {
			cp := ev
			failed = &cp
		}
	})
	// 让 executor 失败：runner 返回 error + FailAbort
	executor.runner = func(_ context.Context, t TaskSpec, _ string) (TaskResult, error) {
		return TaskResult{}, context.DeadlineExceeded
	}
	if _, err := o.Run(context.Background(), "目标"); err == nil {
		t.Fatal("runner 失败应返回错误")
	}
	mu.Lock()
	defer mu.Unlock()
	if failed == nil {
		t.Fatal("应发 run_failed 事件")
	}
}
