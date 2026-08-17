package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/orchestrate"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// 多智能体编排接入（P4-E 轻量版）
//
// 设计：编排器 = framework/orchestrate.Orchestrator（固定教研模板 + 内置角色池）。
// 子任务 = 独立 agent.Session（纯文本角色、无工具、独立记忆），复用 s.provider
// 与 header 注入链路。进度经 obs.OnTaskStatus 实时下发（前端渲染节点状态流）。
// 最终回答作为 assistant 消息落库（轻量持久化）；过程输出另存 orchestration_runs
// 表（P4-I 过程入库，含子任务终态/Error/耗时/token，供会话与管理端复盘）。
// ---------------------------------------------------------------------------

// roleSpec 编排子任务角色定义（框架内置教研角色池）。
type roleSpec struct {
	SystemPrompt string
	Model        string // 空 = 用会话默认模型
	MaxRounds    int
	TimeoutSec   int // 角色级超时秒数（0 = 跟随全局 AGENT_ORCH_SUBTASK_TIMEOUT_SEC）
}

// orchestrationRoles 内置教研角色池（P4-E 轻量版：框架内置，后期可配置化）。
// role 语义：research（检索/梳理）→ outline（设计大纲）→ content（撰写正文）→
// review（审核校稿）；worker 为兜底通用角色。
//
// 轮数策略（P4-L 宽松化）：子任务常带工具多轮往返，轮数给足冗余——硬限制
// 放宽（宁多勿少，保证任务成功），同时每个角色的 SystemPrompt 均带"尽快
// 完成、避免不必要往返"的软限制控制节奏与 token 成本。
//
// 角色级超时（P4-L 细化）：默认全局 30 分钟，这里按角色负载差异化——
// content（长文撰写，最耗时）给满 30 分钟；outline（大纲/多资料梳理）25 分钟；
// research（检索多轮往返）与 review/worker 20 分钟。角色级非零时覆盖全局值，
// 兼顾"长任务不误杀"与"短任务不空耗"。
var orchestrationRoles = map[string]roleSpec{
	"research": {
		// 检索类任务带 web_search 等工具，一次搜索往返消耗一轮，轮数不足时
		// 资料较多容易"轮数耗尽仍未收敛"（线上固定编排 research 失败即此因）。
		// 给足冗余并提示"资料充分即输出"，避免反复空检索烧 token。
		SystemPrompt: "你是课程研究助理。你的职责是围绕主题检索、梳理背景资料与知识点，输出要点式中文摘要，条理清晰、只列干货。注意：资料收集充分后立即输出摘要，不要反复执行相同或相似的检索；尽快完成，避免不必要的多轮往返。",
		MaxRounds:    12,
		TimeoutSec:   1200, // 20 分钟：多轮检索的兜底红线
	},
	"outline": {
		SystemPrompt: "你是教学设计专家。你的职责是把资料组织成一份逻辑严谨的课程大纲（章节/小节/教学目标），输出结构化列表。资料已充分时无需再检索，尽快产出大纲，避免不必要的多轮往返。",
		MaxRounds:    10,
		TimeoutSec:   1500, // 25 分钟：资料多、要组织大纲
	},
	"content": {
		SystemPrompt: "你是课程内容撰写专家。你的职责是基于大纲与资料撰写完整正文，内容详实、通俗、可直接用于教学。大纲与资料已充分时直接撰写，避免不必要的多轮往返；尽快完成。",
		MaxRounds:    12,
		TimeoutSec:   1800, // 30 分钟：长文撰写最耗时
	},
	"review": {
		SystemPrompt: "你是教学质量审核专家。你的职责是审查正文：指出事实错误、逻辑漏洞、结构问题，并给出可落地的修改建议。基于已有内容一次性完成审查，尽快输出，避免不必要的多轮往返。",
		MaxRounds:    10,
		TimeoutSec:   1200, // 20 分钟：审稿输入大但无需检索
	},
	"worker": {
		SystemPrompt: "你是通用任务执行助理，按要求高质量完成目标。尽快完成，避免不必要的多轮往返。",
		MaxRounds:    10,
		TimeoutSec:   1200, // 20 分钟：兜底角色
	},
}

// subtaskTimeout 子任务超时解析：角色级超时（roleSpec.TimeoutSec）非零时
// 覆盖全局值；两者均为 0 表示不限制（0 秒）。
func subtaskTimeout(global time.Duration, role roleSpec) time.Duration {
	if role.TimeoutSec > 0 {
		return time.Duration(role.TimeoutSec) * time.Second
	}
	return global
}

// teachingDAG 教研固定编排模板：研究 →（解锁）大纲 →（解锁）内容 →（解锁）审核。
// Goal 里的 {goal} 由 FixedPlanner.Plan 替换为用户实际目标。
func teachingDAG() []orchestrate.TaskSpec {
	return []orchestrate.TaskSpec{
		{ID: "research", Role: "research", Goal: "围绕用户目标「{goal}」，梳理核心概念、重难点、常见误区与教学建议，输出要点式摘要。"},
		{ID: "outline", Role: "outline", Goal: "围绕用户目标「{goal}」，设计教学大纲：教学目标、教学环节、时间分配、例题安排。", Deps: []string{"research"}},
		{ID: "content", Role: "content", Goal: "围绕用户目标「{goal}」，依据大纲撰写完整教案正文：导入、讲解、例题、小结、作业。", Deps: []string{"research", "outline"}},
		{ID: "review", Role: "review", Goal: "围绕用户目标「{goal}」，审核教案：指出逻辑问题、知识错误、可改进之处，并给出修改建议。", Deps: []string{"content"}},
	}
}

