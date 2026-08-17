// Package llmsvc 实现 llm-gateway 业务域：
// OpenAI 兼容端点 /v1/chat/completions 转发（非流式 + SSE 流式）、
// 用量统计（usage_logs 落库）、按用户限流与配额。
//
// 调用链（P2 架构决策）：
//
//	agent-service ──HTTP(OpenAI 协议)──▶ llm-gateway ──framework llm.Provider──▶ DeepSeek
//	                                        │
//	                                        ├─ 每请求写 usage_logs（P2-33）
//	                                        ├─ 请求速率限流 + token 月配额（P2-34）
//	                                        └─ 上游错误 → 统一错误 + HTTP 状态（P2-35）
//
// 设计要点：
//   - 服务端复用 framework 的 llm.OpenAICompatible 作为上游客户端
//     （重试 / SSE 解析 / 错误解码全部复用，零新 Provider 代码）；
//   - 真实 API Key 只存在于本服务（llm-gateway），agent-service 与前端不可见；
//   - user_id 经请求头 X-User-Id 传入（由调用方 gateway/agent 注入），
//     用于限流与用量归属。
package llmsvc

import "time"

// UsageLog 一次模型调用的用量记录（对应 usage_logs 表，P2-33）。
type UsageLog struct {
	UserID           int64   // 调用方用户（跨库，无外键）
	AgentID          string  // 调用方智能体域（X-Agent-Id 注入；空 = 非智能体入口）
	RequestID        string  // 全链路 request_id
	Model            string  // 实际使用的模型名
	PromptTokens     int     // 输入 token
	CompletionTokens int     // 输出 token
	TotalTokens      int     // 合计
	CostUSD          float64 // 估算成本（美元）
	Stream           bool    // 是否流式请求
	Success          bool    // true=成功 false=失败
}

// AgentUsage 某智能体域在时间窗口内的聚合用量（按 agent_id 分组）。
type AgentUsage struct {
	AgentID          string
	Calls            int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostUSD          float64
	LastUsedAt       time.Time // 最近一次成功调用时间（无记录 = 零值）
}

// IsZero 是否无任何用量记录。
func (u *AgentUsage) IsZero() bool { return u.Calls == 0 }
