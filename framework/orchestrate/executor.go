package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Steve5201/agent-framework/llm"
)

// Runner 执行单个子任务。宿主实现：通常用角色池构建 agent.Session，
// 运行 task.Goal（可拼接上游成果摘要），返回结构化结果。
//
// 约定：
//   - 返回 error = 任务失败（Executor 按 FailPolicy 处理）；
//   - TaskResult.Status 可留空（按成功处理）或显式设置；
//   - 声明 OutputSchema 的任务，宿主应用 ValidateStructuredOutput 校验。
type Runner func(ctx context.Context, task TaskSpec, upstream string) (TaskResult, error)

// FailPolicy 子任务失败时的降级策略。
type FailPolicy int

const (
	// FailAbort 任一任务失败 → 整个编排失败（默认，最保守）。
	FailAbort FailPolicy = iota
	// FailSkipDependents 任务失败后其（直接/间接）下游标记 skipped，
	// 其余无依赖失败的任务继续执行。
	FailSkipDependents
	// FailContinue 任务失败不影响下游（下游按自己的 Input 继续）。
	FailContinue
)

// Executor DAG 执行器：入度 0 并行调度、完成解锁下游、失败降级。
// 一轮批处理一个"就绪层"，批内并行、批间串行，天然满足依赖。
//
// P4-J 阶段3·递归：含 Subgraph 的"复合任务"先递归执行子图（深度受
// maxDepth 约束），把子图成果摘要注入上游，再由任务自身 Runner 整合产出。
type Executor struct {
	runner      Runner
	maxParallel int // 并行上限（默认 1 = 顺序）
	maxDepth    int // 子图最大嵌套深度（默认 MaxSubgraphDepth）
	policy      FailPolicy
	onResult    func(TaskResult) // 任务结束回调（nil = 不回调）
	onProgress  ProgressFunc     // 进度回调（nil = 不回调）
}

// ExecutorOption 定制执行器。
type ExecutorOption func(*Executor)

// WithMaxParallel 设置并行执行上限（默认 1 = 顺序执行）。
// 实际并发还受 Runner 内部资源（LLM 限流、沙盒 worker）约束。
func WithMaxParallel(n int) ExecutorOption {
	return func(e *Executor) {
		if n > 0 {
			e.maxParallel = n
		}
	}
}

// WithMaxDepth 设置子图最大嵌套深度（默认 MaxSubgraphDepth=3，运行期防御；
// 图构建期 validateTree 已有同样限制）。0/负数 = 使用默认值。
func WithMaxDepth(d int) ExecutorOption {
	return func(e *Executor) {
		if d > 0 {
			e.maxDepth = d
		}
	}
}

// WithFailPolicy 设置失败降级策略。
func WithFailPolicy(p FailPolicy) ExecutorOption {
	return func(e *Executor) { e.policy = p }
}

// WithResultCallback 设置每次任务结束后的回调（结果实时可见）。
func WithResultCallback(f func(TaskResult)) ExecutorOption {
	return func(e *Executor) { e.onResult = f }
}

// WithProgress 设置编排进度回调（任务开始/结束都会触发，见 ProgressEvent）。
func WithProgress(f ProgressFunc) ExecutorOption {
	return func(e *Executor) { e.onProgress = f }
}

