// orchestrator-demo 多 Agent 编排示例：教研场景（固定编排 → 可选动态分解）。
//
// 演示能力：
//   - 固定编排：研究 → 大纲 → 内容 → 审核 的教研 DAG（入度0并行 + 完成解锁下游）；
//   - 动态分解（--dynamic）：LLM 把用户目标实时拆成子任务 DAG；
//   - 子任务 = 独立 agent.Session（各自人格 + 记忆 + 工具）；
//   - 结果合并（Aggregator）精炼为最终回答。
//
// 前置条件：设置环境变量 DEEPSEEK_API_KEY。
//
// 运行：
//
//	cd framework
//	go run ./examples/orchestrator-demo                       # 固定编排
//	go run ./examples/orchestrator-demo -goal "为"电子信息工程"专业写一节课程教案" -dynamic
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/orchestrate"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// roleConfig 角色配置：独立人格（system prompt）+ 模型 + 轮数上限。
type roleConfig struct {
	SystemPrompt string
	Model        string
	MaxRounds    int
}

// rolePool 教研场景角色池。子任务按 Role 取配置构建独立 Session。
var rolePool = map[string]roleConfig{
	"research": {
		SystemPrompt: "你是课程研究助理。你的职责是围绕主题检索、梳理背景资料与知识点，输出要点式中文摘要，条理清晰、只列干货。",
		Model:        llm.DeepSeekFlashModel,
		MaxRounds:    5,
	},
	"outline": {
		SystemPrompt: "你是教学设计专家。你的职责是把资料组织成一份逻辑严谨的课程大纲（章节/小节/教学目标），输出结构化列表。",
		Model:        llm.DeepSeekFlashModel,
		MaxRounds:    5,
	},
	"content": {
		SystemPrompt: "你是课程内容撰写专家。你的职责是基于大纲与资料撰写完整正文，内容详实、通俗、可直接用于教学。",
		Model:        llm.DeepSeekFlashModel,
		MaxRounds:    6,
	},
	"review": {
		SystemPrompt: "你是教学质量审核专家。你的职责是审查正文：指出事实错误、逻辑漏洞、结构问题与改进建议。",
		Model:        llm.DeepSeekFlashModel,
		MaxRounds:    5,
	},
	"worker": {
		SystemPrompt: "你是通用任务执行助理，按要求高质量完成目标。",
		Model:        llm.DeepSeekFlashModel,
		MaxRounds:    5,
	},
}

func main() {
	// 安全红线：API Key 只从环境变量读取，禁止硬编码。
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Println("错误：请先设置环境变量 DEEPSEEK_API_KEY")
		os.Exit(1)
	}

	goal := flag.String("goal", "为《信号与系统》「卷积」一节设计一堂 45 分钟的教学方案",
		"编排目标（用户想要完成的事情）")
	dynamic := flag.Bool("dynamic", false, "使用 LLM 动态分解（默认固定教研模板）")
	flag.Parse()

	provider, err := llm.NewOpenAICompatible(llm.Config{
		Name:    "deepseek",
		BaseURL: llm.DeepSeekBaseURL,
		APIKey:  apiKey,
		Model:   llm.DeepSeekFlashModel,
		Timeout: 180 * time.Second, // 子任务可能较长（含审核长输入），放宽默认 60s
	})
	if err != nil {
		fmt.Println("初始化模型失败:", err)
		os.Exit(1)
	}

	// 1. Planner：固定模板 or 动态分解
	var planner orchestrate.Planner
	if *dynamic {
		planner = orchestrate.NewLLMPlanner(provider, llm.DeepSeekFlashModel)
		fmt.Println("▶ 编排模式：动态分解（LLM 实时拆解任务 DAG）")
	} else {
		planner, err = orchestrate.NewFixedPlanner(teachingDAG())
		if err != nil {
			fmt.Println("构建固定模板失败:", err)
			os.Exit(1)
		}
		fmt.Println("▶ 编排模式：固定模板（教研 DAG：研究→大纲→内容→审核）")
	}

	// 2. Executor：子任务 = 独立 Session，入度0并行、失败降级跳过下游
	runner := buildRunner(provider)
	executor := orchestrate.NewExecutor(
		runner,
		orchestrate.WithMaxParallel(2),
		orchestrate.WithFailPolicy(orchestrate.FailSkipDependents),
		orchestrate.WithResultCallback(func(r orchestrate.TaskResult) {
			fmt.Printf("  [%s] %-10s 耗时 %5.1fs  tokens=%d\n",
				r.Status, r.TaskID, r.Duration, r.Usage.TotalTokens)
		}),
	)

	// 3. Aggregator：结果合并
	agg := orchestrate.NewAggregator(provider, llm.DeepSeekFlashModel)

	// 4. 编排器门面
	o := orchestrate.NewOrchestrator(planner, executor, agg)

	fmt.Printf("\n◆ 用户目标：%s\n", *goal)
	start := time.Now()
	res, err := o.Run(context.Background(), *goal)
	if err != nil {
		fmt.Println("\n✗ 编排失败:", err)
		os.Exit(1)
	}
	elapsed := time.Since(start)

	fmt.Printf("\n◆ 编排完成：共 %d 个子任务，总耗时 %.1fs，累计 tokens=%d\n",
		len(res.Tasks), elapsed.Seconds(), res.TotalUsage.TotalTokens)
	for _, r := range res.Tasks {
		summary := r.Content
		if len(summary) > 60 {
			summary = summary[:60] + "…"
		}
		fmt.Printf("  %-10s %-9s %s\n", r.TaskID, r.Status, summary)
	}
	fmt.Printf("\n========== 最终回答 ==========\n%s\n", res.Final)
}

