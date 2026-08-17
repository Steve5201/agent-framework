package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

// ErrEmptyReply 模型最终回答为空（无内容且未调用工具）时返回的哨兵错误。
//
// 用途：空回复若被当作正常回答落库会污染会话历史（之后模型持续返回空，
// 会话"卡死"）。Run / RunStream 发现空最终回答即返回本错误，调用方
// （agentsvc）负责不落库，并按"是否已执行工具"给出面向用户的明确提示。
var ErrEmptyReply = errors.New("agent: 模型返回了空回复")

// ErrMaxRounds 达到消息循环最大轮数仍未见最终回答（工具循环不收敛）。
//
// 与 ErrEmptyReply 不同：触发前对话历史中已有合法消息（assistant+tool 成对），
// 调用方可先按历史差分持久化"部分轮次"，再向用户报告限制——避免"整轮白干、
// 用户看到上一步对话凭空消失"。
var ErrMaxRounds = errors.New("agent: 达到最大轮数，对话未收敛")

// ErrMaxThinkingRounds 思考（工具调用）轮次达到上限仍继续调工具。
// 语义与 ErrMaxRounds 一致：历史中存在合法部分轮次，可持久化后报告。
var ErrMaxThinkingRounds = errors.New("agent: 思考（工具调用）轮次已达上限")

// Run 处理一条用户消息，自动完成"调 LLM → 执行工具 → 再调 LLM"循环，
// 直到模型给出最终回答（无工具调用）或达到 MaxRounds。
//
// 这是非流式入口：等待完整回答后返回。
func (s *Session) Run(ctx context.Context, userInput string) (*Result, error) {
	// 1. 用户消息进入记忆
	s.mem.Add(schema.Message{Role: schema.RoleUser, Content: userInput})
	// 上下文压缩：历史灌入若造成超窗，先压成摘要再进首轮请求（模型能感知早期对话梗概）。
	s.condenseContext(ctx)

	rounds, toolCalls, thinkingRounds := 0, 0, 0
	var finalContent string
	var usage llm.Usage

	for {
		// 轮数保护：防止"无限调工具"死循环
		if rounds >= s.config.MaxRounds {
			return nil, fmt.Errorf("%w: 最大 %d 轮", ErrMaxRounds, s.config.MaxRounds)
		}
		rounds++

		// 2. 调 LLM（携带 system + 历史 + 工具说明书）
		resp, err := s.provider.Chat(ctx, s.buildRequest(false))
		if err != nil {
			return nil, fmt.Errorf("agent: LLM 调用失败: %w", err)
		}
		usage = resp.Usage

		// 3. 记录 assistant 回复
		// 协议要求：assistant 消息若带工具调用指令，必须原样存入历史，
		// 与后续 role=tool 结果消息成对出现，否则模型会拒绝继续推理。
		// 思考内容（resp.Reasoning）一并保存：工具轮后续请求必须回传，
		// 且前端需要它渲染"思考过程"气泡。
		assistantMsg := schema.Message{Role: schema.RoleAssistant, Content: resp.Content, Reasoning: resp.Reasoning}
		if len(resp.ToolCalls) > 0 {
			assistantMsg.ToolCalls = resp.ToolCalls
		}
		s.mem.Add(assistantMsg)

		// 4. 无工具调用 → 模型已给出最终回答
		if len(resp.ToolCalls) == 0 {
			finalContent = resp.Content
			break
		}

		// 5. 思考轮次护栏：本轮调用了工具，计入思考轮；超限即停（不再执行工具，
		//    节省 token）。assistant 消息已入历史，调用方可持久化部分轮次。
		thinkingRounds++
		if s.config.MaxThinkingRounds > 0 && thinkingRounds > s.config.MaxThinkingRounds {
			return nil, fmt.Errorf("%w: 工具调用 %d 轮", ErrMaxThinkingRounds, thinkingRounds)
		}

		// 6. 逐个执行工具，结果回填记忆（作为 role=tool 消息）
		for _, call := range resp.ToolCalls {
			toolCalls++
			start := time.Now()
			result, err := s.execTool(ctx, call)
			duration := time.Since(start)
			// 工具审计（与流式路径 stream.go 一致）：调用是否成功都记录，
			// 供上层按会话/用户统计工具使用量（非流式编排子任务同样落库）。
			if s.toolAudit != nil {
				s.toolAudit(call, result, err, duration)
			}
			if err != nil {
				// 权限/校验失败：把错误回填，让模型知道并调整策略
				s.mem.Add(schema.Message{
					Role:       schema.RoleTool,
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("工具调用未执行: %v", err),
				})
				continue
			}
			s.mem.Add(schema.Message{
				Role:       schema.RoleTool,
				ToolCallID: call.ID,
				Content:    result.Content,
			})
		}
		// 7. 进入下一轮：模型将基于工具结果继续推理
		// 本轮新增了多轮工具消息，可能再次超窗——压缩后进下一轮，保证窗口有界。
		s.condenseContext(ctx)
	}

	// 防"空回复污染历史"：模型最终回答必须非空。
	// 若空内容（如只输出思考、上游异常）被当作正常回答落库，会污染会话
	// 历史——之后每次对话模型都会看到一条空 assistant 消息，持续返回空
	//（会话"卡死"，见 agentsvc loadHistory 的健康过滤）。空回答视为错误，
	// 不落库，调用方重试即可。
	if finalContent == "" {
		return nil, ErrEmptyReply
	}

	return &Result{Content: finalContent, Usage: usage, Rounds: rounds, ToolCalls: toolCalls}, nil
}

