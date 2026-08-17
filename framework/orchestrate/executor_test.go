package orchestrate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Steve5201/agent-framework/llm"
)

// syncRunner 串行执行的测试 Runner：记录调用顺序。
func syncRunner(t *testing.T, log *[]string, mu *sync.Mutex) Runner {
	return func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
		mu.Lock()
		*log = append(*log, task.ID)
		mu.Unlock()
		return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok:" + task.ID}, nil
	}
}

// concurrencyCounter 并发计数辅助：active 当前并发、peak 历史峰值。
type concurrencyCounter struct {
	mu     sync.Mutex
	active int
	peak   int
}

func (c *concurrencyCounter) enter() {
	c.mu.Lock()
	c.active++
	if c.active > c.peak {
		c.peak = c.active
	}
	c.mu.Unlock()
}

func (c *concurrencyCounter) leave() {
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
}

// parallelRunner 并行执行（记录并发峰值）的测试 Runner。
func parallelRunner(c *concurrencyCounter) Runner {
	return func(ctx context.Context, task TaskSpec, _ string) (TaskResult, error) {
		c.enter()
		time.Sleep(20 * time.Millisecond)
		c.leave()
		return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok:" + task.ID}, nil
	}
}

func TestExecutor_SequentialDeps(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{
		spec("a"),
		spec("b", "a"),
		spec("c", "b"),
	})
	var log []string
	var mu sync.Mutex
	ex := NewExecutor(syncRunner(t, &log, &mu))
	results, err := ex.Run(context.Background(), g)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	// 定义顺序返回
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	// 执行顺序必须 a→b→c
	if !reflectSliceEqual(log, []string{"a", "b", "c"}) {
		t.Fatalf("执行顺序 = %v, want [a b c]", log)
	}
	for _, r := range results {
		if r.Status != TaskCompleted {
			t.Fatalf("任务 %s 状态 = %s, want completed", r.TaskID, r.Status)
		}
		if r.StartedAt.IsZero() {
			t.Fatalf("任务 %s 未记录启动时间", r.TaskID)
		}
	}
}

func TestExecutor_ParallelLevel(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{
		spec("a"),
		spec("b"),
		spec("c"),
		spec("d"),
	})
	c := &concurrencyCounter{}
	ex := NewExecutor(parallelRunner(c), WithMaxParallel(4))
	results, err := ex.Run(context.Background(), g)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4", len(results))
	}
	if c.peak < 2 {
		t.Fatalf("并发峰值 = %d, 期望 > 1（入度0任务应并行）", c.peak)
	}
}

func TestExecutor_MaxParallelCaps(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{
		spec("a"), spec("b"), spec("c"), spec("d"), spec("e"),
	})
	c := &concurrencyCounter{}
	ex := NewExecutor(parallelRunner(c), WithMaxParallel(2))
	if _, err := ex.Run(context.Background(), g); err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if c.peak > 2 {
		t.Fatalf("并发峰值 = %d, 超过上限 2", c.peak)
	}
}

func TestExecutor_FailAbort(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{
		spec("a"),
		spec("b"),
		spec("c", "a", "b"),
	})
	boom := errors.New("boom")
	runner := func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
		if task.ID == "b" {
			return TaskResult{TaskID: task.ID, Status: TaskFailed, Content: "failed"}, boom
		}
		return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
	}
	ex := NewExecutor(runner, WithFailPolicy(FailAbort))
	results, err := ex.Run(context.Background(), g)
	if err == nil {
		t.Fatal("FailAbort 策略下失败应返回错误")
	}
	foundFailed := false
	for _, r := range results {
		if r.Status == TaskFailed {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Fatal("结果中应包含 failed 任务")
	}
}

