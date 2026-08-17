package llmsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/ratelimit"
	"github.com/Steve5201/agent-framework/llm"
	"go.uber.org/zap"
)

// maxRequestBody 入站请求体上限：1 MiB，防止超大请求耗尽内存。
const maxRequestBody = 1 << 20

// headerUserID 调用方用户 ID 请求头（gateway/agent 注入，P2-32）。
const headerUserID = "X-User-Id"

// headerUserRole 调用方角色请求头（gateway/agent 注入，q2 配额角色默认）。
// 值来自 gateway 对 JWT 的解析，llm-gateway 不信任客户端直传的角色。
const headerUserRole = "X-User-Role"

// headerAgentID 调用方智能体域请求头（agent 注入，P2-AI 用量按域聚合）。
// 非智能体入口（如直连调试）缺省为空串，落库 agent_id=” 不计入任何域。
const headerAgentID = "X-Agent-Id"

// usageMaxDays 用量聚合窗口上限（天），防误传超大值拖垮聚合查询。
const usageMaxDays = 90

// HandlerConfig llm-gateway handler 装配配置。
type HandlerConfig struct {
	Log                  *zap.Logger
	Provider             llm.Provider // 上游客户端（兼容单上游：OpenAICompatible 指向 DeepSeek）
	Registry             *Registry    // 模型注册表（P3 模型管理）；nil = 兼容单上游模式
	Usage                UsageStore   // 用量落库（PostgreSQL 或测试 fake）
	RequestRate          float64      // 每用户每秒请求速率（限流）
	RequestBurst         int          // 每用户突发容量
	TokenQuotaMonth      int64        // 普通用户每用户每月 token 配额（0=不限制）
	AdminTokenQuotaMonth int64        // 管理员每用户每月 token 配额（0=不限制）
	QuotaStore           QuotaStore   // 用户配额覆盖存储（user_quota 表）；nil = 无覆盖
	Model                string       // 默认模型（入站未指定时使用；兼容单上游模式）
	PromptPricePer1M     float64      // 输入单价（美元/百万 token）
	CompletionPricePer1M float64      // 输出单价（美元/百万 token）
}

// Handler OpenAI 兼容端点的 HTTP 处理器。
type Handler struct {
	log                  *zap.Logger
	provider             llm.Provider
	registry             *Registry
	usage                UsageStore
	rl                   *ratelimit.Store
	tokenQuota           int64
	adminTokenQuota      int64
	quotaStore           QuotaStore
	model                string
	promptPricePer1M     float64
	completionPricePer1M float64
}

// NewHandler 创建处理器。非法限流参数回退到安全默认值。
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.RequestRate <= 0 {
		cfg.RequestRate = 60
	}
	if cfg.RequestBurst <= 0 {
		cfg.RequestBurst = 20
	}
	return &Handler{
		log:                  cfg.Log,
		provider:             cfg.Provider,
		registry:             cfg.Registry,
		usage:                cfg.Usage,
		rl:                   ratelimit.NewStore(ratelimit.Config{Rate: cfg.RequestRate, Burst: cfg.RequestBurst}),
		tokenQuota:           cfg.TokenQuotaMonth,
		adminTokenQuota:      cfg.AdminTokenQuotaMonth,
		quotaStore:           cfg.QuotaStore,
		model:                cfg.Model,
		promptPricePer1M:     cfg.PromptPricePer1M,
		completionPricePer1M: cfg.CompletionPricePer1M,
	}
}

// Register 注册路由到 mux。
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", h.completions)
	mux.HandleFunc("GET /v1/usage/agents/{agent_id}", h.agentUsage)
}

// Usage 返回用量存储（供管理端用量端点注册复用同一实例）。
func (h *Handler) Usage() UsageStore { return h.usage }

// effectiveQuota 计算用户当月有效配额。
// 优先级：user_quota 表显式覆盖 > 角色默认（管理员用 adminTokenQuota，
// 普通用户用 tokenQuota）。返回 0 表示不限。覆盖查询失败时降级到角色默认，
// 仅记日志，不阻断主链路。
func (h *Handler) effectiveQuota(ctx context.Context, userID int64, role string) int64 {
	if h.quotaStore != nil {
		if quota, ok, err := h.quotaStore.Get(ctx, userID); err != nil {
			h.log.Error("查询用户配额覆盖失败，降级角色默认", zap.Error(err),
				zap.Int64("user_id", userID))
		} else if ok {
			return quota // 显式覆盖（0=不限）
		}
	}
	if isAdminRole(role) {
		return h.adminTokenQuota
	}
	return h.tokenQuota
}

