package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
)

// collectCondenser 记录每次压缩的输入并返回固定摘要（测试用压缩器）。
type collectCondenser struct {
	calls   [][]schema.Message // 每次压缩收到的消息
	summary string             // 固定返回的摘要
	err     error              // 可选：模拟压缩失败
}

func (c *collectCondenser) Condense(ctx context.Context, dropped []schema.Message) (string, error) {
	cp := make([]schema.Message, len(dropped))
	copy(cp, dropped)
	c.calls = append(c.calls, cp)
	if c.err != nil {
		return "", c.err
	}
	return c.summary, nil
}

// TestCondensing_InvalidConfig 验证非法配置报错（与 ShortTermMemory 一致）。
func TestCondensing_InvalidConfig(t *testing.T) {
	if _, err := NewCondensingMemory(0, 0, nil); err == nil {
		t.Error("maxMessages=0 应报错")
	}
	if _, err := NewCondensingMemory(10, -1, nil); err == nil {
		t.Error("protected 为负数应报错")
	}
	if _, err := NewCondensingMemory(5, 6, nil); err == nil {
		t.Error("protected > maxMessages 应报错")
	}
}

// TestCondensing_WithinWindow 窗口内不触发压缩，消息原样保留。
func TestCondensing_WithinWindow(t *testing.T) {
	cc := &collectCondenser{summary: "摘要"}
	m, err := NewCondensingMemory(5, 1, cc.Condense)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	m.Add(msg(schema.RoleSystem, "你是助手"))
	m.Add(msg(schema.RoleUser, "1"))
	m.Add(msg(schema.RoleUser, "2"))
	if m.Len() != 3 {
		t.Errorf("Len = %d, want 3", m.Len())
	}
	if len(cc.calls) != 0 {
		t.Errorf("窗口内不应触发压缩，实际压缩 %d 次", len(cc.calls))
	}
}

// TestCondensing_OverflowCondenses 超限后：旧消息被移出窗口、压缩成摘要，
// 摘要以 system 角色出现在受保护区之后。
func TestCondensing_OverflowCondenses(t *testing.T) {
	cc := &collectCondenser{summary: "用户问了A与B，均已答复"}
	m, err := NewCondensingMemory(4, 1, cc.Condense) // 保护1条 + 摘要1条 + 活跃2条
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	// 先灌入受保护区首条 + 3 条活跃 = 4 条，恰满窗口
	m.Add(msg(schema.RoleSystem, "你是助手"))
	m.Add(msg(schema.RoleUser, "问题A"))
	m.Add(msg(schema.RoleAssistant, "答复A"))
	m.Add(msg(schema.RoleUser, "问题B"))
	if err := m.Condense(context.Background()); err != nil {
		t.Fatalf("Condense error = %v", err)
	}
	if len(cc.calls) != 0 {
		t.Fatalf("未超限不应压缩，实际 %d 次", len(cc.calls))
	}

	// 再追一条 → 活跃 4 条 > 预算 2，移入 pending
	m.Add(msg(schema.RoleAssistant, "答复B"))
	if err := m.Condense(context.Background()); err != nil {
		t.Fatalf("Condense error = %v", err)
	}
	if len(cc.calls) != 1 {
		t.Fatalf("应压缩 1 次，实际 %d", len(cc.calls))
	}
	// 批量压缩：溢出时一次压到窗口约 1/3（预算 3 → 保留 1 条活跃），
	// 故本次移出最早的 3 条，而不是只压超出的 1 条。
	if len(cc.calls[0]) != 3 {
		t.Errorf("应压缩最早 3 条活跃消息，实际 %d 条", len(cc.calls[0]))
	}
	recent := m.Recent()
	if recent[0].Role != schema.RoleSystem || recent[0].Content != "你是助手" {
		t.Errorf("受保护区首条被篡改：%+v", recent[0])
	}
	if recent[1].Role != schema.RoleSystem || !strings.Contains(recent[1].Content, "摘要") {
		t.Errorf("摘要应以 system 消息出现在第二位：%+v", recent[1])
	}
	if recent[2].Content != "答复B" {
		t.Errorf("窗口应保留最近的活跃消息，首条活跃 = %q", recent[2].Content)
	}
	if m.Len() != 3 {
		t.Errorf("Len = %d, want 3（保护+摘要+1活跃）", m.Len())
	}
}

