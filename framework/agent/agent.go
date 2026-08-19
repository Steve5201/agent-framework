// Package agent 提供 Agent 会话与消息循环——框架的"大脑"。
//
// 职责：把已完成的四个模块串成一个完整循环：
//
//	用户消息 → 记忆追加 → 调 LLM（llm 包）→
//	├─ 无工具调用 → 返回回答，结束
//	└─ 有工具调用 → 逐个执行（tool 包）→ 结果回填记忆 → 再调 LLM …
//
// 这是"Agent 会思考"的引擎：LLM 负责"想"，工具负责"做"，
// 本包负责"反复地想和做，直到给出答案"。
//
// 依赖关系（遵循依赖纪律）：agent → llm / tool / memory / schema。
package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/memory"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// Session 一次 Agent 会话：持有配置、模型、工具、记忆。
// 一个 Session 对应一段连续的对话（多次用户消息之间共享历史与记忆）。
type Session struct {
	config      schema.AgentConfig
	provider    llm.Provider
	registry    *tool.Registry
	mem         memory.Memory
	longTerm    memory.LongTermMemory
	approval    func(schema.ToolCall) bool         // 工具确认回调（nil 表示需确认的工具一律拒绝）
	toolAudit   ToolAuditFunc                      // 工具执行审计回调（nil = 不审计）
	asyncRunner AsyncRunner                        // 外部异步执行器（阶段3·本地工具代理）
	pendingMu   sync.Mutex                         // 保护 pending 挂起表
	pending     map[string]chan *schema.ToolResult // 挂起中的外部工具调用：tool_call_id → 回填通道
	condenseN   []memory.CondenseInfo              // 本轮发生的上下文压缩记录（供宿主落库/提示，DrainCondenseNotices 消费）
}

// ToolAuditFunc 工具执行审计回调：每次工具执行结束后调用一次。
// 提供完整调用信息（含 ToolCall.ID/Arguments 与结果/耗时），供宿主
// （如 agent-service）写入审计日志或审计表。实现必须保持轻量，
// 不应阻塞会话主循环（失败只记日志，不影响对话）。
type ToolAuditFunc func(call schema.ToolCall, result *schema.ToolResult, err error, duration time.Duration)

// Option 定制会话行为。
type Option func(*Session)

// WithToolAuditor 设置工具执行审计回调（阶段1·审计落库用）。
// 该回调会在每次工具执行成功/失败后调用，宿主可据此记录
// user/session/tool/args/result/duration 到审计表。
func WithToolAuditor(f ToolAuditFunc) Option {
	return func(s *Session) { s.toolAudit = f }
}

// WithApprovalFunc 设置工具调用的用户确认回调。
// 返回 true 表示用户同意执行该工具；返回 false 则拒绝。
// 默认 nil：任何需要确认的工具（L2/L3）都会被拒绝。
func WithApprovalFunc(f func(schema.ToolCall) bool) Option {
	return func(s *Session) { s.approval = f }
}

// WithAsyncRunner 设置外部异步执行器（阶段3·本地工具代理）。
//
// 注册了 External=true 工具（如桌面本地工具）的会话必须配置执行器，
// 否则对应工具调用会直接报错。宿主（agent-service）实现 Dispatch 时，
// 通常结合 llm.WithHeader 注入的 user_id 把调用路由到正确的
// 会话/客户端，并在外部执行完成后调用 session.SubmitToolResult 回填。
func WithAsyncRunner(r AsyncRunner) Option {
	return func(s *Session) { s.asyncRunner = r }
}

// WithLongTermMemory 注入长期记忆实现（默认 NoopLongTermMemory 空实现）。
// 需要真实记忆时，可用 memory.NewInMemoryLongTermMemory()（内存+关键词检索，
// 开箱即用）或自行实现 memory.LongTermMemory 注入（如文件/数据库/向量库存储）。
func WithLongTermMemory(m memory.LongTermMemory) Option {
	return func(s *Session) { s.longTerm = m }
}

