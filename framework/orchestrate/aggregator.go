package orchestrate

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

// Aggregator 结果合并器：把各子任务成果精炼为对用户目标的完整回答。
// 这是"合并"环节：模型综合各节点输出，去重、补全、形成连贯结论。
type Aggregator struct {
	provider llm.Provider
	model    string
}

// NewAggregator 创建合并器。
func NewAggregator(p llm.Provider, model string) *Aggregator {
	return &Aggregator{provider: p, model: model}
}

// aggSystemPrompt 合并器系统提示词：把各子任务成果整合为完整连贯的回答。
func aggSystemPrompt() string {
	return "你是结果汇总者（Aggregator）。把各子任务的成果整合为一份完整、连贯、面向最终用户的回答。" +
		"要求：逻辑清晰、去除冗余与重复、保留关键信息；用中文回答；不要提及子任务分解过程。" +
		"注意：若部分子任务未完成（输入中标明「未完成」及原因），基于已完成成果尽力作答，" +
		"缺失部分如实说明当前无法覆盖，严禁编造或臆测。"
}

// clipError 截断子任务失败原因（防超长错误信息撑爆合并 prompt；详情仍存
// orchestration_runs 表 / 前端失败详情）。按 rune 截断，保留语义完整性。
func clipError(s string) string {
	r := []rune(s)
	if len(r) <= 100 {
		return s
	}
	return string(r[:100]) + "…"
}

// buildAggregatePrompt 构造合并器用户输入：用户目标 + 各子任务成果。
// 已完成任务拼完整成果；失败/跳过任务拼"未完成"清单（含截断原因），
// 供聚合器"尽力而为"输出并如实向用户说明缺失部分——部分失败不再导致
// 整体报错（P4-L：部分失败透明合并）。
func buildAggregatePrompt(goal string, results []TaskResult) string {
	var b strings.Builder
	b.WriteString("用户目标：")
	b.WriteString(goal)
	b.WriteString("\n\n各子任务成果：\n")
	hasCompleted := false
	for _, r := range results {
		switch r.Status {
		case TaskCompleted:
			hasCompleted = true
			b.WriteString("【")
			b.WriteString(r.TaskID)
			b.WriteString("】")
			if r.Content != "" {
				b.WriteString(r.Content)
			} else {
				b.WriteString("（无输出）")
			}
			b.WriteString("\n\n")
		case TaskFailed, TaskSkipped:
			b.WriteString("【")
			b.WriteString(r.TaskID)
			b.WriteString("】（未完成")
			if r.Error != "" {
				b.WriteString("，原因：")
				b.WriteString(clipError(r.Error))
			}
			b.WriteString("）\n\n")
		}
	}
	// 无任何已完成成果时，聚合器基于失败清单与用户目标尽力作答并如实说明现状，
	// 而非整体报错（避免"前面成功、最后一步失败"整链报废、前面 token 白烧）。
	if !hasCompleted {
		b.WriteString("（无已完成子任务成果，请基于以上失败原因与用户目标尽力作答，并如实说明现状。）\n\n")
	}
	return b.String()
}

// Aggregate 汇总所有子任务输出，生成最终回答（非流式）。
// 部分任务失败/跳过时"尽力而为"合并：基于已完成成果输出并如实说明缺失。
func (a *Aggregator) Aggregate(ctx context.Context, goal string, results []TaskResult) (string, error) {
	req := &llm.Request{
		Model: a.model,
		Messages: []schema.Message{
			{Role: schema.RoleSystem, Content: aggSystemPrompt()},
			{Role: schema.RoleUser, Content: buildAggregatePrompt(goal, results)},
		},
	}
	resp, err := a.provider.Chat(ctx, req)
	if err != nil {
		return "", fmt.Errorf("orchestrate: 结果合并失败: %w", err)
	}
	return resp.Content, nil
}

// AggregateStream 流式合并：与 Aggregate 同语义，但最终回答经 ChatStream 逐
// 增量回调 onDelta（打字机效果，供 SSE 下发），同时返回完整文本供落库。
// 调用方必须确保流被完整消费；失败时返回错误（已消费的增量不回收）。
func (a *Aggregator) AggregateStream(ctx context.Context, goal string, results []TaskResult, onDelta func(string)) (string, error) {
	req := &llm.Request{
		Model: a.model,
		Messages: []schema.Message{
			{Role: schema.RoleSystem, Content: aggSystemPrompt()},
			{Role: schema.RoleUser, Content: buildAggregatePrompt(goal, results)},
		},
	}
	stream, err := a.provider.ChatStream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("orchestrate: 结果合并失败: %w", err)
	}
	defer stream.Close()

	var full strings.Builder
	for {
		ev, err := stream.Next()
		if err == io.EOF || ev.Done {
			break
		}
		if err != nil {
			return "", fmt.Errorf("orchestrate: 结果合并失败: %w", err)
		}
		if ev.Content == "" {
			continue
		}
		full.WriteString(ev.Content)
		if onDelta != nil {
			onDelta(ev.Content)
		}
	}
	return full.String(), nil
}
