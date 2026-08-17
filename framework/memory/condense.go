package memory

import (
	"context"
	"fmt"

	"github.com/Steve5201/agent-framework/schema"
)

// CondenseFunc 摘要压缩回调：把一组即将被裁剪的旧消息压缩成一条紧凑摘要。
//
// 设计原则（对齐业界 LLM-based context condensation，如 LangChain / AutoGen
// 的 summarization memory）：滑动窗口超限时不再"直接丢弃最旧消息"，而是让
// LLM 把它们"蒸馏"成一条摘要（通常 100~300 字），模型仍能感知早期对话的
// 核心事实，只是失去了逐字细节。
//
// 实现者负责：
//   - 输出长度控制，避免摘要本身吃掉上下文预算；
//   - 返回错误（如上游不可用/超时），框架会退化为普通裁剪，不阻塞对话。
//
// 返回空字符串视为"放弃本次压缩"，对应消息直接丢弃（摘要维持原值）。
type CondenseFunc func(ctx context.Context, dropped []schema.Message) (string, error)

// Condensable 记忆可压缩接口：上层（如 agent.Session）在持有 ctx 的时机调用，
// 把窗口中最旧的待压缩消息真正"压成"摘要。
//
// 为什么是独立接口而不是塞进 Memory.Add/Trim？Memory 接口不携带 ctx，
// 而摘要压缩通常需要调用 LLM（必须传 ctx 才能正确取消）。因此压缩动作由
// 调用方显式触发；未实现本接口的记忆（如 ShortTermMemory）维持纯滑动窗口。
type Condensable interface {
	// Condense 尝试把待压缩消息压缩成摘要。无待压缩消息/无压缩器时直接返回 nil。
	Condense(ctx context.Context) error
}

// CondenseInfo 一次上下文压缩的结果信息（供宿主落库/向用户提示）。
// 语义：本次把 Dropped 条最旧消息压成一条摘要（第 Count 次压缩）。
type CondenseInfo struct {
	Dropped int // 本次压缩移出窗口并压成摘要的消息条数
	Count   int // 会话累计压缩次数（从 1 开始，逐级浓缩见 CondensingMemory.summary）
}

// CondenseInfoAware 记忆可报告"最近一次压缩"的结果信息。
// Condense 本身只返回 error，压缩成功后宿主往往还想知道"压掉了多少条"
// （落库压缩记录 / 向前端提示），因此提供消费式读取：取走后即清空，
// 避免同一条记录被重复上报。
type CondenseInfoAware interface {
	// ConsumeLastCondense 返回最近一次成功压缩的信息并清空；无记录时返回 nil。
	ConsumeLastCondense() *CondenseInfo
}

// CondensingMemory 摘要压缩型短期记忆：与 ShortTermMemory 的滑动窗口语义
// 一致，但超限时先把最旧消息暂存为"待压缩"，由调用方触发 Condense 交给
// CondenseFunc 压成一条 system 摘要，而不是直接丢弃。
//
// 内部状态布局：
//
//	messages = [受保护区][活跃消息...]   （摘要与待压缩消息单独维护）
//	Recent() 返回 = [受保护区][摘要 system][活跃消息...]
//
// 窗口预算：maxMessages 扣除受保护区与摘要各占 1 个位置后的余额，是活跃
// 消息的上限。摘要会随窗口继续滑动再次溢出——此时旧摘要 + 新丢弃消息一起
// 交给压缩器（摘要逐级浓缩，早期上下文不丢失，只越来越精炼）。
type CondensingMemory struct {
	maxMessages  int              // 窗口上限（总消息数，含受保护与摘要）
	protected    int              // 保护的消息数（从头开始数）
	condense     CondenseFunc     // 摘要压缩回调（nil = 退化为普通滑动窗口）
	messages     []schema.Message // 消息序列：受保护区 + 活跃消息
	summary      string           // 当前摘要文本（空 = 尚无摘要）
	pending      []schema.Message // 已移出窗口、待压缩的旧消息
	condenseNum  int              // 累计成功压缩次数（用于 CondenseInfo.Count）
	lastCondense *CondenseInfo    // 最近一次成功压缩的信息（消费式，见 ConsumeLastCondense）
}

// NewCondensingMemory 创建摘要压缩型短期记忆。
// 参数语义与 NewShortTermMemory 一致（maxMessages 必须 > 0，0 <= protected <= maxMessages）；
// condense 可为 nil，表示纯滑动窗口（与 ShortTermMemory 行为等价）。
func NewCondensingMemory(maxMessages int, protected int, condense CondenseFunc) (*CondensingMemory, error) {
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
	return &CondensingMemory{
		maxMessages: maxMessages,
		protected:   protected,
		condense:    condense,
		messages:    make([]schema.Message, 0, maxMessages),
	}, nil
}

// SetCondenser 设置/替换摘要压缩回调（nil = 关闭压缩，退化为普通滑动窗口）。
// 用于在记忆创建之后注入压缩器（如 agent.NewSession 先建记忆、再经 Option 注入）。
func (m *CondensingMemory) SetCondenser(fn CondenseFunc) {
	m.condense = fn
}

// Add 追加消息并在超限时把最旧消息移入"待压缩"区（窗口保持有界）。
func (m *CondensingMemory) Add(msg schema.Message) {
	m.messages = append(m.messages, msg)
	m.overflowToPending()
}

// activeBudget 当前可容纳的活跃消息数（不含受保护区与摘要）。
// 摘要占据一个槽位后活跃预算相应收缩，保证 Recent() 不超窗口上限。
func (m *CondensingMemory) activeBudget() int {
	budget := m.maxMessages - m.protected
	if m.summary != "" {
		budget--
	}
	if budget < 0 {
		return 0
	}
	return budget
}

