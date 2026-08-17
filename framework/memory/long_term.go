package memory

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// wordRe 用于把查询切成连续"字母数字 / 中文"词组（忽略空格与标点）。
// 注：中文按 \p{L} 连续匹配，"用户偏好"整体是一个词。
var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// InMemoryLongTermMemory 基于内存 map 的长期记忆实现。
//
// 特点：
//   - 零外部依赖，开箱即用（适合单机 / 测试 / 原型阶段）；
//   - 检索用"关键词匹配"（词命中计数），不需要向量库也能跑通
//     "记一条 → 查相关"的完整闭环；
//   - 进程退出即丢失——需要持久化时，请实现自己的 LongTermMemory
//     （参考 docs/api/framework.md 第 6 章：实现 LongTermMemory 的完整套路）。
//
// P3 接入 pgvector 后，可替换为向量语义检索实现，上层零改动。
type InMemoryLongTermMemory struct {
	mu      sync.RWMutex
	entries map[string]MemoryEntry
	order   map[string]int64 // ID -> 插入序号（排序 tie-breaker：时间相同时保证后插入排前）
	seq     int64            // 自增序列，用于生成 ID 后缀与排序
}

// NewInMemoryLongTermMemory 创建内存长期记忆。
func NewInMemoryLongTermMemory() *InMemoryLongTermMemory {
	return &InMemoryLongTermMemory{
		entries: make(map[string]MemoryEntry),
		order:   make(map[string]int64),
	}
}

// Remember 存入一条记忆：ID 为空自动生成；重复 ID 覆盖更新并刷新时间戳。
func (m *InMemoryLongTermMemory) Remember(_ context.Context, entry MemoryEntry) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if entry.ID == "" {
		m.seq++
		entry.ID = fmt.Sprintf("mem-%d-%d", now.UnixNano(), m.seq)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	m.entries[entry.ID] = entry
	m.order[entry.ID] = m.seq // 覆盖更新也刷新排序序号（视为"最新"）
	m.seq++
	return entry.ID, nil
}

// Recall 关键词匹配检索：把 query 切成词，按条目（内容/标签/来源）的
// 命中词数计分，命中越多排越前，返回前 limit 条。
func (m *InMemoryLongTermMemory) Recall(_ context.Context, query string, limit int) ([]MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keywords := tokenize(query)
	type hit struct {
		entry MemoryEntry
		score int
	}
	var hits []hit
	for _, e := range m.entries {
		if score := matchScore(e, keywords); score > 0 {
			hits = append(hits, hit{entry: e, score: score})
		}
	}
	// 命中数降序；同分时更新时间新的在前（时间相同时后插入的在前）
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return newer(hits[i].entry, hits[j].entry, m.order)
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
func (m *InMemoryLongTermMemory) Get(_ context.Context, id string) (MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return MemoryEntry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return e, nil
}

// List 列出全部记忆（按更新时间倒序）。
func (m *InMemoryLongTermMemory) List(_ context.Context) ([]MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]MemoryEntry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return newer(out[i], out[j], m.order) })
	return out, nil
}

// Forget 删除指定记忆；不存在时返回包装 ErrNotFound 的错误。
func (m *InMemoryLongTermMemory) Forget(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(m.entries, id)
	return nil
}

// Clear 清空全部记忆。
func (m *InMemoryLongTermMemory) Clear(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]MemoryEntry)
	m.order = make(map[string]int64)
	return nil
}

// newer 判断 a 是否比 b "更新"：先比更新时间戳；时间相同时比插入序号
// （后插入的视为更新）。order 中的序号由 Remember 单调递增维护，
// 保证排序在任意写入节奏下都稳定。
func newer(a, b MemoryEntry, order map[string]int64) bool {
	if a.UpdatedAt.Equal(b.UpdatedAt) {
		return order[a.ID] > order[b.ID]
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

// 编译期断言：InMemory 实现了 LongTermMemory 接口。
var _ LongTermMemory = (*InMemoryLongTermMemory)(nil)

// tokenize 把查询文本切成检索词：字母数字连续段为一个词。
// 中文汉字属于 \p{L}，"用户偏好 Go" 会被切成 ["用户偏好", "go"]。
func tokenize(q string) []string {
	return wordRe.FindAllString(strings.ToLower(q), -1)
}

// matchScore 计算一条记忆对关键词列表的命中得分。
// 关键词出现在内容 / 标签 / 来源中均计 1 分（重复词只计一次）。
func matchScore(e MemoryEntry, keywords []string) int {
	haystack := strings.ToLower(e.Content + " " + strings.Join(e.Tags, " ") + " " + e.Source)
	if haystack == "" {
		return 0
	}
	seen := make(map[string]struct{}, len(keywords))
	score := 0
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if _, dup := seen[kw]; dup {
			continue
		}
		seen[kw] = struct{}{}
		if strings.Contains(haystack, kw) {
			score++
		}
	}
	return score
}
