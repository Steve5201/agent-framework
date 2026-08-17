package memory

import (
	"context"
	"errors"
	"testing"
)

// TestInMemory_Remember_GenerateID 验证自动生成 ID 且可重复调用。
func TestInMemory_Remember_GenerateID(t *testing.T) {
	m := NewInMemoryLongTermMemory()
	ctx := context.Background()

	id, err := m.Remember(ctx, MemoryEntry{Content: "用户偏好 Go"})
	if err != nil {
		t.Fatalf("Remember error = %v", err)
	}
	if id == "" {
		t.Fatal("自动生成的 ID 不应为空")
	}

	// 相同 ID 再次写入视为覆盖，不产生重复条目
	id2, err := m.Remember(ctx, MemoryEntry{ID: id, Content: "用户偏好 Go（更新）"})
	if err != nil {
		t.Fatalf("Remember 覆盖 error = %v", err)
	}
	if id2 != id {
		t.Errorf("覆盖时应返回原 ID，实际 = %q", id2)
	}
	list, _ := m.List(ctx)
	if len(list) != 1 {
		t.Errorf("覆盖后条目数 = %d, want 1", len(list))
	}
	got, _ := m.Get(ctx, id)
	if got.Content != "用户偏好 Go（更新）" {
		t.Errorf("覆盖后 Content = %q", got.Content)
	}
}

// TestInMemory_Recall 验证关键词检索的命中与排序。
func TestInMemory_Recall(t *testing.T) {
	m := NewInMemoryLongTermMemory()
	ctx := context.Background()

	_, _ = m.Remember(ctx, MemoryEntry{Content: "用户偏好 Go 语言", Source: "conversation", Tags: []string{"preference"}})
	_, _ = m.Remember(ctx, MemoryEntry{Content: "正在学习 Tauri 框架", Source: "conversation", Tags: []string{"tauri"}})
	_, _ = m.Remember(ctx, MemoryEntry{Content: "示例大学大二学生", Source: "user_profile", Tags: []string{"profile"}})

	// 只命中第一条（含 "Go"）
	got, err := m.Recall(ctx, "Go", 5)
	if err != nil {
		t.Fatalf("Recall error = %v", err)
	}
	if len(got) != 1 || got[0].Content != "用户偏好 Go 语言" {
		t.Errorf("Recall(Go) = %+v, want 仅命中 Go 条目", got)
	}

	// 命中两条（"Tauri" 出现在内容与标签）
	got, err = m.Recall(ctx, "Tauri", 5)
	if err != nil {
		t.Fatalf("Recall error = %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Recall(Tauri) 命中数 = %d, want 1", len(got))
	}

	// 中文关键词检索
	got, err = m.Recall(ctx, "示例大学", 5)
	if err != nil {
		t.Fatalf("Recall error = %v", err)
	}
	if len(got) != 1 || got[0].Content != "示例大学大二学生" {
		t.Errorf("Recall(示例大学) = %+v", got)
	}

	// limit 限制条数
	for i := 0; i < 5; i++ {
		_, _ = m.Remember(ctx, MemoryEntry{Content: "Go 并发模型"})
	}
	got, err = m.Recall(ctx, "Go", 3)
	if err != nil {
		t.Fatalf("Recall error = %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Recall limit=3 实际返回 %d 条", len(got))
	}
	// 未命中时返回空但不报错
	got, err = m.Recall(ctx, "不存在的关键词xyz", 5)
	if err != nil || len(got) != 0 {
		t.Errorf("未命中应返回空且无错误，实际 = %v, err=%v", got, err)
	}
}

// TestInMemory_GetNotFound 验证读取不存在的记忆返回 ErrNotFound。
func TestInMemory_GetNotFound(t *testing.T) {
	m := NewInMemoryLongTermMemory()
	if _, err := m.Get(context.Background(), "no-such-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在应返回 ErrNotFound，实际 = %v", err)
	}
}

// TestInMemory_Forget 验证删除与不存在删除的报错。
func TestInMemory_Forget(t *testing.T) {
	m := NewInMemoryLongTermMemory()
	ctx := context.Background()

	id, _ := m.Remember(ctx, MemoryEntry{Content: "临时记忆"})
	if err := m.Forget(ctx, id); err != nil {
		t.Fatalf("Forget error = %v", err)
	}
	if _, err := m.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后应不存在，实际 err = %v", err)
	}
	if err := m.Forget(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("重复删除应返回 ErrNotFound，实际 = %v", err)
	}
}

// TestInMemory_Clear 验证清空全部。
func TestInMemory_Clear(t *testing.T) {
	m := NewInMemoryLongTermMemory()
	ctx := context.Background()

	_, _ = m.Remember(ctx, MemoryEntry{Content: "a"})
	_, _ = m.Remember(ctx, MemoryEntry{Content: "b"})
	if err := m.Clear(ctx); err != nil {
		t.Fatalf("Clear error = %v", err)
	}
	list, _ := m.List(ctx)
	if len(list) != 0 {
		t.Errorf("Clear 后条目数 = %d, want 0", len(list))
	}
}

// TestInMemory_List_Order 验证 List 按更新时间倒序。
func TestInMemory_List_Order(t *testing.T) {
	m := NewInMemoryLongTermMemory()
	ctx := context.Background()

	_, _ = m.Remember(ctx, MemoryEntry{Content: "先写"})
	_, _ = m.Remember(ctx, MemoryEntry{Content: "后写"})
	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List 条目数 = %d, want 2", len(list))
	}
	if list[0].Content != "后写" || list[1].Content != "先写" {
		t.Errorf("List 顺序 = %q,%q, want 后写,先写", list[0].Content, list[1].Content)
	}
}