func TestExecutor_FailSkipDependents(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{
		spec("a"),
		spec("b"),
		spec("c", "a"), // b 失败不应影响 c
		spec("d", "b"), // b 失败 → d 被跳过
	})
	runner := func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
		if task.ID == "b" {
			return TaskResult{TaskID: task.ID, Status: TaskFailed, Content: "failed"}, errors.New("boom")
		}
		return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
	}
	ex := NewExecutor(runner, WithFailPolicy(FailSkipDependents))
	results, err := ex.Run(context.Background(), g)
	if err != nil {
		t.Fatalf("FailSkipDependents 策略下不应中止: %v", err)
	}
	statusByID := map[string]TaskStatus{}
	for _, r := range results {
		statusByID[r.TaskID] = r.Status
	}
	if statusByID["a"] != TaskCompleted {
		t.Fatalf("a 应 completed, got %s", statusByID["a"])
	}
	if statusByID["c"] != TaskCompleted {
		t.Fatalf("c 应 completed（不受 b 失败影响）, got %s", statusByID["c"])
	}
	if statusByID["d"] != TaskSkipped {
		t.Fatalf("d 应 skipped（依赖 b 失败）, got %s", statusByID["d"])
	}
}

func TestExecutor_FailContinue(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{
		spec("a"),
		spec("b", "a"),
	})
	runner := func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
		if task.ID == "a" {
			return TaskResult{TaskID: task.ID, Status: TaskFailed, Content: "failed"}, errors.New("boom")
		}
		return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
	}
	ex := NewExecutor(runner, WithFailPolicy(FailContinue))
	results, err := ex.Run(context.Background(), g)
	if err != nil {
		t.Fatalf("FailContinue 不应中止: %v", err)
	}
	statusByID := map[string]TaskStatus{}
	for _, r := range results {
		statusByID[r.TaskID] = r.Status
	}
	if statusByID["b"] != TaskCompleted {
		t.Fatalf("b 应 completed（FailContinue）, got %s", statusByID["b"])
	}
}

func TestExecutor_ResultCallback(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{spec("a"), spec("b", "a")})
	var mu sync.Mutex
	called := 0
	ex := NewExecutor(
		func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
			return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
		},
		WithResultCallback(func(TaskResult) {
			mu.Lock()
			called++
			mu.Unlock()
		}),
	)
	if _, err := ex.Run(context.Background(), g); err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if called != 2 {
		t.Fatalf("进度回调调用 %d 次, want 2", called)
	}
}

func TestExecutor_Cancel(t *testing.T) {
	g, _ := NewTaskGraph([]TaskSpec{
		spec("a"), spec("b"), spec("c"), spec("d"),
	})
	// Runner 永远阻塞，用 ctx 取消终止
	ex := NewExecutor(func(ctx context.Context, task TaskSpec, _ string) (TaskResult, error) {
		<-ctx.Done()
		return TaskResult{}, ctx.Err()
	}, WithMaxParallel(2))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := ex.Run(ctx, g); err == nil {
		t.Fatal("ctx 取消后应返回错误")
	}
}