// buildOrchestrator 构建编排器（内置角色池 + 子任务 Runner + 进度回调）。
// obs 为空或 OnTaskStatus 为空时进度静默（非流式 Chat 场景）。
//
// 编排方案由会话配置 OrchestratePlan 决定（P4-J 动态编排）：
//   - fixed（默认，空/缺省）→ 固定教研模板 DAG（研究→大纲→正文→审核）；
//   - dynamic → LLM 动态分解：按用户目标实时拆解子任务 DAG（更灵活，多一次
//     LLM 调用；planner 模型跟随会话所选模型）。
//
// 子任务 Runner 注入会话级工具（sessionToolRegistry），使编排过程中子任务
// 可用搜索/读文档/文档生成等能力——解决"子任务无工具只能纯文本"的局限
// （P4-J：编排子任务工具化 + 文档生成二期）。
func (s *Service) buildOrchestrator(sess *Session, obs *agent.StreamObserver) *orchestrate.Orchestrator {
	runner := func(ctx context.Context, task orchestrate.TaskSpec, upstream string) (orchestrate.TaskResult, error) {
		// 子任务过程增量透传（P4-M）：经 task_content 事件实时下发，前端节点气泡打字机渲染。
		// 中间产出可见，用户不再"卡住"；最终 Content 仍完整落库（persistOrchestrationRun）。
		// Kind 区分增量类型（P4-N）：text=正文 / reasoning=思考 / tool_start|tool_end=工具调用。
		// 思考与工具状态按 TaskID 独立下发，前端累积到对应节点，不会串到其他节点。
		var subObs *agent.StreamObserver
		if obs != nil && obs.OnTaskStatus != nil {
			send := func(kind, content string) {
				obs.OnTaskStatus(agent.TaskStatusEvent{Type: "task_content", TaskID: task.ID, Kind: kind, Content: content})
			}
			subObs = &agent.StreamObserver{
				OnContent:    func(delta string) { send("text", delta) },
				OnReasoning:  func(delta string) { send("reasoning", delta) },
				OnToolCall:   func(call schema.ToolCall) { send("tool_start", call.Name) },
				OnToolResult: func(call schema.ToolCall, _ *schema.ToolResult, _ error) { send("tool_end", call.Name) },
			}
		}
		return s.runSubTask(ctx, sess, task, upstream, subObs)
	}
	executor := orchestrate.NewExecutor(
		runner,
		orchestrate.WithMaxParallel(2), // 并行上限：受 LLM 限流约束，保守 2
		orchestrate.WithFailPolicy(orchestrate.FailSkipDependents),
		orchestrate.WithProgress(func(ev orchestrate.ProgressEvent) {
			if obs == nil || obs.OnTaskStatus == nil {
				return
			}
			var totalTokens int64
			if ev.Result != nil {
				totalTokens = int64(ev.Result.Usage.TotalTokens)
			}
			// 任务级失败原因在 Result.Error（ProgressEvent.Error 仅承载 run 级失败）；
			// 失败详情经 SSE 下发，前端节点流红色展示具体报错（P4-I）。
			errMsg := ev.Error
			if ev.Result != nil && ev.Result.Error != "" {
				errMsg = ev.Result.Error
			}
			obs.OnTaskStatus(agent.TaskStatusEvent{
				Type:        string(ev.Type),
				TaskID:      ev.TaskID,
				Status:      string(ev.Status),
				Error:       errMsg,
				TotalTokens: totalTokens,
			})
		}),
	)

	// 编排所用模型：会话快照已选优先，否则服务实例默认（planner/聚合跟随）。
	orchModel := s.model
	if sess.Config.Model != "" {
		orchModel = sess.Config.Model
	}
	var planner orchestrate.Planner
	if sess.Config.OrchestratePlan == "dynamic" {
		// 动态分解：用户目标 → LLM 实时拆解子任务 DAG。
		s.log.Info("orchestrate 使用动态分解方案", zap.Int64("session_id", sess.ID))
		planner = orchestrate.NewLLMPlanner(s.provider, orchModel)
	} else {
		// 固定教研模板（默认）：研究→大纲→正文→审核。
		if p, err := orchestrate.NewFixedPlanner(teachingDAG()); err == nil {
			planner = p
		} else {
			// 模板是编译期常量，构建失败理论不可达；兜底动态分解保证可用。
			s.log.Error("构建固定编排模板失败，回退动态分解", zap.Error(err))
			planner = orchestrate.NewLLMPlanner(s.provider, orchModel)
		}
	}
	agg := orchestrate.NewAggregator(s.provider, orchModel)
	return orchestrate.NewOrchestrator(planner, executor, agg)
}