// isAdminRole 判断角色是否属于管理员集合（与 authsvc.Role.IsAdmin 保持
// 一致；llm-gateway 不依赖 authsvc，仅按 X-User-Role 头判定）。
func isAdminRole(role string) bool {
	switch role {
	case "super_admin", "agent_admin", "admin":
		return true
	}
	return false
}

// completions OpenAI 兼容对话入口：解析 → 限流/配额 → 转发（流式/非流式）。
func (h *Handler) completions(w http.ResponseWriter, r *http.Request) {
	reqID := apperr.RequestIDFromContext(r.Context())

	// 1. 用户身份：X-User-Id（gateway/agent 注入）。缺失或非法 → 拒绝，
	//    避免匿名请求共享限流桶、无法归属用量。
	userID, err := parseUserID(r.Header.Get(headerUserID))
	if err != nil {
		writeError(w, reqID, apperr.New(apperr.CodeInvalidArgument, "缺少或非法的 "+headerUserID))
		return
	}

	// 2. 解析并校验请求体。
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, reqID, apperr.New(apperr.CodeInvalidArgument, "读取请求体失败"))
		return
	}
	var req chatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, reqID, apperr.New(apperr.CodeInvalidArgument, "请求体不是合法的 JSON"))
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, reqID, apperr.New(apperr.CodeInvalidArgument, "messages 不能为空"))
		return
	}

	// 2.1 解析模型 → 上游 provider + 计价（P3：按 model 名路由到多供应商；
	// 兼容单上游模式直接透传）。未知模型返回 400 并列出可用模型。
	model, provider, promptPrice, completionPrice, err := h.resolveModel(req.Model)
	if err != nil {
		writeError(w, reqID, err)
		return
	}

	// 3. 限流：请求速率（按用户独立桶）。
	if !h.rl.Allow(userKey(userID)) {
		writeError(w, reqID, apperr.New(apperr.CodeResourceExhausted, "请求过于频繁，请稍后再试"))
		return
	}
	// 配额：本月 token 累计（查询失败只记日志，不阻断主链路）。
	// 有效配额优先级：user_quota 表显式覆盖 > 角色默认（管理员用
	// AdminTokenQuotaMonth，普通用户用 TokenQuotaMonth）；0 = 不限制。
	if quota := h.effectiveQuota(r.Context(), userID, r.Header.Get(headerUserRole)); quota > 0 {
		used, err := h.usage.MonthTotalTokens(r.Context(), userID)
		if err != nil {
			h.log.Error("查询本月用量失败", zap.Error(err), zap.String("request_id", reqID))
		} else if used >= quota {
			writeError(w, reqID, apperr.New(apperr.CodeResourceExhausted, "本月 token 配额已用尽"))
			return
		}
	}

	// 4. 协议 → 框架中间格式。
	lreq, err := toLLMRequest(&req, model)
	if err != nil {
		writeError(w, reqID, apperr.New(apperr.CodeInvalidArgument, err.Error()))
		return
	}

	// 4.0 上游参数兼容（P4-I）：注册表标记 no_thinking 的模型（litellm
	// custom_openai / Ollama 等标准 OpenAI 端点）不接受 thinking /
	// reasoning_effort，转发前剥离，否则上游 400 UnsupportedParamsError。
	// 剥离后框架不会下发这两个字段，思考与否由上游自身行为决定。
	if h.registry != nil {
		if e := h.registry.Get(model); e != nil && e.Spec.NoThinking {
			lreq.Thinking = nil
		}
	}

	// 4.1 智能体域（X-Agent-Id，用量按域聚合；缺省空串不归属任何域）。
	agentID := r.Header.Get(headerAgentID)

	// 5. 分发。
	if req.Stream {
		h.stream(w, r, lreq, model, provider, promptPrice, completionPrice, userID, agentID, reqID)
		return
	}
	h.chat(w, r, lreq, model, provider, promptPrice, completionPrice, userID, agentID, reqID)
}