// NewExecutor 创建执行器。
func NewExecutor(runner Runner, opts ...ExecutorOption) *Executor {
	e := &Executor{runner: runner, maxParallel: 1, maxDepth: MaxSubgraphDepth, policy: FailAbort}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Run 执行整张 DAG，返回全部任务结果（按 DAG 定义顺序）。
// 调度算法：循环取"就绪层"（依赖全完成且未完成）→ 批内并行执行 →
// 按策略处理失败 → 直至全部终结（completed / failed / skipped）。
// 复合任务（含 Subgraph）先递归执行子图，再执行自身。
func (e *Executor) Run(ctx context.Context, graph *TaskGraph) ([]TaskResult, error) {
	return e.runGraph(ctx, graph, 0, "")
}

// runGraph 递归执行一层 DAG。depth 为当前层深度（root=0）；prefix 用于进度
// 事件任务 ID 分级（如 "research."、"research.decompose."），结果层 TaskID
// 始终不带前缀，保证宿主入库/合并语义不变。
func (e *Executor) runGraph(ctx context.Context, graph *TaskGraph, depth int, prefix string) ([]TaskResult, error) {
	n := graph.Len()
	done := make(map[string]TaskResult, n)
	doneSet := make(map[string]bool, n)
	var doneMu sync.Mutex

	// 失败传播集合（FailSkipDependents 用）：当前已 failed 的任务 ID。
	failed := make(map[string]bool, n)
	var failedMu sync.Mutex

	var firstErr error
	var errMu sync.Mutex

	for len(done) < n {
		// 1. 当前就绪层
		readyIDs := graph.ReadyTasks(doneSet)

		// 2. FailSkipDependents：依赖链上有失败 → 标记 skipped
		var skipped []TaskSpec
		if e.policy == FailSkipDependents {
			failedMu.Lock()
			fcopy := make(map[string]bool, len(failed))
			for id := range failed {
				fcopy[id] = true
			}
			failedMu.Unlock()
			for _, id := range readyIDs {
				t, _ := graph.Task(id)
				if dependsOnFailed(graph, t, fcopy, map[string]bool{}) {
					skipped = append(skipped, t)
				}
			}
		}
		for _, t := range skipped {
			r := TaskResult{TaskID: t.ID, Role: t.Role, Status: TaskSkipped, Content: "上游任务失败，本任务被跳过"}
			doneMu.Lock()
			done[t.ID] = r
			doneSet[t.ID] = true
			doneMu.Unlock()
			if e.onResult != nil {
				e.onResult(r)
			}
			if e.onProgress != nil {
				e.onProgress(ProgressEvent{Type: ProgressTaskFinished, TaskID: prefix + t.ID, Status: TaskSkipped, Result: &r})
			}
		}
		if len(skipped) > 0 {
			continue
		}

		// 3. 停滞防护：有剩余任务但无就绪 → 图异常（构建期已防环，理论不可达）
		if len(readyIDs) == 0 {
			return resultsInOrder(graph, done), fmt.Errorf(
				"orchestrate: 调度停滞，%d 个任务无法推进", n-len(done))
		}

		// 4. 批内并行执行（信号量限制并发）
		sem := make(chan struct{}, e.maxParallel)
		batch := make([]TaskSpec, 0, len(readyIDs))
		for _, id := range readyIDs {
			t, _ := graph.Task(id)
			batch = append(batch, t)
		}
		var wg sync.WaitGroup
		for _, t := range batch {
			wg.Add(1)
			go func(t TaskSpec) {
				defer wg.Done()
				// 等待并发名额或 ctx 取消
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				r, err := e.runOne(ctx, graph, done, t, depth, prefix)
				doneMu.Lock()
				done[t.ID] = r
				doneSet[t.ID] = true
				doneMu.Unlock()
				if r.Status == TaskFailed {
					failedMu.Lock()
					failed[t.ID] = true
					failedMu.Unlock()
				}
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
				}
				if e.onResult != nil {
					e.onResult(r)
				}
			}(t)
		}
		wg.Wait()

		if ctx.Err() != nil {
			return resultsInOrder(graph, done), ctx.Err()
		}
		if firstErr != nil && e.policy == FailAbort {
			return resultsInOrder(graph, done), fmt.Errorf("orchestrate: 任务失败，中止编排: %v", firstErr)
		}
	}
	return resultsInOrder(graph, done), nil
}

// runOne 执行单个子任务并补全时间统计。
// 复合任务（t.Subgraph 非空）先递归执行子图：子图成果汇总为上游摘要注入
// 任务自身 Runner，子图 token 用量累计进本任务成本。
func (e *Executor) runOne(ctx context.Context, graph *TaskGraph, done map[string]TaskResult, t TaskSpec, depth int, prefix string) (TaskResult, error) {
	start := time.Now()
	// 任务开始进度事件（running）
	if e.onProgress != nil {
		e.onProgress(ProgressEvent{Type: ProgressTaskStarted, TaskID: prefix + t.ID, Status: TaskRunning})
	}
	// 上游成果摘要：拼接直接依赖的成功输出，供子 Agent 参考。
	upstream := upstreamSummary(t, done)
	var subUsage llm.Usage
	if len(t.Subgraph) > 0 {
		// 深度防御（图构建期已校验，这里兜底防止绕过 NewTaskGraph 的图）。
		if depth+1 > e.maxDepth {
			r := failedTaskResult(t, start, fmt.Sprintf("子图嵌套超过深度上限 %d", e.maxDepth))
			e.emitProgress(prefix+t.ID, r)
			return r, fmt.Errorf("orchestrate: 任务 %s 子图深度超过上限 %d", t.ID, e.maxDepth)
		}
		subGraph, err := NewTaskGraph(t.Subgraph)
		if err != nil {
			r := failedTaskResult(t, start, err.Error())
			e.emitProgress(prefix+t.ID, r)
			return r, fmt.Errorf("orchestrate: 任务 %s 子图非法: %w", t.ID, err)
		}
		subResults, err := e.runGraph(ctx, subGraph, depth+1, prefix+t.ID+".")
		if err != nil {
			r := failedTaskResult(t, start, err.Error())
			e.emitProgress(prefix+t.ID, r)
			return r, fmt.Errorf("orchestrate: 任务 %s 子图执行失败: %w", t.ID, err)
		}
		if sum := subgraphSummary(subResults); sum != "" {
			upstream = "以下是本任务进一步拆解出的子任务成果，请整合进你的输出：\n" + sum + "\n\n" + upstream
		}
		subUsage = subgraphUsage(subResults)
	}
	res, err := e.runner(ctx, t, upstream)
	res.TaskID = t.ID
	res.StartedAt = start
	res.FinishedAt = time.Now()
	res.Duration = res.FinishedAt.Sub(start).Seconds()
	res.inputs = t.Deps
	if err != nil {
		res.Status = TaskFailed
		if res.Error == "" {
			res.Error = err.Error()
		}
	} else if res.Status == "" {
		res.Status = TaskCompleted
	}
	// 子图 token 用量累计进复合任务（成本核算不遗漏递归层）。
	res.Usage.PromptTokens += subUsage.PromptTokens
	res.Usage.CompletionTokens += subUsage.CompletionTokens
	res.Usage.TotalTokens += subUsage.TotalTokens
	e.emitProgress(prefix+t.ID, res)
	return res, err
}