// runSubTask 执行单个编排子任务：独立 Session（子任务工具跟随会话配置，
// 见 sessionToolRegistry——搜索/读文档/文档生成等能力在编排内可用）。
// 复用 header 注入链路（X-User-Id / X-Agent-Id）保证上游按用户/智能体域限流与
// 用量聚合正确；工具审计与会话主链路一致（auditObserver）。
//
// 韧性（P4-L）：整任务重试 + 超时。子任务可能带工具多轮往返，任一次
// LLM 调用失败（5xx/429/网络/超时）都视为整个子任务失败——此时对可重试
// 错误整任务重试（默认 1 次，指数退避），最大限度保证中间链不出错
// （避免"前面任务成功、最后一个失败"导致整体输出失败、前面 token 浪费）。
// 每次重试重新 NewSession，避免上一轮失败历史污染上下文。
// onContent 非空时子任务走流式（RunStreamWithObserver）：输出增量实时透传
// 给前端（P4-M 打字机渲染）；流式链路同时规避"非流式大请求的整体超时 504"
// （ChatStream 无 provider 整体超时，由调用方 ctx 控制——治本而非治标）。
func (s *Service) runSubTask(ctx context.Context, sess *Session, task orchestrate.TaskSpec, upstream string, subObs *agent.StreamObserver) (orchestrate.TaskResult, error) {
	role, ok := orchestrationRoles[task.Role]
	if !ok {
		role = orchestrationRoles["worker"]
	}
	model := task.Model
	if model == "" {
		model = role.Model
	}
	if model == "" {
		model = sess.Config.Model // 会话所选模型优先（动态分解场景更贴合）
	}
	if model == "" {
		model = s.model
	}
	maxRounds := task.MaxRounds
	if maxRounds <= 0 {
		maxRounds = role.MaxRounds
	}
	if maxRounds <= 0 {
		maxRounds = 6
	}

	input := task.Goal
	if task.Input != "" {
		input = task.Input + "\n\n" + task.Goal
	}
	if upstream != "" {
		input = "以下是已完成的关联成果，供你参考：\n" + upstream + "\n\n请完成你的任务：\n" + input
	}

	// 工具审计归属当前会话（编排过程工具调用与会话审计记录统一）；
	// registry 同一请求内配置不变，循环外构建一次，重试复用。
	audit := s.auditObserver(sess.UserID, sess.ID)
	reg := s.sessionToolRegistry(sess.Config)
	// 子代理不暴露需宿主外部执行的工具（local_shell 等 External=true）：
	// 编排子任务运行在服务端内部、无桌面确认通道——保留会让模型调用后直接
	// 报错（框架 execExternal 对未配置 AsyncRunner 的会话报错）。业界标准：
	// 子代理工具集 = 主会话工具集 - 宿主专用（人工确认）工具。
	reg = stripExternalTools(reg)
	// 子任务瘦身（P4-M）：按白名单只保留基础轻量工具，排除 skills/MCP/
	// code_executor 等重工具，显著减小请求体（大请求是上游 504 放大器）。
	reg = filterSubtaskTools(reg)

	// execute 执行一次完整的子任务（全新 Session；Run 内部会累积
	// user/assistant/tool 消息到短期记忆，重试必须重建以保证干净上下文）。
	execute := func() (*agent.Result, error) {
		cfg := schema.AgentConfig{
			Model:        model,
			SystemPrompt: role.SystemPrompt,
			MaxRounds:    maxRounds,
			Memory:       schema.MemoryConfig{MaxMessages: 12},
		}
		opts := []agent.Option{agent.WithToolAuditor(audit)}
		if s.autoApproveTools {
			// 本地个人使用：L2/L3 工具自动放行（审计见 auditObserver），与主会话一致。
			opts = append(opts, agent.WithApprovalFunc(s.approveToolCall))
		}
		ag, err := agent.NewSession(cfg, s.provider, reg, opts...)
		if err != nil {
			return nil, err
		}
		runCtx := withUserHeaders(ctx, sess.UserID)
		if sess.AgentID != "" {
			runCtx = llm.WithHeader(runCtx, "X-Agent-Id", sess.AgentID)
		}
		// 子任务级超时（默认 30 分钟，0 = 不限制）：编排常带工具多轮往返，
		// 全局 LLM 超时偏保守，单独放宽避免长任务被误杀。角色级超时
		//（roleSpec.TimeoutSec）非零时覆盖全局值——长角色（content）放宽、
		// 短角色（research/worker）收紧，见 orchestrationRoles 注释。
		if timeout := subtaskTimeout(s.orchSubtaskTimeout, role); timeout > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(runCtx, timeout)
			defer cancel()
		}
		start := time.Now()
		// 统一走流式执行（P4-M）：子任务输出去流式化，避免非流式大请求被
		// provider 整体超时截断（上游 504 根因）。subObs 为空时事件不
		// 透传，行为等价 Run。
		res, err := ag.RunStreamWithObserver(runCtx, input, subObs)
		if err != nil {
			// 失败日志增强（P4-I）：记录角色/模型/耗时，便于定位"编排某步失败"根因
			//（超时 / 限流 429 / 上游 400 / 上下文超限等，errors 往往自带上游信息）。
			s.log.Warn("orchestrate 子任务失败",
				zap.String("task_id", task.ID),
				zap.String("role", task.Role),
				zap.String("model", model),
				zap.Duration("elapsed", time.Since(start)),
				zap.Error(err),
			)
			return nil, err
		}
		return res, nil
	}

	var lastErr error
	attempt := 0
	for ; attempt <= s.orchSubtaskRetries; attempt++ {
		res, err := execute()
		if err == nil {
			return orchestrate.TaskResult{
				TaskID:  task.ID,
				Role:    task.Role,
				Status:  orchestrate.TaskCompleted,
				Content: res.Content,
				Usage:   res.Usage,
			}, nil
		}
		lastErr = err
		// 达到重试上限 / 非可重试错误 / 父 ctx 已取消 → 停止。
		if attempt >= s.orchSubtaskRetries || !isRetryableLLMError(err) || ctx.Err() != nil {
			break
		}
		// 指数退避：1s → 2s → 4s → 8s → 16s，封顶 30s（防上游拥塞惊群）。
		backoff := 30 * time.Second
		if attempt < 5 {
			backoff = time.Duration(1<<uint(attempt)) * time.Second
		}
		s.log.Warn("orchestrate 子任务将重试",
			zap.String("task_id", task.ID),
			zap.String("role", task.Role),
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", s.orchSubtaskRetries),
			zap.Duration("backoff", backoff),
			zap.Error(err),
		)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			lastErr = ctx.Err()
			break
		}
	}
	// 失败原因带分类前缀（P4-M）：让"未收敛"与"连接/上游故障"在节点流
	// 一眼可辨，不必逐字读原始报错。分类在前、原始错误在后（详情保留）。
	// attempt > 0 时标注"已重试 N 次仍失败"，便于确认重试链路是否生效
	//（反复失败且非重试类错误时，直接定位为上游持续故障而非代码问题）。
	retryNote := ""
	if attempt > 0 {
		retryNote = fmt.Sprintf("（重试 %d 次后仍失败）", attempt)
	}
	return orchestrate.TaskResult{
		TaskID: task.ID,
		Role:   task.Role,
		Status: orchestrate.TaskFailed,
		Error:  classifySubTaskError(lastErr) + retryNote + "：" + lastErr.Error(),
	}, lastErr
}