// resolveModel 按请求 model 名解析上游 provider 与计价参数，返回生效的模型名。
//   - 注册表模式（h.registry != nil）：空 model = 默认模型；按名取条目；
//     未知模型 / 条目上游客户端构造失败 → 明确业务错误；
//   - 兼容单上游模式：直接使用 h.provider，model 名原样透传（旧部署零改动）。
func (h *Handler) resolveModel(model string) (string, llm.Provider, float64, float64, error) {
	if h.registry != nil {
		entry := h.registry.Default()
		if model != "" {
			entry = h.registry.Get(model)
		}
		if entry == nil {
			names := h.registry.Names()
			if len(names) == 0 {
				return "", nil, 0, 0, apperr.New(apperr.CodeUnavailable,
					"模型注册表为空（可能全部被禁用），请联系管理员配置可用模型")
			}
			// 注册表只含启用模型：未知即"不存在或已禁用"。
			return "", nil, 0, 0, apperr.New(apperr.CodeInvalidArgument,
				"未知或已禁用的模型 "+model+"，可用模型："+strings.Join(names, "、"))
		}
		if entry.Err() != nil {
			return "", nil, 0, 0, apperr.New(apperr.CodeUnavailable,
				"模型 "+entry.Spec.Name+" 上游客户端构造失败，请检查接入参数（BaseURL/密钥等）")
		}
		return entry.Spec.Name, entry.Provider,
			entry.Spec.PromptPricePer1M, entry.Spec.CompletionPricePer1M, nil
	}
	if model == "" {
		model = h.model
	}
	return model, h.provider, h.promptPricePer1M, h.completionPricePer1M, nil
}

// ---------------------------------------------------------------------------
// 非流式（P2-31）
// ---------------------------------------------------------------------------

// chat 非流式转发：调用上游 → 用量落库 → 返回 OpenAI 兼容响应。
func (h *Handler) chat(w http.ResponseWriter, r *http.Request, lreq *llm.Request, model string, provider llm.Provider, promptPrice, completionPrice float64, userID int64, agentID, reqID string) {
	resp, err := provider.Chat(r.Context(), lreq)
	if err != nil {
		// 失败也落一条记录（token 未知记 0，status=失败）。
		h.log.Error("上游模型调用失败（非流式）", zap.String("request_id", reqID),
			zap.String("model", model), zap.Int64("user_id", userID), zap.Error(err))
		h.logUsage(&UsageLog{UserID: userID, AgentID: agentID, RequestID: reqID, Model: model, Stream: false, Success: false})
		writeError(w, reqID, mapUpstreamError(err))
		return
	}

	cost := CostUSD(resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		promptPrice, completionPrice)
	h.logUsage(&UsageLog{
		UserID: userID, AgentID: agentID, RequestID: reqID, Model: model,
		PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens: resp.Usage.TotalTokens, CostUSD: cost,
		Stream: false, Success: true,
	})

	out := buildChatCompletionResponse(chatID(reqID), model, resp)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		h.log.Error("写出响应失败", zap.Error(err), zap.String("request_id", reqID))
	}
}

// ---------------------------------------------------------------------------
// 流式 SSE（P2-32）
// ---------------------------------------------------------------------------