// TestCondensing_NilCondenserPlainDrop 无压缩器 = 普通滑动窗口：超限直接丢弃最旧。
// 批量策略：溢出时一次压到窗口 1/3（窗口 3 → 预算 3 → 保留 1 条活跃），
// 丢弃 3 条，后续窗口重新蓄水。
func TestCondensing_NilCondenserPlainDrop(t *testing.T) {
	m, err := NewCondensingMemory(3, 0, nil)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	for i := 1; i <= 5; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	if m.Len() != 2 {
		t.Errorf("Len = %d, want 2", m.Len())
	}
	recent := m.Recent()
	if recent[0].Content != "4" {
		t.Errorf("应保留最近 2 条，首条 = %q", recent[0].Content)
	}
}

// TestCondensing_CondenseFailureFallback 压缩失败：待压缩消息丢弃、窗口仍有界、
// 不新增摘要，对话不受阻。
func TestCondensing_CondenseFailureFallback(t *testing.T) {
	cc := &collectCondenser{summary: "x", err: errors.New("上游不可用")}
	m, err := NewCondensingMemory(4, 0, cc.Condense)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	for i := 1; i <= 6; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	if err := m.Condense(context.Background()); err == nil {
		t.Error("压缩失败应返回错误")
	}
	if m.Len() > 4 {
		t.Errorf("窗口应仍有界，Len = %d", m.Len())
	}
	for _, r := range m.Recent() {
		if strings.Contains(r.Content, "先前对话摘要") {
			t.Error("压缩失败不应产生摘要")
		}
	}
}

// TestCondensing_SuccessiveCondensation 二次压缩：旧摘要 + 新丢弃消息一起交给
// 压缩器（摘要逐级浓缩，早期上下文不丢失）。
func TestCondensing_SuccessiveCondensation(t *testing.T) {
	cc := &collectCondenser{summary: "S1"}
	m, err := NewCondensingMemory(4, 0, cc.Condense)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	for i := 1; i <= 6; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	if err := m.Condense(context.Background()); err != nil {
		t.Fatalf("第一次 Condense error = %v", err)
	}
	if len(cc.calls) != 1 {
		t.Fatalf("第一次应压缩 1 次，实际 %d", len(cc.calls))
	}
	// 继续追加直到再次超限
	for i := 7; i <= 12; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	if err := m.Condense(context.Background()); err != nil {
		t.Fatalf("第二次 Condense error = %v", err)
	}
	if len(cc.calls) != 2 {
		t.Fatalf("应压缩 2 次，实际 %d", len(cc.calls))
	}
	// 第二次压缩的输入必须包含旧摘要（system）
	gotSystem := false
	for _, mm := range cc.calls[1] {
		if mm.Role == schema.RoleSystem && strings.Contains(mm.Content, "S1") {
			gotSystem = true
		}
	}
	if !gotSystem {
		t.Errorf("第二次压缩应携带旧摘要 S1：%+v", cc.calls[1])
	}
	// Recent 中应有更新后的摘要
	if !strings.Contains(m.Recent()[0].Content, "S1") {
		t.Errorf("Recent 应含新摘要：%+v", m.Recent())
	}
}

// TestCondensing_PreservesToolPairing 超限移出消息时不得切开 assistant(tool_calls)
// ↔ tool 配对（复用 ShortTermMemory 的线上 400 回归场景）。
func TestCondensing_PreservesToolPairing(t *testing.T) {
	m, err := NewCondensingMemory(4, 1, nil)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	m.Add(msg(schema.RoleUser, "知识库在哪")) // 受保护首条
	m.Add(assistantWithTC("c1"))
	m.Add(toolMsg("c1"))
	m.Add(assistantWithTC("c2"))
	m.Add(toolMsg("c2"))
	m.Add(assistantWithTC("c3"))
	m.Add(toolMsg("c3")) // 触发裁剪：7 > 4

	got := m.Recent()
	if idx := firstOrphanToolIndex(got); idx >= 0 {
		t.Fatalf("窗口出现孤立 tool（下标 %d）：%+v", idx, got)
	}
	if got[0].Role != schema.RoleUser || got[0].Content != "知识库在哪" {
		t.Errorf("受保护首条被裁剪：%+v", got[0])
	}
}

// TestCondensing_Clear 清空重置摘要与待压缩区。
func TestCondensing_Clear(t *testing.T) {
	cc := &collectCondenser{summary: "s"}
	m, _ := NewCondensingMemory(4, 0, cc.Condense)
	for i := 1; i <= 6; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	_ = m.Condense(context.Background())
	if m.Len() == 0 {
		t.Fatal("压缩后 Len 不应为 0")
	}
	m.Clear()
	if m.Len() != 0 {
		t.Errorf("Clear 后 Len = %d, want 0", m.Len())
	}
	if m.summary != "" {
		t.Errorf("Clear 后摘要应清空，实际 = %q", m.summary)
	}
	// 清空后可继续正常使用
	m.Add(msg(schema.RoleUser, "新话题"))
	if m.Len() != 1 {
		t.Errorf("Clear 后追加 Len = %d, want 1", m.Len())
	}
}

