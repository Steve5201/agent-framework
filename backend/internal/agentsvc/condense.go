package agentsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/memory"
	"github.com/Steve5201/agent-framework/schema"
)

// condenseSystemPrompt 摘要压缩的系统指令：约束输出为简短中文摘要。
// 目标是把"早期对话"压缩到模型仍能感知核心事实、但不占太多 token。
const condenseSystemPrompt = `你是对话历史压缩器。把给定的对话历史压缩成 200 字以内的中文摘要，保留：用户的核心诉求、已确认的事实、尚未解决的问题、关键结论与偏好。忽略：思考过程、工具调用参数细节、寒暄与重复内容。直接输出摘要正文，禁止任何前缀、引用标记或解释性文字。`

// summarizeMessagesCap 单次压缩输入的消息条数上限（防摘要请求体过大）。
// 与前端窗口上限（200）对齐：批量压缩单次最多压约 2/3 窗口（≈133 条），
// 该上限保证全部进入摘要，不因截断丢信息。
const summarizeMessagesCap = 200

// summarizeMsgRuneCap 单条消息参与摘要的正文长度上限（rune 计数，截长留首）。
const summarizeMsgRuneCap = 200

// condenseTimeout 摘要压缩调用的独立超时：压缩是辅助链路，绝不让它拖垮
// 会话主循环。超时返回错误，framework 会退化为普通裁剪（直接丢弃）。
const condenseTimeout = 30 * time.Second

// condensationMarker 上下文压缩记录消息（system 角色）的内容前缀。
// persistRound 在压缩发生时落库一条提示消息，前端 fromHistory 据此解析为
// 用户可见的"已压缩上下文"提示条（像 Trae 一样），历史回看时仍能定位
// "哪个节点压缩过"。system 角色天然被 loadHistory 过滤，不占模型上下文。
const condensationMarker = "__condense_v1__"

// buildCondensationMessage 把一次压缩记录转为可落库的 system 提示消息。
// 内容 = marker + JSON {dropped, count}：dropped 为本次压缩丢弃的消息条数，
// count 为会话累计压缩次数。前端据文案展示；不携带摘要全文（摘要只服务
// 模型，loadHistory 会过滤，无需冗余落库）。
func buildCondensationMessage(roundNo int64, info memory.CondenseInfo) *Message {
	b, _ := json.Marshal(map[string]int{"dropped": info.Dropped, "count": info.Count})
	return &Message{
		Role:    string(schema.RoleSystem),
		Content: condensationMarker + string(b),
		RoundNo: roundNo,
		Version: 0,
	}
}

// makeCondenser 构造上下文压缩回调（注入 framework agent.WithMemoryCondenser）。
//
// 背景：agent 滑动窗口超限时默认"直接丢弃最旧消息"（memory.ShortTermMemory
// 的 TODO，现由 framework CondensingMemory 支持摘要压缩）。本函数让窗口溢出
// 时用 LLM 把旧消息压成一条 ≤200 字摘要，模型仍能感知早期对话梗概。
//
// 成本提醒：每轮窗口溢出触发一次小型 LLM 调用（会话同款模型、非流式），
// 频率 ≈ 每 MaxMessages 条消息一次；失败（上游不可用/超时）自动降级为普通
// 裁剪，不阻塞对话。
func (s *Service) makeCondenser(model string) memory.CondenseFunc {
	return func(ctx context.Context, dropped []schema.Message) (string, error) {
		text := serializeForSummary(dropped, summarizeMessagesCap)
		if text == "" {
			return "", nil // 无可压缩内容（如全是空正文消息）
		}
		// 独立超时：压缩失败不能拖垮主对话
		ctx, cancel := context.WithTimeout(ctx, condenseTimeout)
		defer cancel()
		resp, err := s.provider.Chat(ctx, &llm.Request{
			Model: model,
			Messages: []schema.Message{
				{Role: schema.RoleSystem, Content: condenseSystemPrompt},
				{Role: schema.RoleUser, Content: text},
			},
			// 摘要压缩是简单改写任务：关闭思考模式，省 token、提速（成本优化）。
			Thinking: &llm.ThinkingConfig{Enabled: false},
		})
		if err != nil {
			return "", fmt.Errorf("上下文压缩失败: %w", err)
		}
		out := strings.TrimSpace(resp.Content)
		if out == "" {
			return "", fmt.Errorf("上下文压缩返回空摘要")
		}
		return out, nil
	}
}

// serializeForSummary 把待压缩消息序列化成压缩器的输入文本。
// 只保留语义信息（角色 + 正文），丢弃思考内容与工具调用细节（那正是要
// 被压缩掉的冗余），并按条数与单条长度截断，保证摘要请求体可控。
func serializeForSummary(msgs []schema.Message, maxCount int) string {
	var b strings.Builder
	count := 0
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue // 无正文（如思考轮/工具声明轮）不贡献语义
		}
		if count >= maxCount {
			b.WriteString("\n…（其余历史从略）\n")
			break
		}
		count++
		label := map[schema.Role]string{
			schema.RoleUser:      "用户",
			schema.RoleAssistant: "助手",
			schema.RoleTool:      "工具结果",
			schema.RoleSystem:    "摘要",
		}[m.Role]
		if label == "" {
			label = string(m.Role)
		}
		b.WriteString("[" + label + "] " + clipRunes(content, summarizeMsgRuneCap) + "\n")
	}
	return b.String()
}