// stream 流式转发：上游 Stream 迭代器 → OpenAI SSE chunk 序列 → [DONE]。
func (h *Handler) stream(w http.ResponseWriter, r *http.Request, lreq *llm.Request, model string, provider llm.Provider, promptPrice, completionPrice float64, userID int64, agentID, reqID string) {
	st, err := provider.ChatStream(r.Context(), lreq)
	if err != nil {
		h.log.Error("上游模型调用失败（流式）", zap.String("request_id", reqID),
			zap.String("model", model), zap.Int64("user_id", userID), zap.Error(err))
		h.logUsage(&UsageLog{UserID: userID, AgentID: agentID, RequestID: reqID, Model: model, Stream: true, Success: false})
		writeError(w, reqID, mapUpstreamError(err))
		return
	}
	defer st.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, reqID, apperr.New(apperr.CodeInternal, "响应不支持流式推送"))
		return
	}

	// SSE 响应头（X-Accel-Buffering 关闭代理缓冲，保证逐块到达）。
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	chunkID, created := chatID(reqID), time.Now()

	// 首块：声明 assistant 角色（协议要求首个 delta 携带 role）。
	first := newStreamChunk(chunkID, model, created)
	first.Choices = []streamChoice{{Index: 0, Delta: streamDelta{Role: "assistant"}}}
	if err := writeSSE(w, first); err != nil {
		h.log.Error("写出 SSE 首块失败", zap.Error(err), zap.String("request_id", reqID))
		return
	}
	flusher.Flush()

	// 迭代上游事件并转发；累计用量（部分厂商在流末尾附带 usage）。
	// finishReason 记录上游最后一次 finish_reason：模型"为何停止"的语义
	// （stop/tool_calls/length）必须透传——改写为固定 stop 会切断
	// 依赖 finish_reason=tool_calls 触发工具执行的调用方链路。
	var prompt, completion, total int
	finishReason := ""
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// 流中断：落失败记录并终止（客户端以 [DONE] 收尾）。
			h.logUsage(&UsageLog{UserID: userID, AgentID: agentID, RequestID: reqID, Model: model, Stream: true, Success: false})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
			h.log.Error("上游流中断", zap.Error(err), zap.String("request_id", reqID))
			return
		}
		if ev.Usage != nil {
			prompt, completion, total = ev.Usage.PromptTokens, ev.Usage.CompletionTokens, ev.Usage.TotalTokens
		}
		if ev.FinishReason != "" {
			finishReason = ev.FinishReason
		}

		delta := streamDelta{}
		hasDelta := false
		if ev.Content != "" {
			delta.Content = ev.Content
			hasDelta = true
		}
		if ev.Reasoning != "" {
			delta.ReasoningContent = ev.Reasoning
			hasDelta = true
		}
		for _, tc := range ev.ToolCalls {
			delta.ToolCalls = append(delta.ToolCalls, streamToolDelta{
				Index: tc.Index,
				ID:    tc.ID,
				Function: struct {
					Name      string `json:"name,omitempty"`
					Arguments string `json:"arguments,omitempty"`
				}{Name: tc.Name, Arguments: tc.Arguments},
			})
			hasDelta = true
		}
		if !hasDelta {
			continue // 纯 usage 块等空事件，跳过
		}

		c := newStreamChunk(chunkID, model, created)
		c.Choices = []streamChoice{{Index: 0, Delta: delta}}
		if err := writeSSE(w, c); err != nil {
			h.log.Error("写出 SSE 块失败", zap.Error(err), zap.String("request_id", reqID))
			return
		}
		flusher.Flush()
		if ev.Done {
			// 不在此 break：include_usage 已开启时，finish_reason 块之后
			// 通常还有 usage 块（choices 为空，不带 delta）。continue 继续
			// 读到 [DONE]（io.EOF），确保 usage 被累计并转发给调用方。
			continue
		}
	}

	// 收尾：finish_reason 块 → usage 块（上游附带用量时）→ [DONE]。
	// usage 块须在 [DONE] 前发出：客户端（framework sseStream）读到
	// [DONE] 即结束，且按协议 usage 块（choices 为空）紧随 finish_reason。
	end := newStreamChunk(chunkID, model, created)
	finish := finishReason
	if finish == "" {
		finish = "stop" // 上游未给结束原因时按协议默认 stop
	}
	end.Choices = []streamChoice{{Index: 0, Delta: streamDelta{}, FinishReason: &finish}}
	_ = writeSSE(w, end)
	if total > 0 {
		u := newStreamChunk(chunkID, model, created)
		u.Usage = &outUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
		_ = writeSSE(w, u)
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()

	// 成功落库。
	cost := CostUSD(prompt, completion, promptPrice, completionPrice)
	h.logUsage(&UsageLog{
		UserID: userID, AgentID: agentID, RequestID: reqID, Model: model,
		PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total,
		CostUSD: cost, Stream: true, Success: true,
	})
}

// ---------------------------------------------------------------------------
// 用量聚合（P2-AI：智能体管理模块"用量数字"）
// ---------------------------------------------------------------------------