// TestCondensing_RecentCopy Recent 返回副本，外部修改不影响内部状态。
func TestCondensing_RecentCopy(t *testing.T) {
	m, _ := NewCondensingMemory(5, 0, nil)
	m.Add(msg(schema.RoleUser, "hi"))
	got := m.Recent()
	got[0].Content = "篡改"
	if m.Recent()[0].Content != "hi" {
		t.Error("Recent 应返回副本，内部状态不应被外部修改")
	}
}

// TestCondensing_SetCondenser 记忆创建后仍可注入压缩器：待压缩消息会一直
// 保留到 Condense 被调用（agent.NewSession 先建记忆、再经 Option 注入的真实路径）。
func TestCondensing_SetCondenser(t *testing.T) {
	cc := &collectCondenser{summary: "注入的摘要"}
	m, _ := NewCondensingMemory(4, 0, nil) // 先无压缩器
	for i := 1; i <= 6; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	// 注入前：窗口超限的消息已进入待压缩区，但尚未被压缩
	if len(cc.calls) != 0 {
		t.Fatalf("注入前不应触发压缩，实际 %d 次", len(cc.calls))
	}
	m.SetCondenser(cc.Condense) // 后注入
	if err := m.Condense(context.Background()); err != nil {
		t.Fatalf("注入后 Condense error = %v", err)
	}
	if len(cc.calls) != 1 {
		t.Fatalf("注入压缩器后应触发压缩，实际 %d 次", len(cc.calls))
	}
	if !strings.Contains(m.Recent()[0].Content, "注入的摘要") {
		t.Errorf("Recent 应含注入后生成的摘要：%+v", m.Recent())
	}
}

// TestCondensing_ConsumeLastCondense 压缩成功后 ConsumeLastCondense 返回本次
// 信息（Dropped 条数 / 累计 Count），取走后清空；无压缩时返回 nil。
func TestCondensing_ConsumeLastCondense(t *testing.T) {
	cc := &collectCondenser{summary: "S"}
	m, err := NewCondensingMemory(4, 0, cc.Condense)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	// 未压缩：无记录
	if got := m.ConsumeLastCondense(); got != nil {
		t.Fatalf("未压缩前 Consume = %+v, want nil", got)
	}

	for i := 1; i <= 6; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	if err := m.Condense(context.Background()); err != nil {
		t.Fatalf("Condense error = %v", err)
	}
	// 4 条窗口、0 保护：预算 4（无摘要）→ 加入 6 条后，第 5 条触发批量压缩
	//（压到 1/3 → 保留 1 条），移出 4 条进 pending。
	got := m.ConsumeLastCondense()
	if got == nil {
		t.Fatal("压缩成功后应返回 CondenseInfo")
	}
	if got.Dropped != 4 {
		t.Errorf("Dropped = %d, want 4", got.Dropped)
	}
	if got.Count != 1 {
		t.Errorf("Count = %d, want 1（第一次压缩）", got.Count)
	}
	// 消费式：再次读取应为 nil（同一记录不重复上报）
	if again := m.ConsumeLastCondense(); again != nil {
		t.Errorf("消费后应清空，实际 = %+v", again)
	}

	// 继续追加直到再次压缩：Count 递增、Dropped 按实际丢弃数
	for i := 7; i <= 12; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	if err := m.Condense(context.Background()); err != nil {
		t.Fatalf("第二次 Condense error = %v", err)
	}
	got2 := m.ConsumeLastCondense()
	if got2 == nil || got2.Count != 2 {
		t.Errorf("第二次压缩 Count = %+v, want 2", got2)
	}
	if got2.Dropped == 0 {
		t.Errorf("第二次压缩 Dropped 不应为 0：%+v", got2)
	}
	// 压缩失败不产生记录
	ccErr := &collectCondenser{summary: "x", err: errors.New("上游不可用")}
	m2, _ := NewCondensingMemory(4, 0, ccErr.Condense)
	for i := 1; i <= 6; i++ {
		m2.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	_ = m2.Condense(context.Background())
	if got := m2.ConsumeLastCondense(); got != nil {
		t.Errorf("压缩失败不应产生记录，实际 = %+v", got)
	}
}