// teachingDAG 教研固定模板：研究 →（解锁）大纲 →（解锁）内容 →（解锁）审核。
// Goal 里的 {goal} 占位符由 FixedPlanner.Plan 替换为用户实际目标，
// 保证每个子任务（尤其无依赖的入口任务）都清楚最终要做什么。
func teachingDAG() []orchestrate.TaskSpec {
	return []orchestrate.TaskSpec{
		{
			ID:   "research",
			Role: "research",
			Goal: "围绕用户目标「{goal}」，梳理核心概念、重难点、常见误区与教学建议，输出要点式摘要。",
		},
		{
			ID:   "outline",
			Role: "outline",
			Goal: "围绕用户目标「{goal}」，设计本节教学大纲：教学目标、教学环节、时间分配、例题安排。",
			Deps: []string{"research"},
		},
		{
			ID:   "content",
			Role: "content",
			Goal: "围绕用户目标「{goal}」，依据大纲撰写完整教案正文：导入、讲解、例题、小结、作业。",
			Deps: []string{"research", "outline"},
		},
		{
			ID:   "review",
			Role: "review",
			Goal: "围绕用户目标「{goal}」，审核教案：指出逻辑问题、知识错误、可改进之处，并给出修改建议。",
			Deps: []string{"content"},
		},
	}
}

// buildRunner 构造子任务执行器：按角色构建独立 Session 运行一次循环。
func buildRunner(provider llm.Provider) orchestrate.Runner {
	return func(ctx context.Context, task orchestrate.TaskSpec, upstream string) (orchestrate.TaskResult, error) {
		rc, ok := rolePool[task.Role]
		if !ok {
			rc = rolePool["worker"]
		}
		model := task.Model
		if model == "" {
			model = rc.Model
		}
		maxRounds := task.MaxRounds
		if maxRounds <= 0 {
			maxRounds = rc.MaxRounds
		}

		cfg := schema.AgentConfig{
			Model:        model,
			SystemPrompt: rc.SystemPrompt + schemaHint(task.OutputSchema),
			MaxRounds:    maxRounds,
			Memory:       schema.MemoryConfig{MaxMessages: 10},
		}
		// 子任务工具集：轻量内置工具（无外部依赖）。
		reg := tool.NewRegistry()
		if err := reg.Register(tool.CalculatorTool{}); err != nil {
			return orchestrate.TaskResult{}, err
		}

		s, err := agent.NewSession(cfg, provider, reg)
		if err != nil {
			return orchestrate.TaskResult{}, err
		}

		input := task.Goal
		if task.Input != "" {
			input = task.Input + "\n\n" + task.Goal
		}
		if upstream != "" {
			input = "以下是已完成的关联成果，供你参考：\n" + upstream + "\n\n请完成你的任务：\n" + input
		}

		res, err := s.Run(ctx, input)
		if err != nil {
			return orchestrate.TaskResult{TaskID: task.ID, Status: orchestrate.TaskFailed}, err
		}
		data, verr := orchestrate.ValidateStructuredOutput(res.Content, task)
		if verr != nil {
			return orchestrate.TaskResult{TaskID: task.ID, Status: orchestrate.TaskFailed}, verr
		}
		return orchestrate.TaskResult{
			TaskID: task.ID, Status: orchestrate.TaskCompleted,
			Content: res.Content, Data: data, Usage: res.Usage,
		}, nil
	}
}

// schemaHint 若任务声明输出 schema，则追加"JSON 输出"约束。
func schemaHint(sch json.RawMessage) string {
	if len(sch) == 0 {
		return ""
	}
	return "\n\n你的最终回答必须是一个严格合法的 JSON 对象，匹配以下 JSON Schema：\n" +
		string(sch) + "\n只输出 JSON，不要任何解释文字。"
}
