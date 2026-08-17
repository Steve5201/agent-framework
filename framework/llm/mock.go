package llm

import (
	"context"
	"io"

	"github.com/Steve5201/agent-framework/schema"
)

// MockProvider 内存版 Provider，用于测试 agent 等上层逻辑，
// 避免在单测中真实调用大模型（花钱、慢、不稳定）。
//
// 用法：
//
//	p := &MockProvider{Name_: "mock", Content: "你好"}
//	resp, _ := p.Chat(ctx, &Request{...})
type MockProvider struct {
	Name_        string                            // 供应商名
	Content      string                            // 非流式固定回答
	ToolCalls    []schema.ToolCall                 // 非流式固定工具调用
	Events       []StreamEvent                     // 流式固定事件序列（可选）
	Err          error                             // 可选：模拟调用失败
	ChatFn       func(*Request) (*Response, error) // 可选：非流式自定义行为
	ChatStreamFn func(*Request) (Stream, error)    // 可选：流式自定义行为
}

// Name 实现 Provider 接口。
func (m *MockProvider) Name() string {
	if m.Name_ == "" {
		return "mock"
	}
	return m.Name_
}

// Chat 实现 Provider 接口：优先走 ChatFn，否则返回预设内容/工具调用/错误。
func (m *MockProvider) Chat(_ context.Context, req *Request) (*Response, error) {
	if m.ChatFn != nil {
		return m.ChatFn(req)
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return &Response{Content: m.Content, ToolCalls: m.ToolCalls}, nil
}

// ChatStream 实现 Provider 接口：优先走 ChatStreamFn，否则按预设事件序列迭代。
func (m *MockProvider) ChatStream(_ context.Context, req *Request) (Stream, error) {
	if m.ChatStreamFn != nil {
		return m.ChatStreamFn(req)
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return &sliceStream{events: m.Events}, nil
}

// sliceStream 基于内存事件切片的 Stream 实现。
type sliceStream struct {
	events []StreamEvent
	idx    int
}

// NewSliceStream 构造基于内存事件切片的 Stream（测试用）。
// ChatStreamFn 自定义流式行为时可快速生成事件序列。
func NewSliceStream(events []StreamEvent) Stream {
	return &sliceStream{events: events}
}

// Next 实现 Stream 接口。
func (s *sliceStream) Next() (StreamEvent, error) {
	if s.idx >= len(s.events) {
		return StreamEvent{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

// Close 实现 Stream 接口（内存流无资源可释放）。
func (s *sliceStream) Close() error { return nil }

// 编译期断言：确保类型实现了接口（Go 惯例，提前暴露接口遗漏）。
var (
	_ Provider = (*MockProvider)(nil)
	_ Stream   = (*sliceStream)(nil)
)