// classifySubTaskError 把编排子任务失败原因归类，返回带分类前缀的中文摘要。
//
// 设计动机（P4-M）：编排子任务失败的两大主因"生成未收敛"与"连接/上游故障"
// 处理方式完全不同（前者调提示词/轮数、后者调重试/并发/上游健康），现状靠
// 人肉读原始报错区分。此处按错误链归类为明确标签，前端节点流与聚合器
// 失败清单（clipError 截断后仍保留前缀）都能直接展示。
//
// 分类优先级：轮数耗尽/思考轮超限（业务错误）> HTTP 状态（504/429/5xx/4xx）
// > 上下文超时 > 网络错误 > 兜底。
func classifySubTaskError(err error) string {
	if err == nil {
		return "执行失败"
	}
	if errors.Is(err, agent.ErrMaxRounds) {
		return "生成未收敛（达到最大轮数）"
	}
	if errors.Is(err, agent.ErrMaxThinkingRounds) {
		return "思考（工具调用）轮次超限"
	}
	var hse *llm.HTTPStatusError
	if errors.As(err, &hse) {
		switch {
		case hse.Status == http.StatusGatewayTimeout, hse.Status == http.StatusBadGateway:
			return fmt.Sprintf("上游服务超时（HTTP %d）", hse.Status)
		case hse.Status == http.StatusTooManyRequests:
			return "上游限流（HTTP 429）"
		case hse.Status >= http.StatusInternalServerError:
			return fmt.Sprintf("上游服务故障（HTTP %d）", hse.Status)
		default:
			return fmt.Sprintf("上游请求失败（HTTP %d）", hse.Status)
		}
	}
	// 网络层错误优先于上下文超时判断：url.Error 内部常包裹
	// context.DeadlineExceeded（连接超时），应归为"网络连接失败"而非"执行超时"。
	var ue *url.Error
	if errors.As(err, &ue) {
		return "网络连接失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "执行超时"
	}
	return "执行失败"
}

// orchestrationSubtaskExcludeTools 编排子任务装配的排除工具黑名单。
//
// 历史：P4-M 曾按白名单只保留基础轻量工具（skills/MCP/code_executor 全裁），
// 动机是减小非流式大请求体、降低上游 504。P4-N 流式化（子任务统一走
// RunStream）已从根本解决 504，瘦身不再必要——故改为黑名单模式，对齐业界
// 标准（OpenAI Agents SDK / LangGraph / AutoGen 的子代理工具集设计）：
//
//   - 子代理工具集 = 主会话能力集 − 宿主专用工具 − 对子任务无效的调试工具；
//   - 用户配置的技能（skill_*）、MCP 工具（mcp_*）与 code_executor 等能力
//     应被子任务继承——它们本就是用户显式启用的能力，裁掉会破坏
//     "能力靠配置组合"的框架原则（且用户实测反馈"没有工具"）；
//   - 仅排除对子任务确实无用的工具，避免无谓的请求体与模型工具选择噪声。
//
// 排除项与理由：
//   - echo：L0 联调/教学专用（schema 描述即注明"只有测试/演示时才使用"），
//     生产链路（含编排子任务）不应注入调试工具；
//   - describe_image / read_document：消费"用户上传的媒体/文档"，而子任务
//     输入是上游文本成果，没有可解析的上传物——注入纯属占用请求体；
//   - local_shell（External=true）已由 stripExternalTools 统一裁剪；此处重复
//     登记作为双保险（本地桌面执行需要宿主确认通道，子任务无此通道）。
//
// 注：黑名单是"开放集"——新增工具默认对子任务可见，正符合能力继承语义。
var orchestrationSubtaskExcludeTools = map[string]struct{}{
	"echo":           {},
	"describe_image": {},
	"read_document":  {},
	"local_shell":    {},
}

// filterSubtaskTools 按黑名单裁剪注册表（与 stripExternalTools 叠加使用）。
// 黑名单外的工具（skills/MCP/code_executor/render_document/kb_search 等）
// 全部进入子任务请求体，实现"能力继承"；黑名单内的工具被剔除。
func filterSubtaskTools(reg *tool.Registry) *tool.Registry {
	if reg == nil {
		return nil
	}
	keep := make([]tool.Tool, 0, 16)
	for _, ts := range reg.Schemas() {
		if _, exclude := orchestrationSubtaskExcludeTools[ts.Name]; exclude {
			continue
		}
		if t, err := reg.Get(ts.Name); err == nil {
			keep = append(keep, t)
		}
	}
	out := tool.NewRegistry()
	for _, t := range keep {
		_ = out.Register(t)
	}
	return out
}