// emitProgress 发送任务结束进度事件（终态）。
func (e *Executor) emitProgress(taskID string, r TaskResult) {
	if e.onProgress != nil {
		res := r
		e.onProgress(ProgressEvent{Type: ProgressTaskFinished, TaskID: taskID, Status: res.Status, Result: &res})
	}
}

// failedTaskResult 构造失败结果（补全时间统计）。
func failedTaskResult(t TaskSpec, start time.Time, msg string) TaskResult {
	return TaskResult{
		TaskID: t.ID, Role: t.Role, Status: TaskFailed, Error: msg,
		StartedAt:  start,
		FinishedAt: time.Now(),
		Duration:   time.Since(start).Seconds(),
	}
}

// subgraphSummary 汇总子图内已完成任务的输出，作为复合任务整合的上下文。
// 每个子任务输出截断到 upstreamCap，防止把上层 prompt 撑爆。
func subgraphSummary(results []TaskResult) string {
	var parts []string
	for _, r := range results {
		if r.Status != TaskCompleted || r.Content == "" {
			continue
		}
		c := r.Content
		if len(c) > upstreamCap {
			c = c[:upstreamCap] + "\n…（已截断，子任务成果过长）"
		}
		parts = append(parts, "【"+r.TaskID+"】\n"+c)
	}
	return strings.Join(parts, "\n\n")
}

// subgraphUsage 累计子图全部任务的 token 用量。
func subgraphUsage(results []TaskResult) llm.Usage {
	var u llm.Usage
	for _, r := range results {
		u.PromptTokens += r.Usage.PromptTokens
		u.CompletionTokens += r.Usage.CompletionTokens
		u.TotalTokens += r.Usage.TotalTokens
	}
	return u
}

// upstreamSummary 汇总任务直接依赖的成功输出，作为子任务上下文。
// 每个上游成果截断到 upstreamCap 字符，防止长输出（如万字正文）把下游
// prompt 撑爆导致 LLM 超时；关键信息在开头，截尾不影响子任务理解。
func upstreamSummary(t TaskSpec, done map[string]TaskResult) string {
	var parts []string
	for _, dep := range t.Deps {
		if r, ok := done[dep]; ok && r.Status == TaskCompleted && r.Content != "" {
			c := r.Content
			if len(c) > upstreamCap {
				c = c[:upstreamCap] + "\n…（已截断，上游成果过长）"
			}
			parts = append(parts, "【"+dep+"】\n"+c)
		}
	}
	return strings.Join(parts, "\n\n")
}

// upstreamCap 每个上游成果注入下游的最大字符数（防 prompt 膨胀）。
const upstreamCap = 2000

// dependsOnFailed 判断任务（直接或间接）依赖链上是否存在失败节点。
// visited 防重复遍历（图无环，理论上不会重复，防御性保留）。
func dependsOnFailed(g *TaskGraph, t TaskSpec, failed map[string]bool, visited map[string]bool) bool {
	if visited[t.ID] {
		return false
	}
	visited[t.ID] = true
	for _, dep := range t.Deps {
		if failed[dep] {
			return true
		}
		dt, err := g.Task(dep)
		if err != nil {
			continue
		}
		if dependsOnFailed(g, dt, failed, visited) {
			return true
		}
	}
	return false
}

// resultsInOrder 按 DAG 定义顺序返回结果（稳定输出，便于展示/合并）。
func resultsInOrder(g *TaskGraph, done map[string]TaskResult) []TaskResult {
	out := make([]TaskResult, 0, len(g.Tasks))
	for _, t := range g.Tasks {
		if r, ok := done[t.ID]; ok {
			out = append(out, r)
		}
	}
	return out
}
