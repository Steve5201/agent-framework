package agent

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

// StreamObserver 流式对话过程观察者：除了最终回答的文本增量，还接收
// 思考内容与工具执行过程事件，供上层实时渲染"思考过程"（前端气泡）。
//
// 事件顺序模拟模型真实的"想→做→想"循环：
//
//	OnReasoning（思考）→ OnToolCall（决定调用工具）→
//	OnToolResult（工具返回）→ OnReasoning（继续思考）→ OnContent（最终回答）
//
// 任一回调为 nil 表示不关心该事件。工具执行阶段不会产生 OnContent
// 增量（工具结果回填后模型的下一条文本仍会通过 OnContent 输出）。
type StreamObserver struct {
	// OnReasoning 思考内容增量（DeepSeek reasoning_content）。
	OnReasoning func(delta string)
	// OnContent 回答文本增量（打字机效果）。
	OnContent func(delta string)
	// OnToolCall 一次工具调用开始（参数已按 index 拼装完整）。
	OnToolCall func(call schema.ToolCall)
	// OnToolResult 一次工具调用的执行结果；execErr 非空表示执行失败
	//（失败的结果同样回填给模型，让它调整策略）。
	OnToolResult func(call schema.ToolCall, result *schema.ToolResult, execErr error)
	// OnTaskStatus 多智能体编排进度事件（仅 mode=orchestrate 的会话触发）。
	// 单智能体模式下恒为 nil。事件语义见 TaskStatusEvent。
	OnTaskStatus func(ev TaskStatusEvent)
}

// TaskStatusEvent 编排子任务进度事件（轻量结构，避免 agent 包依赖 orchestrate）。
// 由 agent-service 把 orchestrate.ProgressEvent 转成此结构后经 OnTaskStatus 下发。
type TaskStatusEvent struct {
	Type        string // task_started | task_content | task_finished | run_completed | run_failed
	TaskID      string // 子任务 ID（task_* 时非空）
	Status      string // running | completed | failed | skipped（task_finished 时）
	Error       string // 失败原因（run_failed / failed 时）
	TotalTokens int64  // 该子任务累计 token（task_finished 时）
	// Content 子任务输出增量（type=task_content 时下发，前端累积渲染打字机）。
	// Kind 区分增量内容：text（正文）/ reasoning（思考）/ tool_start（开始调工具）/
	// tool_end（工具执行结束）；tool_* 时 Content 为工具名。
	Content string
	Kind    string // text | reasoning | tool_start | tool_end（task_content 时）
}

// RunStream 流式处理用户消息：contentFn 接收每个文本增量（供前端打字机效果）。
// 流式同样完整支持工具调用：模型把工具参数分片下发（ToolCallDelta），
// 本方法按 index 拼装成完整参数后再执行，并继续循环。
//
// 等价于 RunStreamWithObserver(ctx, userInput, &StreamObserver{OnContent: contentFn})。
func (s *Session) RunStream(ctx context.Context, userInput string, contentFn func(string)) (*Result, error) {
	var obs *StreamObserver
	if contentFn != nil {
		obs = &StreamObserver{OnContent: contentFn}
	}
	return s.RunStreamWithObserver(ctx, userInput, obs)
}

