package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileLongTermMemory 基于 JSON 文件的长期记忆实现。
//
// 特点：
//   - 数据持久化到单个 JSON 文件，进程重启不丢失；
//   - 检索策略与 InMemoryLongTermMemory 相同（关键词匹配，
//     复用 tokenize / matchScore / newer 三个内部函数）；
//   - 适合单机部署 / 轻量使用；接入数据库后替换实现即可，上层零改动。
//
// 注意：每次写操作都会重写整个文件（简单可靠，但不适合超大条目数）。
type FileLongTermMemory struct {
	mu   sync.Mutex
	path string
}

// fileStorage 文件的持久化结构。
type fileStorage struct {
	Entries map[string]MemoryEntry `json:"entries"`
	Order   map[string]int64       `json:"order"`
	Seq     int64                  `json:"seq"`
}

// NewFileLongTermMemory 创建文件长期记忆。
// path 为 JSON 文件路径；不存在时自动创建（含父目录）。
func NewFileLongTermMemory(path string) (*FileLongTermMemory, error) {
	if path == "" {
		return nil, fmt.Errorf("memory: 文件路径不能为空")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("memory: 创建目录失败: %w", err)
		}
		st := fileStorage{
			Entries: make(map[string]MemoryEntry),
			Order:   make(map[string]int64),
		}
		if err := saveFile(path, st); err != nil {
			return nil, err
		}
	}
	return &FileLongTermMemory{path: path}, nil
}

// load 读取并解析记忆文件。
func (f *FileLongTermMemory) load() (fileStorage, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return fileStorage{}, fmt.Errorf("memory: 读取记忆文件失败: %w", err)
	}
	var st fileStorage
	if err := json.Unmarshal(data, &st); err != nil {
		return fileStorage{}, fmt.Errorf("memory: 解析记忆文件失败: %w", err)
	}
	if st.Entries == nil {
		st.Entries = make(map[string]MemoryEntry)
	}
	if st.Order == nil {
		st.Order = make(map[string]int64)
	}
	return st, nil
}

// saveFile 序列化并写入记忆文件。
func saveFile(path string, st fileStorage) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: 序列化记忆失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("memory: 写入记忆文件失败: %w", err)
	}
	return nil
}

// Remember 存入一条记忆并落盘。
func (f *FileLongTermMemory) Remember(_ context.Context, entry MemoryEntry) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, err := f.load()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if entry.ID == "" {
		st.Seq++
		entry.ID = fmt.Sprintf("mem-%d-%d", now.UnixNano(), st.Seq)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	st.Entries[entry.ID] = entry
	st.Order[entry.ID] = st.Seq // 覆盖更新也刷新排序序号（视为"最新"）
	st.Seq++
	if err := saveFile(f.path, st); err != nil {
		return "", err
	}
	return entry.ID, nil
}

// Recall 关键词匹配检索（与 InMemory 相同的计分与排序策略）。
func (f *FileLongTermMemory) Recall(_ context.Context, query string, limit int) ([]MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, err := f.load()
	if err != nil {
		return nil, err
	}
	keywords := tokenize(query)
	type hit struct {
		entry MemoryEntry
		score int
	}
	var hits []hit
	for _, e := range st.Entries {
		if s := matchScore(e, keywords); s > 0 {
			hits = append(hits, hit{entry: e, score: s})
		}
	}
	// 命中数降序；同分时更新（时间 → 插入序号）的在前
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return newer(hits[i].entry, hits[j].entry, st.Order)
	})
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}
	out := make([]MemoryEntry, 0, limit)
	for _, h := range hits[:limit] {
		out = append(out, h.entry)
	}
	return out, nil
}

// Get 按 ID 读取单条记忆；不存在时返回包装 ErrNotFound 的错误。
func (f *FileLongTermMemory) Get(_ context.Context, id string) (MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, err := f.load()
	if err != nil {
		return MemoryEntry{}, err
	}
	e, ok := st.Entries[id]
	if !ok {
		return MemoryEntry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return e, nil
}

// List 列出全部记忆（按更新时间倒序）。
func (f *FileLongTermMemory) List(_ context.Context) ([]MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, err := f.load()
	if err != nil {
		return nil, err
	}
	out := make([]MemoryEntry, 0, len(st.Entries))
	for _, e := range st.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return newer(out[i], out[j], st.Order) })
	return out, nil
}

// Forget 删除指定记忆；不存在时返回包装 ErrNotFound 的错误。
func (f *FileLongTermMemory) Forget(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := st.Entries[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(st.Entries, id)
	delete(st.Order, id)
	return saveFile(f.path, st)
}

// Clear 清空全部记忆。
func (f *FileLongTermMemory) Clear(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return saveFile(f.path, fileStorage{
		Entries: make(map[string]MemoryEntry),
		Order:   make(map[string]int64),
	})
}

// 编译期断言：File 实现了 LongTermMemory 接口。
var _ LongTermMemory = (*FileLongTermMemory)(nil)