// agentUsage GET /v1/usage/agents/{agent_id}?days=7
// 返回该智能体域最近 N 天成功调用聚合。days 缺省 7、范围 1..usageMaxDays。
// 鉴权依赖内网隔离（llm-gateway 仅监听内部端口）；gateway 侧鉴权后转发。
func (h *Handler) agentUsage(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		writeError(w, "", apperr.New(apperr.CodeInvalidArgument, "agent_id 不能为空"))
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 || d > usageMaxDays {
			writeError(w, "", apperr.New(apperr.CodeInvalidArgument, "days 需为 1..90 的整数"))
			return
		}
		days = d
	}
	u, err := h.usage.AgentTotals(r.Context(), agentID, days)
	if err != nil {
		h.log.Error("查询智能体用量失败", zap.Error(err), zap.String("agent_id", agentID))
		writeError(w, "", apperr.New(apperr.CodeInternal, "查询用量失败"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(usageView(u))
}

// usageView AgentUsage → JSON 视图。
func usageView(u *AgentUsage) map[string]any {
	out := map[string]any{
		"agent_id":          u.AgentID,
		"calls":             u.Calls,
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.TotalTokens,
		"cost_usd":          u.CostUSD,
	}
	if !u.LastUsedAt.IsZero() {
		out["last_used_at"] = u.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// logUsage 写入用量记录；落库失败只记日志，不阻塞主链路。
// 使用独立超时 context：请求可能已结束/取消，落库不能被请求生命周期拖累。
func (h *Handler) logUsage(log *UsageLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.usage.LogUsage(ctx, log); err != nil {
		h.log.Error("写入 usage_logs 失败", zap.Error(err), zap.String("request_id", log.RequestID))
	}
}

// writeError 输出统一错误体 {code, message, request_id}（P2-35）。
func writeError(w http.ResponseWriter, reqID string, err error) {
	appErr := toAppErr(err)
	if reqID != "" {
		appErr = appErr.WithRequestID(reqID)
	}
	status, body := apperr.HTTPBody(appErr)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// toAppErr 确保拿到 *apperr.Error（统一错误结构），非该类型时包装。
func toAppErr(err error) *apperr.Error {
	if e, ok := err.(*apperr.Error); ok {
		return e
	}
	return apperr.Wrap(apperr.CodeOf(err), err.Error(), err)
}

// mapUpstreamError 把上游调用错误映射为统一错误 + 合适 HTTP 状态（P2-35）：
//   - 上游 429 → RESOURCE_EXHAUSTED(429)
//   - 上游 401/403 → PERMISSION_DENIED(403)
//   - 上游其它 4xx → INVALID_ARGUMENT(400)
//   - 上游 5xx → UNAVAILABLE(503)
//   - 超时 → DEADLINE_EXCEEDED(504)
//   - 其它 → INTERNAL(500)
//
// 4xx 分支会把上游原始错误体里可读的失败原因（DeepSeek 的 {"error":{"message":…}}）
// 拼进返回文案，避免把真实拒绝原因截断在转换层（曾导致"未知错误"无从排查）。
func mapUpstreamError(err error) error {
	var he *llm.HTTPStatusError
	if errors.As(err, &he) {
		switch {
		case he.Status == http.StatusTooManyRequests:
			return apperr.New(apperr.CodeResourceExhausted, "上游模型服务限流，请稍后再试")
		case he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden:
			return apperr.New(apperr.CodePermissionDenied, "上游模型服务拒绝访问，请检查模型服务密钥")
		case he.Status >= http.StatusInternalServerError:
			return apperr.New(apperr.CodeUnavailable, "上游模型服务暂时不可用")
		default:
			msg := fmt.Sprintf("上游模型服务返回错误（HTTP %d）", he.Status)
			if detail := upstreamDetail(he.Body); detail != "" {
				msg += ": " + detail
			}
			return apperr.New(apperr.CodeInvalidArgument, msg)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.New(apperr.CodeDeadlineExceeded, "上游模型服务响应超时")
	}
	return apperr.Wrap(apperr.CodeInternal, "调用上游模型服务失败", err)
}

// upstreamDetail 从上游错误体提取可读的失败原因：优先解析 OpenAI error JSON
// 的 message 字段；非标准体（HTML/网关包装）则截断原始文本。空体返回空串。
func upstreamDetail(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	if len(s) > 200 {
		s = s[:200] + "…(截断)"
	}
	return s
}

// writeSSE 写一条 SSE data 事件。
func writeSSE(w http.ResponseWriter, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

// parseUserID 解析 X-User-Id。
// 合法取值：真实用户 > 0；游客（阶段2·游客模式）为 auth.GuestUserID 派生的
// 负整数——负值空间专供游客，各自独立限流桶与用量归属；仅 0 非法。
func parseUserID(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty user id")
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid user id %q", s)
	}
	return id, nil
}

// userKey 限流桶 key（按用户隔离）。
func userKey(userID int64) string {
	return "user:" + strconv.FormatInt(userID, 10)
}

// chatID 生成响应 ID：复用 request_id 便于全链路追踪。
func chatID(reqID string) string {
	if reqID == "" {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + reqID
}
