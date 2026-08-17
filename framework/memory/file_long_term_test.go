package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestFile 在临时目录创建文件长期记忆。
func newTestFile(t *testing.T) *FileLongTermMemory {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mem.json")
	m, err := NewFileLongTermMemory(path)
	if err != nil {
		t.Fatalf("NewFileLongTermMemory error = %v", err)
	}
	return m
}

// TestFile_CreateMissingDir 验证自动创建目录与文件。
func TestFile_CreateMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sub", "mem.json")
	m, err := NewFileLongTermMemory(path)
	if err != nil {
		t.Fatalf("NewFileLongTermMemory error = %v", err)
	}
	if m.path != path {
		t.Errorf("path = %q", m.path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("文件应已创建: %v", err)
	}
}

// TestFile_EmptyPath 验证空路径报错。
func TestFile_EmptyPath(t *testing.T) {
	if _, err := NewFileLongTermMemory(""); err == nil {
		t.Error("空路径应报错")
	}
}

// TestFile_RememberRecall 验证存 + 检索闭环。
func TestFile_RememberRecall(t *testing.T) {
	m := newTestFile(t)
	ctx := context.Background()

	id, err := m.Remember(ctx, MemoryEntry{Content: "用户偏好 Go 语言", Tags: []string{"preference"}})
	if err != nil {
		t.Fatalf("Remember error = %v", err)
	}
	if id == "" {
		t.Fatal("ID 不应为空")
	}

	hits, err := m.Recall(ctx, "Go", 5)
	if err != nil {
		t.Fatalf("Recall error = %v", err)
	}
	if len(hits) != 1 || hits[0].Content != "用户偏好 Go 语言" {
		t.Errorf("Recall = %+v", hits)
	}
}

// TestFile_PersistAcrossRestart 验证新实例可读到旧数据（模拟进程重启）。
func TestFile_PersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.json")

	m1, err := NewFileLongTermMemory(path)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	ctx := context.Background()
	id, err := m1.Remember(ctx, MemoryEntry{Content: "跨重启的记忆", Source: "conversation"})
	if err != nil {
		t.Fatalf("Remember error = %v", err)
	}

	// 新实例打开同一文件
	m2, err := NewFileLongTermMemory(path)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	got, err := m2.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if got.Content != "跨重启的记忆" {
		t.Errorf("重启后 Content = %q", got.Content)
	}
}

// TestFile_GetForgetNotFound 验证不存在时的 ErrNotFound。
func TestFile_GetForgetNotFound(t *testing.T) {
	m := newTestFile(t)
	ctx := context.Background()

	if _, err := m.Get(ctx, "no-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在应返回 ErrNotFound，实际 = %v", err)
	}
	if err := m.Forget(ctx, "no-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Forget 不存在应返回 ErrNotFound，实际 = %v", err)
	}
}

// TestFile_Clear 验证清空。
func TestFile_Clear(t *testing.T) {
	m := newTestFile(t)
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
