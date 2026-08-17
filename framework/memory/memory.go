// Package memory 提供 Agent 的记忆能力。
//
// 背景问题：大模型的上下文窗口是有限的（几万到百万 token），装不下
// 无限增长的对话历史。记忆模块负责"什么保留、什么丢弃"。
//
// 记忆分两层（对应"直接内容"与"跨会话内容"的区分）：
//   - 短期记忆（Memory / ShortTermMemory）：本次会话内的滑动窗口。
//     只保留最近 N 条消息，超限丢弃最旧的；开头保护消息（如 system
//     指令）永不裁剪。这保证 Agent 不会"失忆"。
//   - 长期记忆（LongTermMemory）：跨会话的持久记忆。
//     接口定义"记/查/读/列/删/清"全套能力；本包提供两种实现：
//     Noop（空实现，默认）与 InMemoryLongTermMemory（内存 + 关键词检索，
//     开箱即用）；P3 可接入 pgvector 实现向量语义检索，上层零改动。
//
// 本包零外部依赖（只依赖 schema + 标准库），可独立测试。
package memory

import (
	"context"
	"errors"
	"time"

	"github.com/Steve5201/agent-framework/schema"
)

// ErrNotFound 表示目标记忆不存在。
// 所有 LongTermMemory 实现都应返回本哨兵错误（或包含它的包装错误），
// 调用方可用 errors.Is(err, memory.ErrNotFound) 判断"没有这条记忆"。
var ErrNotFound = errors.New("memory: 记录不存在")

// Memory 会话内记忆接口。
// 具体实现（如 ShortTermMemory）负责窗口策略，Agent 只面向本接口编程。
type Memory interface {
	// Add 追加一条消息到对话历史。
	Add(msg schema.Message)

	// Recent 返回当前窗口内的全部消息（副本，外部修改不影响内部状态）。
	Recent() []schema.Message

	// Trim 按窗口策略裁剪超限消息（丢弃最旧的），返回被裁剪的条数。
	Trim() int

	// Len 返回当前消息总数。
	Len() int

	// Clear 清空窗口内全部消息（如开启新话题 / 重置会话时）。
	Clear()
}

// MemoryEntry 一条长期记忆条目。
// 长期记忆不是"一句话"，而是一段可管理的结构化记录：
// 内容 + 来源 + 标签 + 时间戳，便于检索、筛选与运维。
type MemoryEntry struct {
	// ID 唯一标识。Remember 时传空字符串表示"由实现自动生成"，
	// 返回值即新 ID；也可由调用方预先生成后传入（重复 ID 视为覆盖更新）。
	ID string

	// Content 记忆内容。例如"用户偏好 Go 语言，正在学习 Tauri"。
	Content string

	// Source 记忆来源（如 conversation / user_profile / knowledge），
	// 便于按来源筛选与溯源。
	Source string

	// Tags 可选标签（如 ["preference", "tauri"]），用于分类与检索。
	Tags []string

	// CreatedAt 创建时间（Remember 时若为零值，由实现填充当前时间）。
	CreatedAt time.Time

	// UpdatedAt 最近更新时间（由实现负责维护）。
	UpdatedAt time.Time
}

// LongTermMemory 跨会话长期记忆接口。
//
// 要用长期记忆功能，实现本接口的全部方法即可接入框架
// （注入方式见 agent.WithLongTermMemory）。
// 检索匹配策略由实现决定：无向量库时可用关键词匹配
// （参考 InMemoryLongTermMemory），P3 接入 pgvector 后可升级为语义检索。
type LongTermMemory interface {
	// Remember 存入一条记忆。
	// entry.ID 为空时由实现自动生成并返回新 ID；
	// 传入已存在的 ID 视为覆盖更新（刷新 UpdatedAt）。
	Remember(ctx context.Context, entry MemoryEntry) (string, error)

	// Recall 按查询内容检索相关记忆，返回最多 limit 条。
	// limit <= 0 表示不限条数。
	Recall(ctx context.Context, query string, limit int) ([]MemoryEntry, error)

	// Get 按 ID 读取单条记忆；不存在时返回包装了 ErrNotFound 的错误。
	Get(ctx context.Context, id string) (MemoryEntry, error)

	// List 列出全部记忆（实现按更新时间倒序返回）。
	List(ctx context.Context) ([]MemoryEntry, error)

	// Forget 删除指定 ID 的记忆；不存在时返回包装了 ErrNotFound 的错误。
	Forget(ctx context.Context, id string) error

	// Clear 清空全部记忆。
	Clear(ctx context.Context) error
}

// NoopLongTermMemory 长期记忆的空实现：所有操作直接成功但无效果。
//
// 用途：默认 / 测试场景下让上层代码"接得上"长期记忆但什么都不发生；
// 需要真实记忆时换成 InMemoryLongTermMemory 或自定义实现即可。
type NoopLongTermMemory struct{}

// Remember 空实现：不存储，原样返回传入 ID（空则返回空串）。
func (NoopLongTermMemory) Remember(_ context.Context, entry MemoryEntry) (string, error) {
	return entry.ID, nil
}

// Recall 空实现：无记忆可查，返回空。
func (NoopLongTermMemory) Recall(_ context.Context, _ string, _ int) ([]MemoryEntry, error) {
	return nil, nil
}

// Get 空实现：任何 ID 都不存在。
func (NoopLongTermMemory) Get(_ context.Context, _ string) (MemoryEntry, error) {
	return MemoryEntry{}, ErrNotFound
}

// List 空实现：无记忆可列。
func (NoopLongTermMemory) List(_ context.Context) ([]MemoryEntry, error) {
	return nil, nil
}

// Forget 空实现。
func (NoopLongTermMemory) Forget(_ context.Context, _ string) error { return nil }

// Clear 空实现。
func (NoopLongTermMemory) Clear(_ context.Context) error { return nil }

// 编译期断言：Noop 实现了 LongTermMemory 接口。
var _ LongTermMemory = (*NoopLongTermMemory)(nil)
