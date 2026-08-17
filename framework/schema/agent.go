package schema

import (
	"fmt"
	"time"
)

// AgentConfig 一个 Agent 的完整配置：用什么模型、什么人格、
// 能调用哪些工具、怎么管理记忆。
//
// 配置外部化（JSON）意味着：同一个框架代码，只需改配置就能跑出
// 无数个不同人格/能力的 Agent（教学助手、出题老师、代码评审……），
// 这正是"通用框架"的核心理念——能力靠配置组合，不靠改代码。
type AgentConfig struct {
	// Model 使用的 LLM 模型名（如 deepseek-v4-flash / deepseek-v4-pro）。
	Model string `json:"model"`

	// SystemPrompt 系统指令：定义 Agent 身份、行为准则、输出风格。
	SystemPrompt string `json:"system_prompt"`

	// Tools 该 Agent 可用的工具集。空表示纯对话 Agent。
	Tools []ToolSchema `json:"tools,omitempty"`

	// MaxRounds 消息循环最大轮数：防止 Agent 陷入
	// "调工具→再调工具"的死循环，超过即强制停止。
	MaxRounds int `json:"max_rounds"`

	// MaxThinkingRounds 思考（工具调用）轮次上限：只统计"本轮调用了工具"的
	// 轮次，最终回答轮不计入。0 = 不单独限制（仅受 MaxRounds 总轮保护）。
	// 用途：深度思考模型可能"思考→调工具→再思考"循环不收敛，管理员可据此
	// 在总轮之外再设一道更紧的工具循环护栏（如 MaxRounds=24 总轮 +
	// MaxThinkingRounds=16 工具轮）。
	MaxThinkingRounds int `json:"max_thinking_rounds,omitempty"`

	// Memory 记忆策略（B4 阶段实现具体逻辑）。
	Memory MemoryConfig `json:"memory,omitempty"`

	// Thinking 思考模式配置（DeepSeek V4 思考模型）。
	// nil = 未配置，不干预（沿用厂商默认：思考开启）；
	// 非 nil = 显式控制：Enabled=false 时会下发 thinking.type=disabled，
	// 真正关闭思考（不能用零值表达"关闭"，零值会被当作"未配置"）。
	Thinking *ThinkingConfig `json:"thinking,omitempty"`

	// ExternalExecTimeout 外部代理工具（External=true）挂起等待结果的
	// 超时时间。超时后把超时原因作为工具结果回填给模型（不中断会话，
	// 模型可据此换策略或结束）。0 = 默认 120s。
	//
	// 保护价值：非流式 Chat 等无事件通知路径下，若外部执行端（桌面客户端）
	// 掉线或用户不响应，本超时保证会话不会无限期挂起。
	ExternalExecTimeout time.Duration `json:"-"`
}

// ThinkingConfig 思考模式配置：控制模型是否"先思考再回答"及其推理强度。
//
// 协议映射（DeepSeek V4，官方 OpenAI 兼容格式）：
//   - Enabled=true  → thinking: {"type":"enabled"}（默认行为）；
//   - Enabled=false → thinking: {"type":"disabled"}（直接回答，不产生 reasoning_content）；
//   - ReasoningEffort 非空 → 顶层 reasoning_effort 字段（high | max；
//     deepseek-v4-flash 额外支持 low；为空则厂商默认 high）。
type ThinkingConfig struct {
	// Enabled 思考开关。
	Enabled bool `json:"enabled"`
	// ReasoningEffort 推理强度：low | high | max。空 = 不发（厂商默认 high）。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// MemoryConfig 记忆策略配置。
type MemoryConfig struct {
	// MaxMessages 短期记忆窗口内保留的最大消息数。
	// 超过后丢弃最旧的对话，控制上下文 token 消耗。
	MaxMessages int `json:"max_messages"`

	// UseLongTerm 是否启用长期记忆（跨会话）。
	// P1 仅定义字段，P3 接入向量库后生效。
	UseLongTerm bool `json:"use_long_term"`
}

// Validate 校验配置合法性，返回第一个发现的错误。
// 配置错误应在创建 Agent 时尽早暴露，而不是运行到一半才崩溃。
func (c AgentConfig) Validate() error {
	if c.Model == "" {
		return fmt.Errorf("schema: AgentConfig.Model 不能为空")
	}
	if c.MaxRounds <= 0 {
		return fmt.Errorf("schema: AgentConfig.MaxRounds 必须大于 0")
	}
	return nil
}