// stripExternalTools 从注册表裁剪需宿主外部执行（External=true）的工具。
// 背景：External 工具（如 local_shell）依赖宿主侧的 AsyncRunner + 桌面端
// 确认通道执行。编排子任务运行在服务端内部（runSubTask 未注入 AsyncRunner），
// 保留这些工具会让模型调用后直接报错"需外部执行，但会话未配置 AsyncRunner"。
// 业界标准：子代理工具集 = 主会话工具集 - 宿主专用（需人工确认）工具；
// 主会话仍保留（桌面端经 SSE tool_call 事件弹窗确认后本机执行）。
func stripExternalTools(reg *tool.Registry) *tool.Registry {
	if reg == nil {
		return nil
	}
	keep := make([]tool.Tool, 0, 16)
	for _, ts := range reg.Schemas() {
		if ts.External {
			continue
		}
		if t, err := reg.Get(ts.Name); err == nil {
			keep = append(keep, t)
		}
	}
	out := tool.NewRegistry()
	for _, t := range keep {
		_ = out.Register(t)
	}
	return out
}

// isRetryableLLMError 判断编排子任务失败是否值得"整任务重试"。
//
// 可重试（上游/网络瞬时故障，重跑大概率恢复）：
//   - HTTP 429 限流、5xx 上游临时故障（HTTPStatusError）；
//   - 网络层错误（连接重置/DNS/读写超时，url.Error）；
//   - context.DeadlineExceeded（子任务超时——上游瞬时拥塞恢复后可能成功）。
//
// 不重试（业务错误，重跑同样结果）：
//   - 4xx 业务错误（400 参数/上下文超限、401/403 鉴权等）；
//   - 轮数耗尽（agent.ErrMaxRounds）、空回复（agent.ErrEmptyReply）；
//   - context.Canceled（用户/父级主动取消，重试无意义）。
func isRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	var hse *llm.HTTPStatusError
	if errors.As(err, &hse) {
		return hse.Status == http.StatusTooManyRequests || hse.Status >= http.StatusInternalServerError
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

// persistOrchestrateRound 把编排结果作为一个"新轮次"落库：
// user 消息 + 编排过程摘要（system，前端历史渲染用）+ assistant 最终回答，
// round_no = 当前最大轮次 + 1，version = 0。返回 round_no（供 orchestration_runs
// 权威记录关联）。中间节点过程另见 orchestration_runs 表（P4-I）。
func (s *Service) persistOrchestrateRound(ctx context.Context, sessionID int64, res *orchestrate.RunResult, userContent, assistantContent string) (int64, error) {
	maxRound, err := s.repo.MaxRoundNo(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	roundNo := maxRound + 1
	msgs := []*Message{
		{Role: string(schema.RoleUser), Content: userContent, RoundNo: roundNo, Version: 0},
	}
	if summary := buildOrchestrationSummary(res); summary != "" {
		msgs = append(msgs, &Message{Role: string(schema.RoleSystem), Content: summary, RoundNo: roundNo, Version: 0})
	}
	msgs = append(msgs, &Message{Role: string(schema.RoleAssistant), Content: assistantContent, RoundNo: roundNo, Version: 0})
	if err := s.repo.AppendMessages(ctx, sessionID, msgs); err != nil {
		return 0, err
	}
	return roundNo, nil
}

// persistOrchestrateVersion 编排"重新生成"场景的版本落库：user 消息已在
// DB（跳过），只存过程摘要（system）+ assistant 最终回答，round_no 不变、
// version = newVer（与 Regenerate 的版本语义一致）。
func (s *Service) persistOrchestrateVersion(ctx context.Context, sessionID int64, roundNo int64, version int, res *orchestrate.RunResult, final string) error {
	msgs := make([]*Message, 0, 2)
	if summary := buildOrchestrationSummary(res); summary != "" {
		msgs = append(msgs, &Message{Role: string(schema.RoleSystem), Content: summary, RoundNo: roundNo, Version: version})
	}
	msgs = append(msgs, &Message{Role: string(schema.RoleAssistant), Content: final, RoundNo: roundNo, Version: version})
	return s.repo.AppendMessages(ctx, sessionID, msgs)
}

// orchestrationTaskContentCap 编排子任务输出入库的单任务截断上限
// （防长正文撑爆 orchestration_runs.tasks JSONB，过程冗余信息截尾可接受）。
const orchestrationTaskContentCap = 8000

// orchestrationTaskContentSuffix 截断时追加的后缀（与上游摘要截断风格一致）。
const orchestrationTaskContentSuffix = "\n…（已截断）"

// orchestrationSummaryMarker 编排过程摘要消息（system 角色）的内容前缀。
// 前缀 + 紧凑 JSON（任务 id/status/error/tokens/duration），前端据此识别
// 并渲染历史编排过程（loadHistory 过滤该角色，不进入模型上下文）。
const orchestrationSummaryMarker = "__orch_v1__"

// orchestrationErrorCap 摘要里失败任务 error 的最大字符数（防消息表膨胀）。
const orchestrationErrorCap = 500

// orchestrationOutputCap 摘要里单个子任务输出（output）的最大 runes。
// 输出会渲染进历史编排块供回看（不进模型上下文——system 角色被 loadHistory/
// regenerate 双重过滤），但消息表不宜过大：超出截尾，完整版仍存 orchestration_runs。
const orchestrationOutputCap = 2000

// clipRunes 按 rune 截断并追加省略号（中文安全：不按字节切导致乱码）。
// 与 chatdoc.go 的 truncateRunes 区分：后者后缀是"文档过长"提示，不适合编排摘要。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// buildOrchestrationSummary 把编排 RunResult 压缩为"前端可渲染"的过程摘要。
// 任务终态/错误/耗时/token + 子任务输出（output，截断版，供历史回看）。
// system 角色消息被 loadHistory / regenerate 过滤，不进入模型上下文——
// 与"思考文本/工具调用记录"同属"入库但不污染后续对话"的存储策略。
// 空任务列表返回 ""（无需存 system 摘要消息）。
func buildOrchestrationSummary(res *orchestrate.RunResult) string {
	if res == nil || len(res.Tasks) == 0 {
		return ""
	}
	type orchTaskJSON struct {
		ID         string `json:"id"`
		Role       string `json:"role,omitempty"`
		Status     string `json:"status"`
		Error      string `json:"error,omitempty"`
		Output     string `json:"output,omitempty"` // 子任务输出（截断版，前端历史渲染回看）
		Tokens     int64  `json:"tokens"`
		DurationMs int64  `json:"duration_ms"`
	}
	tasks := make([]orchTaskJSON, 0, len(res.Tasks))
	for _, t := range res.Tasks {
		tasks = append(tasks, orchTaskJSON{
			ID:         t.TaskID,
			Role:       t.Role,
			Status:     string(t.Status),
			Error:      clipRunes(t.Error, orchestrationErrorCap),
			Output:     clipRunes(t.Content, orchestrationOutputCap),
			Tokens:     int64(t.Usage.TotalTokens),
			DurationMs: int64(t.Duration * 1000),
		})
	}
	b, err := json.Marshal(map[string]any{"v": 1, "tasks": tasks})
	if err != nil {
		return ""
	}
	return orchestrationSummaryMarker + string(b)
}

// persistOrchestrationRun 把一次编排的过程输出落库（P4-I 过程入库）。
//
// 转换 orchestrate.RunResult.Tasks → []OrchestrationTask（截断 Content、
// 补 Role/Error/DurationMs/Tokens），status 全 completed 为 "completed"
// 否则 "partial"（任一子任务 failed/skipped）。roundNo 关联对话轮次（重新
// 生成场景多版本共一轮）。写失败只记日志降级，不阻塞对话主流程。
func (s *Service) persistOrchestrationRun(ctx context.Context, sess *Session, userID int64, res *orchestrate.RunResult, final string, roundNo int64) {
	status := "completed"
	tasks := make([]OrchestrationTask, 0, len(res.Tasks))
	var totalTokens int64
	for _, t := range res.Tasks {
		if t.Status != orchestrate.TaskCompleted {
			status = "partial"
		}
		content := t.Content
		if len(content) > orchestrationTaskContentCap {
			content = content[:orchestrationTaskContentCap] + orchestrationTaskContentSuffix
		}
		tasks = append(tasks, OrchestrationTask{
			TaskID:     t.TaskID,
			Role:       t.Role,
			Status:     string(t.Status),
			Output:     content,
			Error:      t.Error,
			Tokens:     int64(t.Usage.TotalTokens),
			DurationMs: int64(t.Duration * 1000),
		})
		totalTokens += int64(t.Usage.TotalTokens)
	}
	run := &OrchestrationRun{
		SessionID:   sess.ID,
		UserID:      userID,
		RoundNo:     roundNo,
		Goal:        res.Goal,
		Status:      status,
		Tasks:       tasks,
		Result:      final,
		TotalTokens: totalTokens,
	}
	if err := s.repo.SaveOrchestration(ctx, run); err != nil {
		s.log.Warn("编排过程输出入库失败",
			zap.Int64("session_id", sess.ID), zap.Error(err))
	}
}

// orchContextMaxMsgs 编排上下文注入：最多注入的历史消息条数（user/assistant）。
const orchContextMaxMsgs = 6

// orchContextMsgCap 编排上下文注入：单条历史消息截断上限（runes）。
const orchContextMsgCap = 250

// buildOrchestrateGoal 为编排构建"带上下文的目标"：当前用户需求 + 会话近期
// 历史摘要（P4-L：编排上下文注入）。解决"上下文理解偏了"——planner/子任务
// 只看当前一句话、脱离对话背景（如"按上面的资料做课件"中"上面的资料"指代）。
//
// 只取最近若干条 user/assistant（跳过 system/tool：前者含编排过程状态摘要、
// 后者多为长工具输出，对"理解背景"帮助有限且抬升 token 成本），每条截断；
// DB 读取失败时静默降级为原始需求，不阻塞编排主流程。
func (s *Service) buildOrchestrateGoal(ctx context.Context, sess *Session, content string) string {
	msgs, err := s.repo.ListMessages(ctx, sess.ID)
	if err != nil || len(msgs) == 0 {
		return content
	}
	var recent []*Message
	for i := len(msgs) - 1; i >= 0 && len(recent) < orchContextMaxMsgs; i-- {
		m := msgs[i]
		if m.Content == "" {
			continue
		}
		switch m.Role {
		case string(schema.RoleUser), string(schema.RoleAssistant):
			recent = append(recent, m)
		}
	}
	if len(recent) == 0 {
		return content
	}
	var b strings.Builder
	b.WriteString("以下是本会话近期对话背景，用于帮助你理解当前需求的上下文：\n")
	for i := len(recent) - 1; i >= 0; i-- {
		m := recent[i]
		role := "用户"
		if m.Role == string(schema.RoleAssistant) {
			role = "助手"
		}
		body := m.Content
		if r := []rune(body); len(r) > orchContextMsgCap {
			body = string(r[:orchContextMsgCap]) + "…"
		}
		b.WriteString("- ")
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(body)
		b.WriteString("\n")
	}
	b.WriteString("\n当前用户需求：\n")
	b.WriteString(content)
	return b.String()
}

// streamOrchestrate 编排模式的流式入口：执行编排并把最终回答 + 过程进度
// 经 obs 下发。落库 user/assistant 消息（轻量持久化）。
func (s *Service) streamOrchestrate(ctx context.Context, sess *Session, userID int64, content string, obs *agent.StreamObserver) (*agent.Result, error) {
	o := s.buildOrchestrator(sess, obs)
	runCtx := withUserHeaders(ctx, userID)
	if sess.AgentID != "" {
		runCtx = llm.WithHeader(runCtx, "X-Agent-Id", sess.AgentID)
	}
	// 上下文注入（P4-L）：goal 带会话近期背景（planner/子任务/聚合共享），
	// 解决"上下文理解偏了"；落库仍用原始 content（用户原话）。
	goal := s.buildOrchestrateGoal(ctx, sess, content)
	// 聚合阶段流式（P4-K）：最终回答逐增量经 obs.OnContent 下发（打字机效果），
	// 同时累计完整文本供落库/docgen 联动。中间子任务仍非流式（节点状态流已够）。
	res, err := o.RunStream(runCtx, goal, func(delta string) {
		if obs != nil && obs.OnContent != nil {
			obs.OnContent(delta)
		}
	})
	if err != nil {
		s.log.Warn("orchestrate 执行失败",
			zap.Int64("session_id", sess.ID), zap.Error(err))
		return nil, err
	}
	// 动态规划判定无需编排（ErrNoPlan，简单问题/闲聊）：回退单 Agent 直接
	// 应答（P4-N）。复用会话工具集（含 render_document 等资源工具），
	// 让"工具可见性"这类问题得到真实回答，而不是强制编排后报错。
	if len(res.Tasks) == 0 {
		s.log.Info("orchestrate 判定无需编排，回退直接应答",
			zap.Int64("session_id", sess.ID), zap.String("goal", goal))
		result, newMsgs, err := s.runDirectAgent(ctx, sess, userID, content, obs)
		if err != nil {
			return nil, err
		}
		s.autoRenameIfDefault(ctx, sess, newMsgs)
		return result, nil
	}
	// 编排联动（P4-D）：目标明确要求生成文档时，自动产出 Word/PPT 并追加下载区块。
	// 受 officedoc 能力开关控制（P4-I）：会话显式能力白名单须含 officedoc。
	// 下载区块不是 LLM 流式产物，聚合流结束后一次性追加下发。
	var appendix string
	if s.docGenEnabled(sess) {
		appendix = s.autoRenderDocument(runCtx, userID, content, res.Final)
	}
	final := res.Final + appendix
	// 过程摘要（system）+ user/assistant 落库（P4-I：历史渲染编排过程）。
	// roundNo 同时用于权威记录 orchestration_runs 关联。
	roundNo, err := s.persistOrchestrateRound(ctx, sess.ID, res, content, final)
	if err != nil {
		return nil, err
	}
	// 过程输出入库（P4-I）：含子任务终态/Error/耗时/token，供复盘。
	s.persistOrchestrationRun(runCtx, sess, userID, res, final, roundNo)
	// 文档下载区块（docgen 联动产物）在聚合流结束后一次性追加下发。
	if appendix != "" && obs != nil && obs.OnContent != nil {
		obs.OnContent(appendix)
	}
	s.autoRenameIfDefault(ctx, sess, []*Message{
		{Role: string(schema.RoleUser), Content: content},
		{Role: string(schema.RoleAssistant), Content: final},
	})
	s.log.Info("orchestrate completed",
		zap.Int64("session_id", sess.ID),
		zap.Int("tasks", len(res.Tasks)),
		zap.Int("total_tokens", res.TotalUsage.TotalTokens),
	)
	return &agent.Result{
		Content:   final,
		Usage:     orchestrateUsage(res.TotalUsage),
		Rounds:    1,
		ToolCalls: 0,
	}, nil
}

// chatOrchestrate 编排模式的非流式入口（与 streamOrchestrate 同链路，无 obs 进度）。
func (s *Service) chatOrchestrate(ctx context.Context, sess *Session, userID int64, content string) (*ChatResult, error) {
	o := s.buildOrchestrator(sess, nil)
	runCtx := withUserHeaders(ctx, userID)
	if sess.AgentID != "" {
		runCtx = llm.WithHeader(runCtx, "X-Agent-Id", sess.AgentID)
	}
	// 上下文注入（P4-L）：goal 带会话近期背景（解决"上下文理解偏了"）；
	// 落库仍用原始 content（用户原话）。
	goal := s.buildOrchestrateGoal(ctx, sess, content)
	res, err := o.Run(runCtx, goal)
	if err != nil {
		return nil, err
	}
	// 无需编排：回退单 Agent 直接应答（与 stream 侧一致，P4-N）。
	if len(res.Tasks) == 0 {
		s.log.Info("orchestrate 判定无需编排，回退直接应答",
			zap.Int64("session_id", sess.ID), zap.String("goal", goal))
		result, newMsgs, err := s.runDirectAgent(ctx, sess, userID, content, nil)
		if err != nil {
			return nil, err
		}
		s.autoRenameIfDefault(ctx, sess, newMsgs)
		last := newMsgs[len(newMsgs)-1].ToSchema()
		return &ChatResult{
			Message:   &last,
			Rounds:    result.Rounds,
			ToolCalls: result.ToolCalls,
			Usage:     result.Usage,
		}, nil
	}
	final := res.Final
	if s.docGenEnabled(sess) {
		if appendix := s.autoRenderDocument(runCtx, userID, content, final); appendix != "" {
			final += appendix
		}
	}
	// 过程摘要（system）+ user/assistant 落库（P4-I：历史渲染编排过程）。
	roundNo, err := s.persistOrchestrateRound(ctx, sess.ID, res, content, final)
	if err != nil {
		return nil, err
	}
	// 过程输出入库（P4-I）：含子任务终态/Error/耗时/token，供复盘。
	s.persistOrchestrationRun(runCtx, sess, userID, res, final, roundNo)
	s.autoRenameIfDefault(ctx, sess, []*Message{
		{Role: string(schema.RoleUser), Content: content},
		{Role: string(schema.RoleAssistant), Content: final},
	})
	last := schema.Message{Role: schema.RoleAssistant, Content: final}
	return &ChatResult{
		Message:   &last,
		Rounds:    1,
		ToolCalls: 0,
		Usage:     orchestrateUsage(res.TotalUsage),
	}, nil
}

// runDirectAgent 单 Agent 直接应答（编排回退用，P4-N）。
// 与会话级对话同链路：DB 历史恢复 + 会话工具集（含 render_document 等
// 资源工具）+ 记忆窗口 + 同锁下运行。obs 非空走流式（RunStreamWithObserver），
// 否则非流式 Run。成功返回 agent.Result + 本轮新增消息（供自动命名）。
// 失败时与 Chat/StreamChat 一致：部分消息先落库再报错。
func (s *Service) runDirectAgent(ctx context.Context, sess *Session, userID int64, content string, obs *agent.StreamObserver) (*agent.Result, []*Message, error) {
	ag, err := s.newAgentWithHistory(ctx, sess.ID)
	if err != nil {
		return nil, nil, err
	}
	before := ag.History()
	runCtx0 := runCtx(withUserHeaders(ctx, userID), sess.Config)
	if sess.AgentID != "" {
		runCtx0 = llm.WithHeader(runCtx0, "X-Agent-Id", sess.AgentID)
	}
	var result *agent.Result
	if obs != nil {
		result, err = ag.RunStreamWithObserver(runCtx0, content, obs)
	} else {
		result, err = ag.Run(runCtx0, content)
	}
	if err != nil {
		if perr := s.persistPartialOnError(ctx, sess.ID, before, ag, err); perr != nil {
			s.log.Warn("编排回退直接应答失败且部分落库失败",
				zap.Int64("session_id", sess.ID), zap.Error(perr))
		}
		return nil, nil, mapEmptyReplyError(err, before, ag)
	}
	newMsgs, err := s.persistRound(ctx, sess.ID, before, ag)
	if err != nil {
		return nil, nil, err
	}
	return result, newMsgs, nil
}

// orchestrateUsage 把编排累计用量转为 agent.Result 的 Usage（供返回与日志）。
func orchestrateUsage(u llm.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// streamRegenerateOrchestrate 编排模式的流式重新生成（P4-I 重新生成补流式）。
//
// 语义与 streamOrchestrate 一致（重跑 DAG、进度经 obs 下发、docgen 联动），
// 但落库按版本语义：user 消息已在 DB（跳过），只写过程摘要（system）+
// assistant 最终回答（round_no 不变、version = newVer）。
func (s *Service) streamRegenerateOrchestrate(ctx context.Context, sess *Session, userID int64, runCtx context.Context, userContent string, roundNo int64, newVer int, obs *agent.StreamObserver) (*agent.Result, int, error) {
	o := s.buildOrchestrator(sess, obs)
	// 上下文注入（P4-L）：goal 带会话近期背景（解决"上下文理解偏了"）。
	goal := s.buildOrchestrateGoal(ctx, sess, userContent)
	// 聚合阶段流式（P4-K）：最终回答逐增量下发（打字机效果），同时累计完整文本。
	res, err := o.RunStream(runCtx, goal, func(delta string) {
		if obs != nil && obs.OnContent != nil {
			obs.OnContent(delta)
		}
	})
	if err != nil {
		s.log.Warn("orchestrate regenerate 执行失败",
			zap.Int64("session_id", sess.ID), zap.Error(err))
		return nil, 0, err
	}
	// 动态重生成时规划判定无需编排：版本链语义（round_no/version）与"直接
	// 应答"冲突，不静默写空 assistant——显式报错，提示重新提问。
	if len(res.Tasks) == 0 {
		return nil, 0, fmt.Errorf("orchestrate: 重新生成时规划判定无需编排，请直接重新提问")
	}
	final := res.Final
	if s.docGenEnabled(sess) {
		if appendix := s.autoRenderDocument(runCtx, userID, userContent, final); appendix != "" {
			final += appendix
		}
	}
	if err := s.persistOrchestrateVersion(ctx, sess.ID, roundNo, newVer, res, final); err != nil {
		return nil, 0, err
	}
	// 权威过程记录入库（与正常编排一致；round_no 关联本轮）。
	s.persistOrchestrationRun(runCtx, sess, userID, res, final, roundNo)
	s.autoRenameIfDefault(ctx, sess, []*Message{
		{Role: string(schema.RoleUser), Content: userContent},
		{Role: string(schema.RoleAssistant), Content: final},
	})
	return &agent.Result{
		Content:   final,
		Usage:     orchestrateUsage(res.TotalUsage),
		Rounds:    1,
		ToolCalls: 0,
	}, newVer, nil
}
