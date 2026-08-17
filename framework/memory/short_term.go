package memory

import (
	"fmt"

	"github.com/Steve5201/agent-framework/schema"
)

// ShortTermMemory 会话内短期记忆：滑动窗口实现。
//
// 窗口策略：
//   - 最多保留 maxMessages 条消息；
//   - 开头的 protected 条消息（如 system 系统指令）永不裁剪——
//     否则 Agent 会"忘记自己是谁"；
//   - 超出窗口时，丢弃"保护区之后最旧"的消息，保留最近的。
//
// 为什么窗口策略重要？
// 上下文窗口装不下无限历史。若盲目截断头部，会连 system 一起丢掉；
// 若不做裁剪，token 超限后 API 直接报错。滑动窗口 + 保护头部是最
// 简单可靠的折中方案（更精细的"摘要压缩"留待后续优化）。
type ShortTermMemory struct {
	maxMessages int             // 窗口上限（总消息数）
	protected   int             // 保护的消息数（从头开始数）
	messages    []schema.Message // 消息序列（内部状态）
}

// NewShortTermMemory 创建短期记忆。
// maxMessages 必须 > 0；protected 默认取 0，若配置了 system 建议传入 1。
func NewShortTermMemory(maxMessages int, protected int) (*ShortTermMemory, error) {
	if maxMessages <= 0 {
		return nil, fmt.Errorf("memory: maxMessages 必须大于 0")
	}
	if protected < 0 {
		return nil, fmt.Errorf("memory: protected 不能为负数")
	}
	if protected > maxMessages {
		// 保护消息数超过窗口上限会导致永远裁剪不掉，属于配置错误
		return nil, fmt.Errorf("memory: protected(%d) 不能超过 maxMessages(%d)", protected, maxMessages)
	}
	return &ShortTermMemory{
		maxMessages: maxMessages,
		protected:   protected,
		messages:    make([]schema.Message, 0, maxMessages),
	}, nil
}

// Add 追加消息并在超限时自动裁剪。
func (m *ShortTermMemory) Add(msg schema.Message) {
	m.messages = append(m.messages, msg)
	m.Trim()
}

// Recent 返回窗口内消息的副本（防止外部直接修改内部状态）。
func (m *ShortTermMemory) Recent() []schema.Message {
	out := make([]schema.Message, len(m.messages))
	copy(out, m.messages)
	return out
}

// Trim 裁剪超限消息，返回被丢弃的条数。
//
// 配对保护：裁剪边界不得切开"assistant(带 tool_calls) ↔ role=tool 结果"
// 的配对。若窗口首条是 tool 消息，说明其配对的 assistant(tool_calls) 恰好
// 被丢弃（或历史以 tool 开头），此时少丢弃一条把它保留，避免请求中出现
// "无主工具结果"——OpenAI 兼容协议会直接拒绝这类消息（HTTP 400：
// Messages with role 'tool' must be a response to a preceding message
// with 'tool_calls'），导致会话整轮失败。
func (m *ShortTermMemory) Trim() int {
	if len(m.messages) <= m.maxMessages {
		return 0
	}

	// 保护区保留；可裁剪区 = messages[protected:]
	maxTail := m.maxMessages - m.protected // 可裁剪区允许的最大长度
	tail := m.messages[m.protected:]
	if len(tail) <= maxTail {
		return 0
	}

	// 丢弃可裁剪区最旧的 (len(tail)-maxTail) 条
	drop := len(tail) - maxTail

	// 配对保护：窗口首条若为 tool 消息，向前回溯保留其配对的 assistant。
	for drop > 0 && tail[drop].Role == schema.RoleTool {
		drop--
	}

	// 用新切片组装，避免原地覆盖造成的别名问题
	out := make([]schema.Message, 0, m.maxMessages)
	out = append(out, m.messages[:m.protected]...)
	out = append(out, tail[drop:]...)
	m.messages = out
	return drop
}

// Len 返回当前消息总数。
func (m *ShortTermMemory) Len() int {
	return len(m.messages)
}

// Clear 清空窗口内全部消息（如开启新话题 / 重置会话时）。
func (m *ShortTermMemory) Clear() {
	m.messages = m.messages[:0]
}

// 编译期断言：ShortTermMemory 实现了 Memory 接口。
var _ Memory = (*ShortTermMemory)(nil)