// WithInitialHistory 预加载会话历史（服务端持久化场景用）。
//
// 场景：agent-service 等宿主把上次会话的消息从数据库取出后，需要先
// 灌回记忆窗口，模型才能在"完整上下文"上继续对话——否则每次新建
// Session 都是失忆重启。
//
// 实现：逐条 Add 进短期记忆。若历史超过窗口上限，CondensingMemory 会
// 自动滚动把最旧消息移入待压缩区（与实时对话窗口行为一致）。
func WithInitialHistory(msgs []schema.Message) Option {
	return func(s *Session) {
		for _, m := range msgs {
			s.mem.Add(m)
		}
	}
}

// WithMemoryCondenser 启用上下文压缩（context condensation，对应 memory 包
// 的"摘要压缩留待后续优化"待办）：窗口超限时，用 fn 把最旧的旧消息压成一条
// system 摘要保留在上下文中，而不是直接丢弃——模型仍能感知早期对话梗概。
//
// fn 典型实现基于 LLM（见 agent-service 的 makeCondenser）；nil 表示不压缩
// （纯滑动窗口，默认行为）。注入时机：NewSession 先创建记忆，本 Option 在其
// 之后执行，待压缩消息会保留到首次 Run/RunStream 的 Condense 触发点。
func WithMemoryCondenser(fn func(ctx context.Context, dropped []schema.Message) (string, error)) Option {
	return func(s *Session) {
		if cm, ok := s.mem.(*memory.CondensingMemory); ok {
			cm.SetCondenser(fn)
		}
	}
}

// condenseContext 在持有 ctx 的时机尝试压缩超窗旧消息（context condensation）。
// 压缩成功时把结果信息收集到 condenseN（宿主经 DrainCondenseNotices 消费，
// 落库"哪个节点压缩过"的提示记录）。压缩失败不阻断对话：CondensingMemory
// 内部已退化为普通裁剪（直接丢弃）。
func (s *Session) condenseContext(ctx context.Context) {
	c, ok := s.mem.(memory.Condensable)
	if !ok {
		return
	}
	if err := c.Condense(ctx); err != nil {
		return
	}
	if ci, ok := s.mem.(memory.CondenseInfoAware); ok {
		if info := ci.ConsumeLastCondense(); info != nil {
			s.condenseN = append(s.condenseN, *info)
		}
	}
}

// DrainCondenseNotices 取走本轮已发生的上下文压缩记录（消费式，取后清空）。
// 宿主（agent-service）在 Run/RunStream 结束后调用，把记录落库为 system
// 提示消息，供前端渲染"已压缩上下文"提示条（历史回看时仍可见）。
func (s *Session) DrainCondenseNotices() []memory.CondenseInfo {
	out := s.condenseN
	s.condenseN = nil
	return out
}

// NewSession 创建会话并完成基础校验。
// 配置非法、provider/registry 为空时直接报错（尽早失败原则）。
func NewSession(cfg schema.AgentConfig, provider llm.Provider, reg *tool.Registry, opts ...Option) (*Session, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("agent: provider 不能为 nil")
	}
	if reg == nil {
		return nil, fmt.Errorf("agent: registry 不能为 nil")
	}

	// 默认记忆窗口：20 条，保护 1 条（system 指令所在位置）。
	// 用 CondensingMemory（摘要压缩型）：未注入压缩器时行为与纯滑动窗口一致；
	// 注入 WithMemoryCondenser 后超限旧消息会被压成摘要而非直接丢弃。
	maxMsg := cfg.Memory.MaxMessages
	if maxMsg <= 0 {
		maxMsg = 2000
	}
	mem, err := memory.NewCondensingMemory(maxMsg, 1, nil)
	if err != nil {
		return nil, fmt.Errorf("agent: 初始化记忆失败: %w", err)
	}

	s := &Session{
		config:   cfg,
		provider: provider,
		registry: reg,
		mem:      mem,
		longTerm: memory.NoopLongTermMemory{},
		pending:  make(map[string]chan *schema.ToolResult),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Result 一次 Run 的统计结果。
type Result struct {
	Content   string    // 最终回答文本
	Usage     llm.Usage // 累计 token 用量
	Rounds    int       // 消息循环轮数
	ToolCalls int       // 实际执行的工具调用次数
}

// History 返回当前会话的消息历史（含 system，便于调试/前端展示）。
func (s *Session) History() []schema.Message {
	return s.mem.Recent()
}
