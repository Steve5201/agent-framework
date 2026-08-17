package memory

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
)

// msg 构造测试用消息。
func msg(role schema.Role, content string) schema.Message {
	return schema.Message{Role: role, Content: content}
}

// TestNew_InvalidConfig 验证非法配置报错。
func TestNew_InvalidConfig(t *testing.T) {
	if _, err := NewShortTermMemory(0, 0); err == nil {
		t.Error("maxMessages=0 应报错")
	}
	if _, err := NewShortTermMemory(10, -1); err == nil {
		t.Error("protected 为负数应报错")
	}
	if _, err := NewShortTermMemory(5, 6); err == nil {
		t.Error("protected > maxMessages 应报错")
	}
}

// TestAdd_WithinWindow 验证窗口内不裁剪。
func TestAdd_WithinWindow(t *testing.T) {
	m, err := NewShortTermMemory(3, 0)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	m.Add(msg(schema.RoleUser, "1"))
	m.Add(msg(schema.RoleUser, "2"))
	if m.Len() != 2 {
		t.Errorf("Len = %d, want 2", m.Len())
	}
}

// TestTrim_Overflow 验证超限裁剪（丢最旧，留最近）。
func TestTrim_Overflow(t *testing.T) {
	m, err := NewShortTermMemory(3, 0)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	for i := 1; i <= 5; i++ {
		m.Add(msg(schema.RoleUser, string(rune('0'+i))))
	}
	if m.Len() != 3 {
		t.Errorf("Len = %d, want 3（窗口上限）", m.Len())
	}
	recent := m.Recent()
	if recent[0].Content != "3" {
		t.Errorf("应保留最近的 3 条，最旧应丢弃，实际首条 = %q", recent[0].Content)
	}
	if recent[2].Content != "5" {
		t.Errorf("最后一条应为 5，实际 = %q", recent[2].Content)
	}
}

// TestProtected 验证保护消息不被裁剪。
func TestProtected(t *testing.T) {
	m, err := NewShortTermMemory(3, 1) // 保护 1 条（如 system）
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	m.Add(msg(schema.RoleSystem, "你是助手"))
	m.Add(msg(schema.RoleUser, "1"))
	m.Add(msg(schema.RoleUser, "2"))
	m.Add(msg(schema.RoleUser, "3")) // 触发裁剪

	if m.Len() != 3 {
		t.Errorf("Len = %d, want 3", m.Len())
	}
	recent := m.Recent()
	if recent[0].Role != schema.RoleSystem {
		t.Error("保护的系统消息不应被裁剪")
	}
	if recent[0].Content != "你是助手" {
		t.Errorf("保护消息内容 = %q", recent[0].Content)
	}
	// 裁剪掉的应是 user 消息"1"
	if recent[1].Content != "2" {
		t.Errorf("裁剪后第二条应为 2，实际 = %q", recent[1].Content)
	}
}

// TestRecent_Copy 验证 Recent 返回副本，外部修改不影响内部。
func TestRecent_Copy(t *testing.T) {
	m, _ := NewShortTermMemory(5, 0)
	m.Add(msg(schema.RoleUser, "hi"))

	got := m.Recent()
	got[0].Content = "篡改"
	if m.Recent()[0].Content != "hi" {
		t.Error("Recent 应返回副本，内部状态不应被外部修改")
	}
}

// TestNoopLongTermMemory 验证空实现可调用且不报错。
func TestNoopLongTermMemory(t *testing.T) {
	var mem LongTermMemory = NoopLongTermMemory{}
	ctx := context.Background()

	id, err := mem.Remember(ctx, MemoryEntry{Content: "某条记忆"})
	if err != nil {
		t.Errorf("Remember error = %v", err)
	}
	if id != "" {
		t.Errorf("Noop Remember 应返回空 ID，实际 = %q", id)
	}
	got, err := mem.Recall(ctx, "查询", 5)
	if err != nil {
		t.Errorf("Recall error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Noop Recall 应返回空，实际 = %v", got)
	}
	if _, err := mem.Get(ctx, "id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Noop Get 应返回 ErrNotFound，实际 = %v", err)
	}
	list, err := mem.List(ctx)
	if err != nil || len(list) != 0 {
		t.Errorf("Noop List 应返回空，实际 = %v, err=%v", list, err)
	}
	if err := mem.Forget(ctx, "id"); err != nil {
		t.Errorf("Forget error = %v", err)
	}
	if err := mem.Clear(ctx); err != nil {
		t.Errorf("Clear error = %v", err)
	}
}

