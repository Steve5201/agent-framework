package orchestrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/Steve5201/agent-framework/llm"
)

// Orchestrator 编排器高层入口：planner 分解 → executor 调度执行 → aggregator 合并。
// 是"固定编排 / 动态分解"两套流程的统一门面。
//
// 一个 Orchestrator 实例绑定：Planner、Executor（含 Runner）、Aggregator。
// 每次 Run 会重新 Plan（动态分解可产出新 DAG），故 Executor 每次接收新图。
type Orchestrator struct {
	planner  Planner
	executor *Executor
	agg      *Aggregator
	progress ProgressFunc // 可选：Run 时透传给 executor 并补发 run 级事件
}

// NewOrchestrator 创建编排器。
func NewOrchestrator(p Planner, e *Executor, agg *Aggregator) *Orchestrator {
	return &Orchestrator{planner: p, executor: e, agg: agg}
}

// SetProgress 设置编排进度回调（任务级 + run 级事件，见 ProgressEvent）。
// 须在 Run 之前调用；线程不安全（编排器一次 Run 对应一次回调配置）。
func (o *Orchestrator) SetProgress(f ProgressFunc) { o.progress = f }

// emit 发进度事件（nil 回调时静默跳过）。
func (o *Orchestrator) emit(ev ProgressEvent) {
	if o.progress != nil {
		o.progress(ev)
	}
}

// RunResult 一次编排的完整结果。
type RunResult struct {
	Goal       string       // 用户目标
	Final      string       // 合并后的最终回答
	Tasks      []TaskResult // 全部子任务结果（按 DAG 定义顺序）
	TotalUsage llm.Usage    // 子任务累计 token 用量（不含 planner/aggregator）
}

// planAndExecute 编排公共前半段：planner 分解 → executor 调度执行。
// Run 与 RunStream 共用，聚合阶段再各自选择流式/非流式。
// planner 判定无需编排（ErrNoPlan）时返回 nil 结果，由 Run/RunStream 回退。
func (o *Orchestrator) planAndExecute(ctx context.Context, goal string) ([]TaskResult, error) {
	graph, err := o.planner.Plan(ctx, goal)
	if err != nil {
		// 无需编排（简单问题/模型未按 JSON 输出）：不是失败——
		// 回退直接应答由上层决定，不广播 run_failed。
		if errors.Is(err, ErrNoPlan) {
			return nil, nil
		}
		o.emit(ProgressEvent{Type: ProgressRunFailed, Error: "计划失败: " + err.Error()})
		return nil, fmt.Errorf("orchestrate: 计划失败: %w", err)
	}
	results, err := o.executor.Run(ctx, graph)
	if err != nil {
		o.emit(ProgressEvent{Type: ProgressRunFailed, Error: "执行失败: " + err.Error()})
		return nil, fmt.Errorf("orchestrate: 执行失败: %w", err)
	}
	return results, nil
}

// finalizeUsage 累计子任务 token 用量（planner/aggregator 不计入）。
func finalizeUsage(results []TaskResult) (u llm.Usage) {
	for _, r := range results {
		u.PromptTokens += r.Usage.PromptTokens
		u.CompletionTokens += r.Usage.CompletionTokens
		u.TotalTokens += r.Usage.TotalTokens
	}
	return u
}

// Run 完整跑一遍编排：分解 → 调度 → 合并（非流式）。
// 若已 SetProgress，则任务开始/结束与 run 成功/失败都会发进度事件。
func (o *Orchestrator) Run(ctx context.Context, goal string) (*RunResult, error) {
	// 透传进度回调（同包访问私有字段）。仅当编排器显式 SetProgress 后才覆盖
	// executor 自身的 WithProgress 回调；否则保留 executor 直接注入的回调，
	// 避免把 executor 级进度误清空（agent-service 编排接入即走 executor.WithProgress 上报）。
	if o.progress != nil {
		o.executor.onProgress = o.progress
	}

	results, err := o.planAndExecute(ctx, goal)
	if err != nil {
		return nil, err
	}
	// 无需编排（空计划）：不调用聚合器（它依赖子任务成果），直接返回空结果，
	// 由调用方（agent-service）回退单 Agent 直接应答。
	if len(results) == 0 {
		o.emit(ProgressEvent{Type: ProgressRunCompleted})
		return &RunResult{Goal: goal}, nil
	}
	final, err := o.agg.Aggregate(ctx, goal, results)
	if err != nil {
		o.emit(ProgressEvent{Type: ProgressRunFailed, Error: "合并失败: " + err.Error()})
		return nil, fmt.Errorf("orchestrate: 合并失败: %w", err)
	}
	out := &RunResult{Goal: goal, Final: final, Tasks: results}
	out.TotalUsage = finalizeUsage(results)
	o.emit(ProgressEvent{Type: ProgressRunCompleted})
	return out, nil
}

// RunStream 与 Run 同流程，但聚合阶段走流式：最终回答逐增量经 onDelta
// 回调（打字机效果），同时返回完整 RunResult 供落库/用量统计。
func (o *Orchestrator) RunStream(ctx context.Context, goal string, onDelta func(string)) (*RunResult, error) {
	if o.progress != nil {
		o.executor.onProgress = o.progress
	}

	results, err := o.planAndExecute(ctx, goal)
	if err != nil {
		return nil, err
	}
	// 无需编排（空计划）：跳过聚合器，返回空结果（调用方回退直接应答）。
	if len(results) == 0 {
		o.emit(ProgressEvent{Type: ProgressRunCompleted})
		return &RunResult{Goal: goal}, nil
	}
	final, err := o.agg.AggregateStream(ctx, goal, results, onDelta)
	if err != nil {
		o.emit(ProgressEvent{Type: ProgressRunFailed, Error: "合并失败: " + err.Error()})
		return nil, fmt.Errorf("orchestrate: 合并失败: %w", err)
	}
	out := &RunResult{Goal: goal, Final: final, Tasks: results}
	out.TotalUsage = finalizeUsage(results)
	o.emit(ProgressEvent{Type: ProgressRunCompleted})
	return out, nil
}