// execTool 执行一次工具调用。
// 外部代理工具（ToolSchema.External==true）不直接执行，派发给
// AsyncRunner 异步执行并挂起等待回填（见 execExternal）；
// 其余工具按"用户确认 → 执行"的同步路径处理（带用户确认回调）。
func (s *Session) execTool(ctx context.Context, call schema.ToolCall) (*schema.ToolResult, error) {
	ts, err := s.registry.SchemaByName(call.Name)
	if err != nil {
		return nil, err
	}
	if ts.External {
		return s.execExternal(ctx, call)
	}

	approved := false
	if s.approval != nil {
		approved = s.approval(call)
	}
	return s.registry.Execute(ctx, call, approved)
}

// sanitizeMessages 防御性清理：移除"无主 tool"消息。
//
// 背景：记忆窗口裁剪正常情况下由 memory.ShortTermMemory.Trim 的配对保护
// 保证不切开 assistant(tool_calls)↔tool 配对（线上 400 bug 根因已修复）。
// 但历史消息若来自外部注入（如宿主 loadHistory）、或未来其他路径出现
// 孤立 tool，OpenAI 兼容协议会直接拒绝整个请求（HTTP 400：Messages with
// role 'tool' must be a response to a preceding message with 'tool_calls'），
// 导致会话整轮失败。此处兜底过滤，保证发给模型的请求永远合法。
//
// 注意：反向情况（assistant 声明 tool_calls 但结果缺失）刻意保留——
// 模型声明工具调用后未收到结果仍可继续推理，且不违反本协议约束。
func sanitizeMessages(msgs []schema.Message) []schema.Message {
	declared := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == schema.RoleAssistant {
			for _, tc := range m.ToolCalls {
				declared[tc.ID] = true
			}
		}
	}
	out := make([]schema.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == schema.RoleTool && !declared[m.ToolCallID] {
			continue // 无主 tool：直接丢弃
		}
		out = append(out, m)
	}
	return out
}

// buildRequest 组装发给 LLM 的请求：system 指令 + 窗口内历史 + 工具说明书。
// system 每次动态拼在最前，不占用记忆窗口。
func (s *Session) buildRequest(stream bool) *llm.Request {
	msgs := sanitizeMessages(s.mem.Recent())
	if s.config.SystemPrompt != "" {
		msgs = append([]schema.Message{{Role: schema.RoleSystem, Content: s.config.SystemPrompt}}, msgs...)
	}
	req := &llm.Request{
		Model:    s.config.Model,
		Messages: msgs,
		Tools:    s.registry.Schemas(),
		Stream:   stream,
	}
	// 思考模式配置：非 nil 才透传（nil = 未配置，沿用厂商默认思考开启）。
	// 注意：enabled=false 也必须下发（thinking.type=disabled），
	// 否则省略该字段会被厂商当作默认 enabled，模型仍会思考。
	if s.config.Thinking != nil {
		req.Thinking = &llm.ThinkingConfig{
			Enabled:         s.config.Thinking.Enabled,
			ReasoningEffort: s.config.Thinking.ReasoningEffort,
		}
	}
	return req
}
