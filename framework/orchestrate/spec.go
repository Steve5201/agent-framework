// Package orchestrate 提供多 Agent 编排能力：任务分解 → DAG 调度执行 → 结果合并。
//
// 设计理念：编排器 = 一个 Agent + 编排工具集。本包提供"分解（Planner）、
// 调度（Executor）、合并（Aggregator）"三块可组合的构件，宿主（agent-service
// 或示例程序）把它们接起来即可，不依赖本包之外的业务工具。
//
// 子任务执行模型：每个子任务 = 独立 agent.Session（独立 system prompt /
// 工具集 / 记忆窗口）。执行由宿主提供的 Runner 完成——Runner 按子任务角色
// 构建 Session 并运行，本包只负责"何时跑谁、失败怎么处理、结果怎么汇总"。
//
// 依赖纪律：orchestrate → llm / schema（不反向依赖 agent / tool / 业务代码）。
package orchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Steve5201/agent-framework/llm"
)

// TaskStatus 子任务生命周期状态（P4-D 状态机的节点粒度）。
type TaskStatus string

// 任务状态全集。
const (
	// TaskPending 已计划、尚未开始。
	TaskPending TaskStatus = "pending"
	// TaskRunning 正在执行。
	TaskRunning TaskStatus = "running"
	// TaskCompleted 执行成功。
	TaskCompleted TaskStatus = "completed"
	// TaskFailed 执行失败。
	TaskFailed TaskStatus = "failed"
	// TaskSkipped 因上游失败被跳过（FailSkipDependents 策略）。
	TaskSkipped TaskStatus = "skipped"
)

// TaskSpec 一个子任务的定义：角色 + 目标 + 输入 + 期望输出 + 依赖。
//
// 一张编排 DAG 由若干 TaskSpec 组成。Deps 表达"必须先完成谁"：
// 无依赖（入度 0）的任务可并行执行，依赖全部完成才解锁下游。
//
// Subgraph 支持任务递归拆解（P4-J 阶段3）：一个任务可再携带一张子任务
// DAG。执行时先递归调度 subgraph，把子任务成果汇总为上游摘要注入该任务
// 自身的 Agent（由它整合产出），子任务 token 用量计入该任务成本。
type TaskSpec struct {
	// ID 任务唯一标识（如 "research"），在 DAG 内必须唯一。
	ID string `json:"id"`

	// Role 角色名：对应宿主角色池里的角色配置（决定 system prompt /
	// 工具集 / 模型）。空 = 用宿主默认角色。
	Role string `json:"role"`

	// Goal 本任务要达成的明确目标（作为子 Agent 的用户消息）。
	Goal string `json:"goal"`

	// Input 可选的输入/上下文（宿主负责填充；与上游结果摘要互不冲突）。
	Input string `json:"input,omitempty"`

	// OutputSchema 期望输出的 JSON Schema（对象）。非 nil 时宿主应：
	//   - 在子任务 system prompt 追加"以 JSON 输出"约束；
	//   - 用 ValidateStructuredOutput 解析校验后填充 TaskResult.Data。
	// nil = 自由文本输出，不校验。
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`

	// Deps 依赖的任务 ID 列表：全部完成后本任务才可执行。
	Deps []string `json:"deps,omitempty"`

	// Subgraph 可选的子任务 DAG（递归拆解，P4-J 阶段3）。
	// 非空时本任务是"复合任务"：先递归执行子图（深度/总数受
	// MaxSubgraphDepth / MaxTotalTasks 约束），子图成果汇总为上游摘要，
	// 再由本任务自身的 Agent 整合产出。
	Subgraph []TaskSpec `json:"subgraph,omitempty"`

	// Model 覆盖模型名（空 = 用角色配置的默认模型）。
	Model string `json:"model,omitempty"`

	// MaxRounds 覆盖消息循环轮数（<=0 = 用角色配置默认）。
	MaxRounds int `json:"max_rounds,omitempty"`

	// MaxTokens 单次生成的 token 上限（0 = 不限制）。
	MaxTokens int `json:"max_tokens,omitempty"`
}

// UnmarshalJSON 宽容反序列化：动态分解时模型偶尔会把 subgraph 输出为单个
// 对象（如 {"id":"sub1",...}）而非数组，导致标准解析整段失败（线上报错：
// cannot unmarshal object into ... Subgraph of type []orchestrate.TaskSpec）。
// 此处标准解析失败后，若 subgraph 为对象则自动包装为单元素数组再重试，
// 使动态编排在模型轻微格式偏差下仍可用（字段顺序不影响语义）。
func (t *TaskSpec) UnmarshalJSON(data []byte) error {
	type plain TaskSpec // 别名，避免方法递归
	var p plain
	if err := json.Unmarshal(data, &p); err == nil {
		*t = TaskSpec(p)
		return nil
	}
	// 标准解析失败：仅 subgraph 结构问题时可修复，其余字段原样透传。
	var raw struct {
		ID           string          `json:"id"`
		Role         string          `json:"role"`
		Goal         string          `json:"goal"`
		Input        string          `json:"input,omitempty"`
		OutputSchema json.RawMessage `json:"output_schema,omitempty"`
		Deps         []string        `json:"deps,omitempty"`
		Subgraph     json.RawMessage `json:"subgraph,omitempty"`
		Model        string          `json:"model,omitempty"`
		MaxRounds    int             `json:"max_rounds,omitempty"`
		MaxTokens    int             `json:"max_tokens,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.ID, p.Role, p.Goal = raw.ID, raw.Role, raw.Goal
	p.Input, p.OutputSchema, p.Deps = raw.Input, raw.OutputSchema, raw.Deps
	p.Model, p.MaxRounds, p.MaxTokens = raw.Model, raw.MaxRounds, raw.MaxTokens
	sg := bytes.TrimSpace(raw.Subgraph)
	if len(sg) > 0 {
		if sg[0] == '{' {
			// 对象 → 包装为单元素数组；子元素仍走本方法，递归宽容。
			sg = append(append([]byte{'['}, sg...), ']')
		}
		if err := json.Unmarshal(sg, &p.Subgraph); err != nil {
			return err
		}
	}
	*t = TaskSpec(p)
	return nil
}