// RunStreamWithObserver 流式处理用户消息，并把思考/工具过程事件实时
// 通知给 obs（可空）。实现与 RunStream 完全一致，仅多出事件分发。
func (s *Session) RunStreamWithObserver(ctx context.Context, userInput string, obs *StreamObserver) (*Result, error) {
	s.mem.Add(schema.Message{Role: schema.RoleUser, Content: userInput})
	// 上下文压缩：历史灌入若造成超窗，先压成摘要再进首轮请求（模型能感知早期对话梗概）。
	s.condenseContext(ctx)

	rounds, toolCalls, thinkingRounds := 0, 0, 0
	var finalContent string
	var usage llm.Usage

	for {
		if rounds >= s.config.MaxRounds {
			return nil, fmt.Errorf("%w: 最大 %d 轮", ErrMaxRounds, s.config.MaxRounds)
		}
		rounds++

		// 发起流式请求
		st, err := s.provider.ChatStream(ctx, s.buildRequest(true))
		if err != nil {
			return nil, fmt.Errorf("agent: 流式请求失败: %w", err)
		}

		// 拼装流式结果：文本累积 + 思考累积 + 工具调用增量按 index 归并
		var text strings.Builder
		var reasoning strings.Builder
		toolParts := map[int]*schema.ToolCall{}

		for {
			ev, err := st.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = st.Close()
				// 流中断（超时/网络/用户打断）：把已拼装的部分内容先提交到短期记忆，
				// 供上层 persistPartialOnError 差分落库——用户已在界面上看到的内容
				// 不能随连接消失（P4-L 修复：打断后"已生成内容不入库"）。
				// 只提交已产生的文本（附思考）；未拼装完整的工具调用丢弃，
				// 避免不完整参数被当成真实工具调用重放。
				if text.Len() > 0 {
					s.mem.Add(schema.Message{
						Role:      schema.RoleAssistant,
						Content:   text.String(),
						Reasoning: reasoning.String(),
					})
				}
				return nil, fmt.Errorf("agent: 流读取失败: %w", err)
			}

			if ev.Content != "" {
				text.WriteString(ev.Content)
				if obs != nil && obs.OnContent != nil {
					obs.OnContent(ev.Content)
				}
			}
			if ev.Reasoning != "" {
				reasoning.WriteString(ev.Reasoning)
				if obs != nil && obs.OnReasoning != nil {
					obs.OnReasoning(ev.Reasoning)
				}
			}
			if ev.Usage != nil {
				usage = *ev.Usage
			}
			// 工具调用增量拼装（参数是分片的，需要按 index 拼接）
			for _, d := range ev.ToolCalls {
				tc, ok := toolParts[d.Index]
				if !ok {
					tc = &schema.ToolCall{ID: d.ID, Name: d.Name}
					toolParts[d.Index] = tc
				}
				if tc.ID == "" {
					tc.ID = d.ID
				}
				if tc.Name == "" {
					tc.Name = d.Name
				}
				tc.Arguments = append(tc.Arguments, []byte(d.Arguments)...)
			}
		}
		_ = st.Close()

		// 记录 assistant 消息（含拼装完成的工具调用指令与思考内容）
		assistantMsg := schema.Message{
			Role:      schema.RoleAssistant,
			Content:   text.String(),
			Reasoning: reasoning.String(),
		}
		for _, tc := range toolParts {
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, *tc)
		}
		s.mem.Add(assistantMsg)

		// 无工具调用 → 结束
		if len(toolParts) == 0 {
			finalContent = text.String()
			break
		}

		// 思考轮次护栏：本轮调用了工具，计入思考轮；超限即停（不执行工具）。
		thinkingRounds++
		if s.config.MaxThinkingRounds > 0 && thinkingRounds > s.config.MaxThinkingRounds {
			return nil, fmt.Errorf("%w: 工具调用 %d 轮", ErrMaxThinkingRounds, thinkingRounds)
		}

		// 按 index 排序后逐个执行（保证与模型声明的顺序一致）
		indices := make([]int, 0, len(toolParts))
		for i := range toolParts {
			indices = append(indices, i)
		}
		sort.Ints(indices)

		for _, i := range indices {
			call := *toolParts[i]
			toolCalls++
			if obs != nil && obs.OnToolCall != nil {
				obs.OnToolCall(call)
			}
			start := time.Now()
			result, err := s.execTool(ctx, call)
			duration := time.Since(start)
			if s.toolAudit != nil {
				s.toolAudit(call, result, err, duration)
			}
			if err != nil {
				if obs != nil && obs.OnToolResult != nil {
					obs.OnToolResult(call, nil, err)
				}
				s.mem.Add(schema.Message{
					Role:       schema.RoleTool,
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("工具调用未执行: %v", err),
				})
				continue
			}
			if obs != nil && obs.OnToolResult != nil {
				obs.OnToolResult(call, result, nil)
			}
			s.mem.Add(schema.Message{
				Role:       schema.RoleTool,
				ToolCallID: call.ID,
				Content:    result.Content,
			})
		}
		// 下一轮：模型基于工具结果继续流式输出
		// 本轮新增了多轮工具消息，可能再次超窗——压缩后进下一轮，保证窗口有界。
		s.condenseContext(ctx)
	}

	// 防"空回复污染历史"：与 Run 一致，空最终回答视为错误（不落库）。
	// 注意：即使本轮执行过工具，只要模型未对工具结果生成正文总结，
	// 仍视为异常——由调用方（agentsvc）按历史差分给出上下文化提示。
	if finalContent == "" {
		return nil, ErrEmptyReply
	}

	return &Result{Content: finalContent, Usage: usage, Rounds: rounds, ToolCalls: toolCalls}, nil
}
