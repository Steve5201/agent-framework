package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

// Planner 任务分解策略：把用户目标变成一张任务 DAG。
type Planner interface {
	// Plan 把 goal 分解为 TaskGraph。调用方随后用 Executor 执行。
	Plan(ctx context.Context, goal string) (*TaskGraph, error)
}

// ---------------------------------------------------------------------------
// 固定编排：预定义模板
// ---------------------------------------------------------------------------

// FixedPlanner 固定模板编排：按预设 DAG 执行（流程写死、可控、便宜）。
// 适合流程明确的场景（教研流水线：研究→大纲→内容→审核）。
//
// 模板任务里的 Goal/Input 支持 {goal} 占位符：Plan 时替换为用户实际目标，
// 使每个子任务都能看到"最终要做什么"（尤其无依赖的入口任务）。
type FixedPlanner struct {
	base []TaskSpec // 基础模板（构建期已校验 DAG 合法性）
}

// NewFixedPlanner 用预设任务构建固定模板（构建期即校验 DAG 合法性）。
func NewFixedPlanner(tasks []TaskSpec) (*FixedPlanner, error) {
	if _, err := NewTaskGraph(tasks); err != nil {
		return nil, err
	}
	return &FixedPlanner{base: tasks}, nil
}

// Plan 把用户目标注入模板（替换 {goal} 占位符）后构建任务图。
func (p *FixedPlanner) Plan(_ context.Context, goal string) (*TaskGraph, error) {
	tasks := make([]TaskSpec, len(p.base))
	for i, t := range p.base {
		tasks[i] = t
		tasks[i].Goal = strings.ReplaceAll(t.Goal, goalPlaceholder, goal)
		tasks[i].Input = strings.ReplaceAll(t.Input, goalPlaceholder, goal)
	}
	return NewTaskGraph(tasks)
}

// goalPlaceholder 固定模板里表示"用户目标"的占位符。
const goalPlaceholder = "{goal}"

// ---------------------------------------------------------------------------
// 动态分解：LLM planner
// ---------------------------------------------------------------------------

// llmPlannerSystemPrompt 动态分解的系统指令：约束模型输出严格 JSON 任务清单。
// 显式声明角色枚举与 deps 语义，提高模型输出可解析率。
// 支持 subgraph 递归拆解（P4-J 阶段3）：复杂子任务可再嵌套一张任务 DAG。
const llmPlannerSystemPrompt = `你是任务分解器（Task Planner）。把用户给定的目标拆解为可并行/串行执行的子任务 DAG。

要求：
1. 每个子任务只做一件内聚的事，goal 描述具体、可执行。
2. 用 deps 表达依赖：依赖的任务必须先完成；没有依赖的任务可并行。
3. 控制规模：3~6 个子任务为宜，不要过度拆分。
4. role 使用以下角色之一：research（检索/收集资料）、outline（设计结构/大纲）、content（撰写正文内容）、review（审核校对）、format（格式整理）。不匹配时用 worker。
5. output_schema 为 JSON Schema 对象（可省略，省略表示自由文本输出）。
6. max_rounds（可选，推荐填写）：给足冗余，至少 10（子任务常需多轮工具往返，宁多勿少，避免中途轮数耗尽导致整体失败）；任务越复杂给越多，简单任务可省略（用默认）。
7. 子任务递归拆解（可选）：若某个子任务规模仍较大、还需要先收集素材再撰写等，可为它加 subgraph 字段，内部同样是 tasks 数组结构（至多嵌套 1 层，每层 2~4 个）。无需要时省略 subgraph。
8. 每个子任务应尽快完成，避免不必要的多轮往返（一次做足，不要反复试探）。
9. 只输出 JSON，不要任何解释、不要 markdown 代码块。
10. 如果用户请求非常简单、不需要拆解为多个子任务（单句问答、工具可见性询问、闲聊等），
    直接返回 {"tasks":[]}，表示无需编排——主智能体将直接回答，不要硬拆。

输出格式（严格 JSON）：
{"tasks":[{"id":"research","role":"research","goal":"...","deps":[],"max_rounds":12,"subgraph":[{"id":"sub1","role":"research","goal":"...","deps":[]}],"output_schema":{...}}]}`