// reflectSliceEqual 简单字符串切片比较（避免为测试引入 reflect.DeepEqual 噪音）。
func reflectSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestExecutor_Subgraph_CompositeTask 复合任务（P4-J 阶段3）：
// 先递归执行子图 → 子图成果注入上游 → 任务自身整合产出，token 用量累计。
func TestExecutor_Subgraph_CompositeTask(t *testing.T) {
	g, err := NewTaskGraph([]TaskSpec{{
		ID: "report", Role: "worker", Goal: "整合报告",
		Subgraph: []TaskSpec{spec("research"), spec("draft", "research")},
	}})
	if err != nil {
		t.Fatalf("NewTaskGraph: %v", err)
	}

	var mu sync.Mutex
	var order []string
	upstreams := map[string]string{}
	runner := func(_ context.Context, task TaskSpec, upstream string) (TaskResult, error) {
		mu.Lock()
		order = append(order, task.ID)
		upstreams[task.ID] = upstream
		mu.Unlock()
		return TaskResult{
			TaskID: task.ID, Status: TaskCompleted, Content: "out:" + task.ID,
			Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil
	}
	ex := NewExecutor(runner)
	results, err := ex.Run(context.Background(), g)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 外层结果只含复合任务自身（子图结果不进入外层）。
	if len(results) != 1 || results[0].TaskID != "report" {
		t.Fatalf("外层结果应只有复合任务, got %+v", results)
	}
	// 执行顺序：research → draft → report。
	mu.Lock()
	orderCopy := append([]string(nil), order...)
	mu.Unlock()
	if !reflectSliceEqual(orderCopy, []string{"research", "draft", "report"}) {
		t.Fatalf("执行顺序 = %v, want [research draft report]", orderCopy)
	}
	// report 上游应包含子图成果。
	up := upstreams["report"]
	if !strings.Contains(up, "out:research") || !strings.Contains(up, "out:draft") {
		t.Fatalf("report 上游应包含子图成果, got %q", up)
	}
	// token 累计：report 自身 15 + 子图 (research 15 + draft 15) = 45。
	if results[0].Usage.TotalTokens != 45 {
		t.Fatalf("复合任务 token 应累计子图, got %d", results[0].Usage.TotalTokens)
	}
}

// TestExecutor_Subgraph_ProgressPrefix 子图进度事件 TaskID 带层级前缀
// （"report." 前缀，区分外层与子图任务）。
func TestExecutor_Subgraph_ProgressPrefix(t *testing.T) {
	g, err := NewTaskGraph([]TaskSpec{{
		ID: "report", Role: "worker", Goal: "g",
		Subgraph: []TaskSpec{spec("research")},
	}})
	if err != nil {
		t.Fatalf("NewTaskGraph: %v", err)
	}
	var events []ProgressEvent
	ex := NewExecutor(
		func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
			return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
		},
		WithProgress(func(ev ProgressEvent) { events = append(events, ev) }),
	)
	if _, err := ex.Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var ids []string
	for _, ev := range events {
		if ev.TaskID != "" {
			ids = append(ids, ev.TaskID)
		}
	}
	// 应同时出现外层 "report" 与子图 "report.research"。
	foundOuter, foundSub := false, false
	for _, id := range ids {
		if id == "report" {
			foundOuter = true
		}
		if id == "report.research" {
			foundSub = true
		}
	}
	if !foundOuter || !foundSub {
		t.Fatalf("进度事件应含 report 与 report.research, got %v", ids)
	}
}

// TestExecutor_Subgraph_DepthLimitRuntime 运行期深度限制：图构建合法但执行器
// WithMaxDepth 更严格时，超深子图直接失败（防御绕过 NewTaskGraph 的路径）。
func TestExecutor_Subgraph_DepthLimitRuntime(t *testing.T) {
	g, err := NewTaskGraph([]TaskSpec{{
		ID: "a", Role: "worker", Goal: "复合",
		Subgraph: []TaskSpec{{
			ID: "a1", Role: "worker", Goal: "子",
			Subgraph: []TaskSpec{spec("a1a")},
		}},
	}})
	if err != nil {
		t.Fatalf("NewTaskGraph（全局深度 3）应合法: %v", err)
	}
	ex := NewExecutor(
		func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
			return TaskResult{TaskID: task.ID, Status: TaskCompleted}, nil
		},
		WithMaxDepth(1), // 运行期收紧为 1 层
	)
	results, err := ex.Run(context.Background(), g)
	if err == nil {
		t.Fatal("运行期深度超限应报错")
	}
	if !strings.Contains(err.Error(), "子图深度超过上限") {
		t.Fatalf("错误信息不达意: %v", err)
	}
	// 外层结果中 a 应 failed。
	if len(results) != 1 || results[0].Status != TaskFailed {
		t.Fatalf("a 应 failed, got %+v", results)
	}
}

// TestExecutor_Subgraph_ResultCallback 子图任务也触发结果回调（结果层仍无前缀）。
func TestExecutor_Subgraph_ResultCallback(t *testing.T) {
	g, err := NewTaskGraph([]TaskSpec{{
		ID: "report", Role: "worker", Goal: "g",
		Subgraph: []TaskSpec{spec("research")},
	}})
	if err != nil {
		t.Fatalf("NewTaskGraph: %v", err)
	}
	var mu sync.Mutex
	var got []string
	ex := NewExecutor(
		func(_ context.Context, task TaskSpec, _ string) (TaskResult, error) {
			return TaskResult{TaskID: task.ID, Status: TaskCompleted, Content: "ok"}, nil
		},
		WithResultCallback(func(r TaskResult) {
			mu.Lock()
			got = append(got, r.TaskID)
			mu.Unlock()
		}),
	)
	if _, err := ex.Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflectSliceEqual(got, []string{"research", "report"}) {
		t.Fatalf("结果回调 = %v, want [research report]", got)
	}
}