// overflowToPending 窗口超限时，把最旧的活跃消息移入 pending（待压缩），
// 保证窗口有界。与 ShortTermMemory 一致：不切开 assistant(tool_calls)↔tool
// 配对——若窗口首条是 tool 消息，向前回溯少丢一条（配对保护允许窗口短暂
// 超限，避免协议 400）。
//
// 批量压缩策略：溢出时一次压到窗口的约 1/3（保留 budget/3 条活跃消息），
// 而不是挤牙膏式每次只压超出的几条——否则窗口一满，后续每轮对话都会触发
// 一次 LLM 压缩调用（高频小压缩，成本高且与"压缩一次应空出很多"的预期不符）。
// 批量压一次腾出约 2/3 窗口空间，压缩频率从"每轮一次"降到"每 ~2/3 窗口一次"。
func (m *CondensingMemory) overflowToPending() {
	if len(m.messages) <= m.protected {
		return
	}
	active := m.messages[m.protected:]
	budget := m.activeBudget()
	if len(active) <= budget {
		return
	}
	target := budget / 3
	if target < 1 {
		target = 1
	}
	drop := len(active) - target
	// 配对保护：不得切开 assistant(tool_calls) ↔ tool 配对
	for drop > 0 && drop < len(active) && active[drop].Role == schema.RoleTool {
		drop--
	}
	if drop <= 0 {
		return // 边界已在配对中，放弃本次裁剪（保配对优先）
	}
	m.pending = append(m.pending, active[:drop]...)
	// 用新切片组装，避免原地覆盖造成的别名问题
	out := make([]schema.Message, 0, m.maxMessages)
	out = append(out, m.messages[:m.protected]...)
	out = append(out, active[drop:]...)
	m.messages = out
}

// Condense 实现 Condensable：把待压缩消息交给 CondenseFunc 生成摘要。
// 旧摘要会与新丢弃消息一起参与压缩（摘要逐级浓缩，早期上下文不丢失）。
// 无待压缩消息 → 直接返回 nil；无压缩器/压缩失败 → 待压缩消息丢弃、摘要
// 维持原值，不阻塞对话（返回错误供调用方记录）。
//
// 压缩成功（生成非空摘要）时记录 CondenseInfo（Dropped/Count），调用方可
// 经 ConsumeLastCondense 消费，用于落库压缩记录 / 向前端提示"哪个节点压缩过"。
func (m *CondensingMemory) Condense(ctx context.Context) error {
	if len(m.pending) == 0 {
		return nil
	}
	dropped := m.pending
	m.pending = nil
	if m.condense == nil {
		return nil // 未配置压缩器：退化为普通滑动窗口（直接丢弃）
	}
	combined := make([]schema.Message, 0, len(dropped)+1)
	if m.summary != "" {
		combined = append(combined, schema.Message{Role: schema.RoleSystem, Content: m.summary})
	}
	combined = append(combined, dropped...)
	text, err := m.condense(ctx, combined)
	if err != nil {
		return fmt.Errorf("memory: 摘要压缩失败: %w", err)
	}
	if text != "" {
		m.summary = text
		// 压缩成功：记录本次信息（Dropped 为实际丢弃的消息条数）。
		m.condenseNum++
		m.lastCondense = &CondenseInfo{Dropped: len(dropped), Count: m.condenseNum}
	}
	// 摘要占据一个槽位后活跃预算收缩，重新平衡窗口
	m.overflowToPending()
	return nil
}

// ConsumeLastCondense 实现 CondenseInfoAware：返回最近一次成功压缩的信息
// 并清空（消费式——同一记录只上报一次，避免重复落库/提示）。
func (m *CondensingMemory) ConsumeLastCondense() *CondenseInfo {
	info := m.lastCondense
	m.lastCondense = nil
	return info
}

// Recent 返回窗口内消息的副本：受保护区 + 摘要（system）+ 活跃消息。
// 外部修改不影响内部状态。摘要以 system 角色插入受保护区之后——
// OpenAI 兼容协议允许多条 system 消息，模型据此感知"早期对话梗概"。
func (m *CondensingMemory) Recent() []schema.Message {
	if m.summary == "" {
		out := make([]schema.Message, len(m.messages))
		copy(out, m.messages)
		return out
	}
	insertAt := min(m.protected, len(m.messages))
	out := make([]schema.Message, 0, len(m.messages)+1)
	out = append(out, m.messages[:insertAt]...)
	out = append(out, schema.Message{Role: schema.RoleSystem, Content: "（先前对话摘要）" + m.summary})
	out = append(out, m.messages[insertAt:]...)
	return out
}

// Trim 按窗口策略裁剪超限消息，返回被移出窗口（待压缩/丢弃）的条数。
// Add 已内部触发溢出平衡；本方法主要供显式调用场景，语义与 ShortTermMemory 对齐。
func (m *CondensingMemory) Trim() int {
	before := len(m.messages)
	m.overflowToPending()
	return before - len(m.messages)
}

// Len 返回当前消息总数（含摘要，含受保护区）。
func (m *CondensingMemory) Len() int {
	n := len(m.messages)
	if m.summary != "" {
		n++
	}
	return n
}

// Clear 清空窗口内全部消息与摘要（如开启新话题 / 重置会话时）。
func (m *CondensingMemory) Clear() {
	m.messages = m.messages[:0]
	m.pending = m.pending[:0]
	m.summary = ""
	m.condenseNum = 0
	m.lastCondense = nil
}

// 编译期断言：CondensingMemory 实现了 Memory、Condensable 与 CondenseInfoAware 接口。
var (
	_ Memory            = (*CondensingMemory)(nil)
	_ Condensable       = (*CondensingMemory)(nil)
	_ CondenseInfoAware = (*CondensingMemory)(nil)
)