// TaskResult 一个子任务的执行结果（含状态与成本统计）。
type TaskResult struct {
	TaskID     string     `json:"task_id"`
	Role       string     `json:"role"` // 角色名（research/outline/...，供展示与入库）
	Status     TaskStatus `json:"status"`
	Content    string     `json:"content"`          // 子 Agent 原始输出（自由文本或 JSON）
	Data       any        `json:"data,omitempty"`   // 结构化输出（OutputSchema 解析校验后）
	Error      string     `json:"error,omitempty"`  // 失败原因（TaskFailed 时非空）
	Usage      llm.Usage  `json:"usage"`            // 累计 token 用量（成本核算）
	Duration   float64    `json:"duration_seconds"` // 执行耗时（秒）
	StartedAt  time.Time  `json:"started_at,omitempty"`
	FinishedAt time.Time  `json:"finished_at,omitempty"`
	inputs     []string   // 内部：执行时记录的直接依赖 ID（测试/诊断用）
}

// Validate TaskSpec 基本合法性校验。
func (t TaskSpec) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("orchestrate: 任务 ID 不能为空")
	}
	if t.Role == "" {
		return fmt.Errorf("orchestrate: 任务 %s 的角色不能为空", t.ID)
	}
	if t.Goal == "" {
		return fmt.Errorf("orchestrate: 任务 %s 的目标不能为空", t.ID)
	}
	return nil
}

// ValidateStructuredOutput 解析并校验子任务输出。
//
// 若任务声明了 OutputSchema：把 content 解析为 JSON 对象，并校验 schema 的
// required 字段是否存在。返回解析后的对象（Data）；未声明 schema 时返回
// (nil, nil)，不校验。宿主 Runner 在子任务完成后调用，校验失败即视为
// 该任务失败（结构化输出不符合约定）。
func ValidateStructuredOutput(content string, t TaskSpec) (any, error) {
	if len(t.OutputSchema) == 0 {
		return nil, nil
	}
	var raw any
	if err := json.Unmarshal([]byte(extractJSON(content)), &raw); err != nil {
		return nil, fmt.Errorf("orchestrate: 任务 %s 输出不是合法 JSON: %w", t.ID, err)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("orchestrate: 任务 %s 输出应为 JSON 对象", t.ID)
	}
	var sch struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(t.OutputSchema, &sch)
	for _, f := range sch.Required {
		if _, ok := obj[f]; !ok {
			return nil, fmt.Errorf("orchestrate: 任务 %s 输出缺少必填字段 %q", t.ID, f)
		}
	}
	return obj, nil
}