// LLMPlanner 动态分解：调用 LLM 把目标实时拆成子任务 DAG。
// 适合开放性问题；成本高于固定编排（每次计划一次 LLM 调用）。
type LLMPlanner struct {
	provider llm.Provider
	model    string
	maxTasks int // 子任务数量上限（防模型过度拆分）
}

// NewLLMPlanner 创建动态分解器。model 空 = 用 provider 默认。
func NewLLMPlanner(p llm.Provider, model string) *LLMPlanner {
	return &LLMPlanner{provider: p, model: model, maxTasks: 8}
}

// Plan 调用模型生成任务 DAG 并校验合法性。
func (p *LLMPlanner) Plan(ctx context.Context, goal string) (*TaskGraph, error) {
	req := &llm.Request{
		Model: p.model,
		Messages: []schema.Message{
			{Role: schema.RoleSystem, Content: llmPlannerSystemPrompt},
			{Role: schema.RoleUser, Content: goal},
		},
	}
	resp, err := p.provider.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("orchestrate: 动态分解调用失败: %w", err)
	}
	tasks, err := parsePlanJSON(resp.Content)
	if err != nil {
		return nil, err
	}
	if len(tasks) > p.maxTasks {
		return nil, fmt.Errorf("orchestrate: 分解出 %d 个子任务，超过上限 %d", len(tasks), p.maxTasks)
	}
	return NewTaskGraph(tasks)
}

// planEnvelope 模型输出的信封结构：{"tasks": [...]}。
type planEnvelope struct {
	Tasks []TaskSpec `json:"tasks"`
}

// ErrNoPlan 表示 planner 判定当前请求无需编排（简单问题/闲聊/模型未按
// JSON 输出）。上层应回退到单 Agent 直接应答，而不是当作错误中断——
// "简单问题不强制编排"是动态模式的设计意图（P4-N）。
var ErrNoPlan = errors.New("orchestrate: 无需编排（请求过于简单）")

// parsePlanJSON 解析模型输出的任务清单（容忍 markdown 代码块包裹）。
// 三种结局：
//   - 合法 JSON 且 tasks 非空 → 返回任务清单；
//   - 合法 JSON 但 tasks 为空（模型判定无需拆解）→ 返回 ErrNoPlan；
//   - 非 JSON（模型对简单问题直接回了自然语言）→ 也返回 ErrNoPlan，
//     宁可回退直接应答，也不让"简单问题"中断报错。
func parsePlanJSON(content string) ([]TaskSpec, error) {
	data := []byte(extractJSON(content))
	var env planEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w（模型未输出任务 JSON: %v）", ErrNoPlan, err)
	}
	if len(env.Tasks) == 0 {
		return nil, ErrNoPlan
	}
	return env.Tasks, nil
}

// extractJSON 从模型回复中提取 JSON 文本：
//  1. 整体合法 → 直接用；
//  2. 被 ```json ... ``` 代码块包裹 → 剥离；
//  3. 首尾花括号之间的内容合法 → 截取；
//  4. 都失败 → 原样返回（交给调用方报错）。
func extractJSON(content string) string {
	s := strings.TrimSpace(content)
	if json.Valid([]byte(s)) {
		return s
	}
	if start := strings.Index(s, "```json"); start >= 0 {
		start += len("```json")
		if end := strings.Index(s[start:], "```"); end >= 0 {
			if cand := strings.TrimSpace(s[start : start+end]); json.Valid([]byte(cand)) {
				return cand
			}
		}
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			if cand := strings.TrimSpace(s[i : j+1]); json.Valid([]byte(cand)) {
				return cand
			}
		}
	}
	return s
}