// TestClear 验证短期记忆可清空。
func TestClear(t *testing.T) {
	m, _ := NewShortTermMemory(5, 0)
	m.Add(msg(schema.RoleUser, "1"))
	m.Add(msg(schema.RoleUser, "2"))
	m.Clear()
	if m.Len() != 0 {
		t.Errorf("Clear 后 Len = %d, want 0", m.Len())
	}
}

// ---- 配对保护（线上 400 bug 回归）----

// toolMsg 构造 tool 结果消息。
func toolMsg(id string) schema.Message {
	return schema.Message{Role: schema.RoleTool, Content: "ok", ToolCallID: id}
}

// assistantWithTC 构造带 tool_calls 的 assistant 消息。
func assistantWithTC(id string) schema.Message {
	return schema.Message{
		Role:      schema.RoleAssistant,
		Content:   "",
		ToolCalls: []schema.ToolCall{{ID: id, Name: "file_ops"}},
	}
}

// firstOrphanToolIndex 返回窗口内第一个"无主 tool"的下标；无则返回 -1。
// 校验协议：tool 消息必须被其之前某条 assistant(tool_calls) 声明过。
func firstOrphanToolIndex(msgs []schema.Message) int {
	declared := map[string]bool{}
	for i, m := range msgs {
		if m.Role == schema.RoleAssistant {
			for _, tc := range m.ToolCalls {
				declared[tc.ID] = true
			}
		}
		if m.Role == schema.RoleTool && !declared[m.ToolCallID] {
			return i
		}
	}
	return -1
}

// TestTrim_PreservesToolPairing 回归线上 bug（会话 102 第一轮 400）：
// 单轮多次工具调用使消息超窗，裁剪不得切开 assistant(tool_calls)↔tool 配对，
// 否则请求出现孤立 tool，OpenAI 兼容协议直接拒绝（HTTP 400）。
//
// 场景还原：maxMessages=4、protected=1（system 位置），消息依次为
// user(受保护) + [assistant(tc), tool]×3。最后一次 Add 使总数达 7 > 4，
// 裁剪边界正落在第 1 组配对上。
func TestTrim_PreservesToolPairing(t *testing.T) {
	m, err := NewShortTermMemory(4, 1)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	m.Add(msg(schema.RoleUser, "知识库在哪")) // 受保护首条
	m.Add(assistantWithTC("c1"))
	m.Add(toolMsg("c1"))
	m.Add(assistantWithTC("c2"))
	m.Add(toolMsg("c2"))
	m.Add(assistantWithTC("c3"))
	m.Add(toolMsg("c3")) // 触发裁剪：7 > 4，drop 会切开第 1 组配对

	got := m.Recent()
	if idx := firstOrphanToolIndex(got); idx >= 0 {
		t.Fatalf("窗口出现孤立 tool（下标 %d）：%+v", idx, got)
	}
	// 配对保护允许窗口短暂超限（保配对优先），但不应无限膨胀
	if m.Len() > m.maxMessages+2 {
		t.Errorf("窗口 Len = %d，超限过多（上限 %d）", m.Len(), m.maxMessages)
	}
	// 受保护首条必须保留
	if got[0].Role != schema.RoleUser || got[0].Content != "知识库在哪" {
		t.Errorf("受保护首条被裁剪：%+v", got[0])
	}
}

// TestTrim_PreservesConsecutiveToolPairing 连续多组配对被整体保留，
// 不允许出现"assistant 被丢、tool 残留"或"tool 被丢、assistant 残留"。
func TestTrim_PreservesConsecutiveToolPairing(t *testing.T) {
	m, err := NewShortTermMemory(5, 1)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	m.Add(msg(schema.RoleUser, "u")) // 受保护
	// 6 组配对 + 1 条新用户消息 = 13 条，窗口 5，需裁剪
	for i := 1; i <= 6; i++ {
		m.Add(assistantWithTC("c" + strconv.Itoa(i)))
		m.Add(toolMsg("c" + strconv.Itoa(i)))
	}
	m.Add(msg(schema.RoleUser, "第二轮"))

	got := m.Recent()
	if idx := firstOrphanToolIndex(got); idx >= 0 {
		t.Fatalf("窗口出现孤立 tool（下标 %d）", idx)
	}
	// 窗口末尾必须是新用户消息（最近的消息不丢）
	if last := got[len(got)-1]; last.Role != schema.RoleUser || last.Content != "第二轮" {
		t.Errorf("窗口末尾应为最新用户消息，实际 = %+v", last)
	}
}
