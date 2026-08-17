// agent.go —— 会话与对话 HTTP handlers（P2-50/P2-54）。
//
//   - 会话 CRUD：创建/列表/详情/删除（透传 agent-service，user_id 由
//     gateway 从 JWT 解析并经 gRPC metadata 注入）；
//   - Chat：非流式问答，一次性返回完整回答；
//   - StreamChat：SSE 透传（打字机效果）——agent 的 gRPC 流逐事件转 SSE，
//     含心跳保活与客户端断连清理。
package gatewaysvc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	"go.uber.org/zap"
)

// sseKeepaliveInterval 心跳间隔：一段时间无事件时发注释行，防止
// 中间代理（Nginx 等）因"无数据"超时掐断长连接。
const sseKeepaliveInterval = 15 * time.Second

// ---------------------------------------------------------------------------
// 会话 CRUD
// ---------------------------------------------------------------------------

// CreateSession POST /v1/agent/sessions。
func (c *Clients) CreateSession(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req agentv1.CreateSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.CreateSession(userCtx(r, userID), &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c.Log.Info("session created",
		zap.String("session_id", resp.Session.GetId()),
		zap.String("agent_id", resp.Session.GetAgentId()),
	)
	writeJSON(w, http.StatusCreated, map[string]any{"session": sessionBody(resp.Session)})
}

// ListSessions GET /v1/agent/sessions?page=1&page_size=20&agent_id=tutor。
// agent_id 缺省为空串（管理端域）；传 "*" 列出全部域。
func (c *Clients) ListSessions(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	req := &agentv1.ListSessionsRequest{
		Page:     atoiOr(q.Get("page"), 1),
		PageSize: atoiOr(q.Get("page_size"), 20),
		AgentId:  q.Get("agent_id"),
	}
	resp, err := c.Agent.ListSessions(userCtx(r, userID), req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	list := make([]map[string]any, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		list = append(list, sessionBody(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list, "total": resp.Total})
}

// GetSession GET /v1/agent/sessions/{id}。
func (c *Clients) GetSession(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.GetSession(userCtx(r, userID), &agentv1.GetSessionRequest{SessionId: sessionID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sessionBody(resp.Session)})
}

// ListSessionMessages GET /v1/agent/sessions/{id}/messages。
// 返回会话全部消息（seq 升序），供前端历史回看与上下文恢复。
func (c *Clients) ListSessionMessages(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.ListMessages(userCtx(r, userID), &agentv1.ListMessagesRequest{SessionId: sessionID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	list := make([]map[string]any, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		list = append(list, map[string]any{
			"id":             m.GetId(), // 数据库主键，删除/定位用
			"role":           m.GetRole(),
			"content":        m.GetContent(),
			"reasoning":      m.GetReasoning(), // assistant 思考内容（DeepSeek reasoning_content）
			"tool_call_id":   m.GetToolCallId(),
			"tool_calls":     m.GetToolCalls(),     // JSON 字符串；空串=无
			"round_no":       m.GetRoundNo(),       // 轮次序号（重生成/分支/版本切换定位用）
			"version":        m.GetVersion(),       // 当前版本号
			"total_versions": m.GetTotalVersions(), // 该轮版本总数（切换 UI 用）
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": list})
}

// DeleteSession DELETE /v1/agent/sessions/{id}。
func (c *Clients) DeleteSession(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := c.Agent.DeleteSession(userCtx(r, userID), &agentv1.DeleteSessionRequest{SessionId: sessionID}); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteMessage 删除会话内单条消息（DELETE /v1/agent/sessions/{id}/messages/{mid}）。
// 属主校验在下游 agent-service；删除后该消息不再出现在历史加载中。
func (c *Clients) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	messageID, err := pathMessageID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := c.Agent.DeleteMessage(userCtx(r, userID), &agentv1.DeleteMessageRequest{
		SessionId: sessionID,
		MessageId: messageID,
	}); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RenameSession 更新会话（PATCH /v1/agent/sessions/{id}）。
// 请求体二选一：{"title": "..."} 重命名；{"config": {...}} 更新配置
// （工具权限/思考模式）；两者可同时给出，各自独立应用。
func (c *Clients) RenameSession(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body struct {
		Title  string                 `json:"title"`
		Config *agentv1.SessionConfig `json:"config"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.Title == "" && body.Config == nil {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "至少提供 title 或 config 之一"))
		return
	}
	var sess *agentv1.Session
	if body.Title != "" {
		resp, err := c.Agent.RenameSession(userCtx(r, userID), &agentv1.RenameSessionRequest{
			SessionId: sessionID,
			Title:     body.Title,
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
		sess = resp.Session
	}
	if body.Config != nil {
		resp, err := c.Agent.UpdateSessionConfig(userCtx(r, userID), &agentv1.UpdateSessionConfigRequest{
			SessionId: sessionID,
			Config:    body.Config,
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
		sess = resp.Session
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sessionBody(sess)})
}

// ListTools GET /v1/agent/tools：列出当前默认可用工具集（名称+描述），
// 供前端渲染"工具权限"配置开关。
func (c *Clients) ListTools(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.ListTools(userCtx(r, userID), &agentv1.ListToolsRequest{
		AgentId: r.URL.Query().Get("agent_id"), // 缺省空 = 本实例域
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	list := make([]map[string]any, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		list = append(list, map[string]any{
			"name":        t.GetName(),
			"description": t.GetDescription(),
			"external":    t.GetExternal(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": list})
}

// ListResources GET /v1/agent/resources：列出普通用户可见的资源清单
// （能力 + 技能）。阶段1·权限分层：只返回 id/名称/说明，不含工具名与技能代码，
// 用户据此按"能力/技能"勾选启用，不接触底层工具。
func (c *Clients) ListResources(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.ListResources(userCtx(r, userID), &agentv1.ListResourcesRequest{
		AgentId: r.URL.Query().Get("agent_id"), // 缺省空 = 本实例域
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	list := make([]map[string]any, 0, len(resp.Resources))
	for _, res := range resp.Resources {
		list = append(list, map[string]any{
			"id":          res.GetId(),
			"name":        res.GetName(),
			"description": res.GetDescription(),
			"type":        res.GetType(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": list})
}

// Regenerate 重新生成某轮回答（POST /v1/agent/sessions/{id}/messages/{mid}/regenerate）。
func (c *Clients) Regenerate(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	messageID, err := pathMessageID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.Regenerate(userCtx(r, userID), &agentv1.RegenerateRequest{
		SessionId: sessionID,
		MessageId: messageID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"content":      resp.Content,
		"rounds":       resp.Rounds,
		"tool_calls":   resp.ToolCalls,
		"total_tokens": resp.TotalTokens,
		"version":      resp.Version,
	})
}

// SetActiveVersion 切换某轮活跃版本（POST /v1/agent/sessions/{id}/messages/{mid}/version，{"version": n}）。
func (c *Clients) SetActiveVersion(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	messageID, err := pathMessageID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body struct {
		Version int32 `json:"version"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := c.Agent.SetActiveVersion(userCtx(r, userID), &agentv1.SetActiveVersionRequest{
		SessionId: sessionID,
		MessageId: messageID,
		Version:   body.Version,
	}); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateBranch 基于当前上下文创建分支会话（POST /v1/agent/sessions/{id}/messages/{mid}/branch）。
func (c *Clients) CreateBranch(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	messageID, err := pathMessageID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.CreateBranch(userCtx(r, userID), &agentv1.CreateBranchRequest{
		SessionId: sessionID,
		MessageId: messageID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": sessionBody(resp.Session)})
}

// ---------------------------------------------------------------------------
// 对话
// ---------------------------------------------------------------------------

// Chat POST /v1/agent/sessions/{id}/chat（非流式）。
func (c *Clients) Chat(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Agent.Chat(userCtx(r, userID), &agentv1.ChatRequest{
		SessionId: sessionID,
		Content:   body.Content,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"content":           resp.Content,
		"rounds":            resp.Rounds,
		"tool_calls":        resp.ToolCalls,
		"prompt_tokens":     resp.PromptTokens,
		"completion_tokens": resp.CompletionTokens,
		"total_tokens":      resp.TotalTokens,
	})
}

// StreamChat POST /v1/agent/sessions/{id}/chat/stream（SSE 流式）。
//
// 事件格式（data 均为 JSON 行）：
//
//	回答增量:  data: {"type":"delta","content":"你"}
//	思考增量:  data: {"type":"reasoning","content":"我先想想..."}
//	工具调用:  data: {"type":"tool_call","name":"calculator","arguments":"{...}"}
//	工具返回:  data: {"type":"tool_result","name":"calculator","content":"2"}
//	结束统计: event: done
//	           data: {"type":"done","rounds":1,"tool_calls":0,"total_tokens":10}
//	流中错误: event: error
//	           data: {"message":"..."}
//
// 心跳：每 15s 发注释行 ": keepalive"（对端可按注释行判定连接存活）。
//
// 说明：一个 Delta 可能同时携带 content 与 reasoning，此处拆成两条
// SSE 事件分别下发，前端可独立累积（思考气泡 vs 回答正文）。
func (c *Clients) StreamChat(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeInternal, "当前环境不支持流式输出"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 关掉 Nginx 缓冲，让增量即时到达
	w.WriteHeader(http.StatusOK)

	stream, err := c.Agent.StreamChat(userCtx(r, userID), &agentv1.StreamChatRequest{
		SessionId: sessionID,
		Content:   body.Content,
	})
	if err != nil {
		// 流尚未建立即失败：以事件形式告知（SSE 头已发出，无法改状态码）。
		_ = writeSSE(w, flusher, "error", map[string]any{"message": err.Error()})
		return
	}

	// 后台 goroutine 读 gRPC 流 → 事件通道；主循环 select 分发给 SSE。
	events := make(chan *agentv1.StreamChatEvent)
	errCh := make(chan error, 1)
	go func() {
		for {
			ev, err := stream.Recv()
			if err == io.EOF {
				close(events)
				return
			}
			if err != nil {
				errCh <- err
				return
			}
			events <- ev
		}
	}()

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// 正常结束（agent 已发 Done 事件），收尾。
				flusher.Flush()
				return
			}
			switch e := ev.Event.(type) {
			case *agentv1.StreamChatEvent_Delta:
				// 回答增量与思考增量可能同框到达，拆成独立 SSE 事件下发。
				if e.Delta.Content != "" {
					if err := writeSSE(w, flusher, "", map[string]any{
						"type":    "delta",
						"content": e.Delta.Content,
					}); err != nil {
						return // 客户端已断开
					}
				}
				if e.Delta.Reasoning != "" {
					if err := writeSSE(w, flusher, "", map[string]any{
						"type":    "reasoning",
						"content": e.Delta.Reasoning,
					}); err != nil {
						return
					}
				}
			case *agentv1.StreamChatEvent_ToolCall:
				if err := writeSSE(w, flusher, "", map[string]any{
					"type":         "tool_call",
					"tool_call_id": e.ToolCall.ToolCallId,
					"name":         e.ToolCall.Name,
					"arguments":    e.ToolCall.Arguments,
				}); err != nil {
					return
				}
			case *agentv1.StreamChatEvent_ToolResult:
				if err := writeSSE(w, flusher, "", map[string]any{
					"type":         "tool_result",
					"tool_call_id": e.ToolResult.ToolCallId,
					"name":         e.ToolResult.Name,
					"content":      e.ToolResult.Content,
					"error":        e.ToolResult.Error,
				}); err != nil {
					return
				}
			case *agentv1.StreamChatEvent_TaskStatus:
				// 多智能体编排进度事件（mode=orchestrate）：子任务开始/结束、整体完成/失败。
				// task_content 类型时 content 携带子任务输出增量（前端节点气泡打字机渲染），
				// kind 区分增量类型（text|reasoning|tool_start|tool_end）。
				if err := writeSSE(w, flusher, "", map[string]any{
					"type":         "task_status",
					"task_type":    e.TaskStatus.Type,
					"task_id":      e.TaskStatus.TaskId,
					"status":       e.TaskStatus.Status,
					"error":        e.TaskStatus.Error,
					"content":      e.TaskStatus.Content,
					"kind":         e.TaskStatus.Kind,
					"total_tokens": e.TaskStatus.TotalTokens,
				}); err != nil {
					return
				}
			case *agentv1.StreamChatEvent_Done:
				if e.Done.Error != "" {
					_ = writeSSE(w, flusher, "error", map[string]any{"message": e.Done.Error})
					return
				}
				_ = writeSSE(w, flusher, "done", map[string]any{
					"type":              "done",
					"rounds":            e.Done.Rounds,
					"tool_calls":        e.Done.ToolCalls,
					"prompt_tokens":     e.Done.PromptTokens,
					"completion_tokens": e.Done.CompletionTokens,
					"total_tokens":      e.Done.TotalTokens,
				})
				flusher.Flush()
				return
			}
		case err := <-errCh:
			_ = writeSSE(w, flusher, "error", map[string]any{"message": err.Error()})
			return
		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			// 客户端断开：取消 gRPC 流，结束（无需清理，连接随 ctx 回收）。
			return
		}
	}
}

// StreamRegenerate POST /v1/agent/sessions/{id}/messages/{mid}/regenerate-stream
// （SSE 流式重新生成）。
//
// 事件格式与 StreamChat 完全一致（delta/reasoning/tool_call/tool_result/
// task_status/done/error + 15s keepalive）。前端重生成按钮改调本接口，
// 恢复打字机/思考/工具/编排进度的流式渲染效果。
func (c *Clients) StreamRegenerate(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	messageID, err := pathMessageID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeInternal, "当前环境不支持流式输出"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 关掉 Nginx 缓冲，让增量即时到达
	w.WriteHeader(http.StatusOK)

	stream, err := c.Agent.StreamRegenerate(userCtx(r, userID), &agentv1.RegenerateRequest{
		SessionId: sessionID,
		MessageId: messageID,
	})
	if err != nil {
		// 流尚未建立即失败：以事件形式告知（SSE 头已发出，无法改状态码）。
		_ = writeSSE(w, flusher, "error", map[string]any{"message": err.Error()})
		return
	}

	// 后台 goroutine 读 gRPC 流 → 事件通道；主循环 select 分发给 SSE。
	events := make(chan *agentv1.StreamChatEvent)
	errCh := make(chan error, 1)
	go func() {
		for {
			ev, err := stream.Recv()
			if err == io.EOF {
				close(events)
				return
			}
			if err != nil {
				errCh <- err
				return
			}
			events <- ev
		}
	}()

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// 正常结束（agent 已发 Done 事件），收尾。
				flusher.Flush()
				return
			}
			switch e := ev.Event.(type) {
			case *agentv1.StreamChatEvent_Delta:
				if e.Delta.Content != "" {
					if err := writeSSE(w, flusher, "", map[string]any{
						"type":    "delta",
						"content": e.Delta.Content,
					}); err != nil {
						return
					}
				}
				if e.Delta.Reasoning != "" {
					if err := writeSSE(w, flusher, "", map[string]any{
						"type":    "reasoning",
						"content": e.Delta.Reasoning,
					}); err != nil {
						return
					}
				}
			case *agentv1.StreamChatEvent_ToolCall:
				if err := writeSSE(w, flusher, "", map[string]any{
					"type":         "tool_call",
					"tool_call_id": e.ToolCall.ToolCallId,
					"name":         e.ToolCall.Name,
					"arguments":    e.ToolCall.Arguments,
				}); err != nil {
					return
				}
			case *agentv1.StreamChatEvent_ToolResult:
				if err := writeSSE(w, flusher, "", map[string]any{
					"type":         "tool_result",
					"tool_call_id": e.ToolResult.ToolCallId,
					"name":         e.ToolResult.Name,
					"content":      e.ToolResult.Content,
					"error":        e.ToolResult.Error,
				}); err != nil {
					return
				}
			case *agentv1.StreamChatEvent_TaskStatus:
				if err := writeSSE(w, flusher, "", map[string]any{
					"type":         "task_status",
					"task_type":    e.TaskStatus.Type,
					"task_id":      e.TaskStatus.TaskId,
					"status":       e.TaskStatus.Status,
					"error":        e.TaskStatus.Error,
					"content":      e.TaskStatus.Content,
					"total_tokens": e.TaskStatus.TotalTokens,
				}); err != nil {
					return
				}
			case *agentv1.StreamChatEvent_Done:
				if e.Done.Error != "" {
					_ = writeSSE(w, flusher, "error", map[string]any{"message": e.Done.Error})
					return
				}
				_ = writeSSE(w, flusher, "done", map[string]any{
					"type":              "done",
					"rounds":            e.Done.Rounds,
					"tool_calls":        e.Done.ToolCalls,
					"prompt_tokens":     e.Done.PromptTokens,
					"completion_tokens": e.Done.CompletionTokens,
					"total_tokens":      e.Done.TotalTokens,
				})
				flusher.Flush()
				return
			}
		case err := <-errCh:
			_ = writeSSE(w, flusher, "error", map[string]any{"message": err.Error()})
			return
		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// SubmitToolResult POST /v1/agent/sessions/{id}/tool-results（阶段3·本地工具代理）。
//
// 桌面客户端完成本地工具执行后回填结果，唤醒 agent-service 中挂起等待的
// 会话继续推理。请求体：{"tool_call_id":"...","content":"...","is_error":false}。
// tool_call_id 来自 SSE 的 tool_call 事件（当前仅桌面端本地工具需要此通道）。
func (c *Clients) SubmitToolResult(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body struct {
		ToolCallID string `json:"tool_call_id"`
		Content    string `json:"content"`
		IsError    bool   `json:"is_error"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.ToolCallID == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少工具调用标识 tool_call_id"))
		return
	}
	if _, err := c.Agent.SubmitToolResult(userCtx(r, userID), &agentv1.SubmitToolResultRequest{
		SessionId:  sessionID,
		ToolCallId: body.ToolCallID,
		Content:    body.Content,
		IsError:    body.IsError,
	}); err != nil {
		writeError(w, r, err)
		return
	}
	c.Log.Info("external tool result submitted",
		zap.String("session_id", sessionID),
		zap.String("tool_call_id", body.ToolCallID),
		zap.Bool("is_error", body.IsError),
	)
	w.WriteHeader(http.StatusNoContent)
}

// MergeGuestSessions POST /v1/agent/sessions/merge-guest。
// 登录成功后前端携带原游客 ID，把游客阶段创建的会话合并到当前账号。
// 仅真实用户可调用；游客身份（负 user_id）调用会被下游拒绝。
func (c *Clients) MergeGuestSessions(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body struct {
		GuestID string `json:"guest_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.GuestID == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少游客 ID guest_id"))
		return
	}
	resp, err := c.Agent.MergeGuestSessions(userCtx(r, userID), &agentv1.MergeGuestSessionsRequest{GuestId: body.GuestID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	c.Log.Info("guest sessions merged",
		zap.Int64("user_id", userID),
		zap.Int("migrated", int(resp.Migrated)),
	)
	writeJSON(w, http.StatusOK, map[string]any{"migrated": resp.Migrated})
}

// UploadChatDocument POST /v1/agent/sessions/{id}/documents（模块二·聊天上传文档）。
//
// 请求为 multipart/form-data，字段 file = 用户上传的文档（≤20MB，类型白名单
// md/txt/html/xlsx/pdf/docx/pptx）。gateway 解出文件后透传 agent-service：
// 解析复用 rag ingest 管线，全文落盘用户工作区并注入一条限长 user 消息进
// 会话历史，后续轮次可持续追问。属主校验在 agent-service 侧完成。
func (c *Clients) UploadChatDocument(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sessionID, err := pathSessionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// 请求体上限：文档上限 + multipart 头部余量（防超大请求耗尽内存）。
	docMax := c.chatDocMaxBytes()
	r.Body = http.MaxBytesReader(w, r.Body, docMax+1<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, apperr.Wrap(apperr.CodeInvalidArgument, "缺少文件字段 file（multipart/form-data）", err))
		return
	}
	defer file.Close()
	if header.Size > docMax {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("文档大小超出上限（≤%dMB）", docMax>>20)))
		return
	}
	content, err := io.ReadAll(file)
	if err != nil {
		writeError(w, r, apperr.Wrap(apperr.CodeInternal, "读取上传文件失败", err))
		return
	}
	resp, err := c.Agent.UploadChatDocument(userCtx(r, userID), &agentv1.UploadChatDocumentRequest{
		SessionId: sessionID,
		FileName:  header.Filename,
		Content:   content,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	c.Log.Info("chat document uploaded",
		zap.Int64("user_id", userID),
		zap.String("session_id", sessionID),
		zap.String("file", resp.GetFileName()),
		zap.Int32("segments", resp.GetSegments()),
	)
	writeJSON(w, http.StatusCreated, map[string]any{
		"file_name":    resp.GetFileName(),
		"rel_path":     resp.GetRelPath(),
		"segments":     resp.GetSegments(),
		"injected_len": resp.GetInjectedLen(),
		"media":        resp.GetMedia(),
		"warnings":     resp.GetWarnings(),
	})
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// writeSSE 写一条 SSE 事件并 Flush。返回写错误（写失败 = 连接已断开）。
func writeSSE(w io.Writer, flusher http.Flusher, event string, data any) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// pathSessionID 从路径参数 {id} 解析会话 ID。
func pathSessionID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if id == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "缺少会话 ID")
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil || id == "0" {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的会话 ID")
	}
	return id, nil
}

// pathMessageID 提取 URL 路径中的消息 ID 参数（{mid}）。
func pathMessageID(r *http.Request) (string, error) {
	id := r.PathValue("mid")
	if id == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "缺少消息 ID")
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil || id == "0" {
		return "", apperr.New(apperr.CodeInvalidArgument, "非法的消息 ID")
	}
	return id, nil
}

// atoiOr 解析查询参数，非法/缺失时回退默认值。
func atoiOr(s string, def int) int32 {
	if s == "" {
		return int32(def)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return int32(def)
	}
	return int32(n)
}

// sessionBody proto Session → HTTP JSON（时间统一 RFC3339，配置转对象）。
func sessionBody(s *agentv1.Session) map[string]any {
	return map[string]any{
		"id":         s.GetId(),
		"user_id":    s.GetUserId(),
		"title":      s.GetTitle(),
		"created_at": s.GetCreatedAt().AsTime().Format(time.RFC3339),
		"updated_at": s.GetUpdatedAt().AsTime().Format(time.RFC3339),
		"config":     sessionConfigBody(s.GetConfig()),
		"agent_id":   s.GetAgentId(), // 会话所属智能体域（'' = 管理端域）
	}
}

// sessionConfigBody proto SessionConfig → HTTP JSON 对象。
// 空配置返回空对象 {}（前端语义：全部工具启用 + 思考按厂商默认）。
func sessionConfigBody(c *agentv1.SessionConfig) map[string]any {
	out := map[string]any{}
	if c == nil {
		return out
	}
	if len(c.EnabledTools) > 0 {
		out["enabled_tools"] = c.EnabledTools
	}
	// 显式清空的资源选择也要回传：set 标记 + 空数组 = "不启用任何能力/技能"，
	// 前端据此区分"未设置（全部启用）"与"显式全不选"。
	if len(c.EnabledResources) > 0 || c.EnabledResourcesSet {
		if c.EnabledResources == nil {
			out["enabled_resources"] = []string{}
		} else {
			out["enabled_resources"] = c.EnabledResources
		}
		if c.EnabledResourcesSet {
			out["enabled_resources_set"] = true
		}
	}
	// 能力/技能独立 presence 标记（P3 反馈）：透传给前端配置区，
	// 前端据此恢复各类别"显式全不选"勾选状态。
	if c.EnabledCapabilitiesSet {
		out["enabled_capabilities_set"] = true
	}
	if c.EnabledSkillsSet {
		out["enabled_skills_set"] = true
	}
	if c.Thinking != nil {
		t := map[string]any{"enabled": c.Thinking.Enabled}
		if c.Thinking.ReasoningEffort != "" {
			t["reasoning_effort"] = c.Thinking.ReasoningEffort
		}
		out["thinking"] = t
	}
	if len(c.KbIds) > 0 {
		out["kb_ids"] = c.KbIds
	}
	if c.KbIdsSet {
		// 显式设置过知识库（含清空）的标记：前端据此保留"本会话不使用知识库"，
		// 否则空数组回传缺失会被默认配置覆盖。
		out["kb_ids_set"] = true
	}
	// MCP 选择：显式清空（set 标记）同样要回传，空数组 + set = "本会话不装配
	// 任何 MCP 工具"，与"未设置（全部启用）"语义不同，前端据此渲染勾选态。
	if len(c.McpServers) > 0 || c.McpServersSet {
		if c.McpServers == nil {
			out["mcp_servers"] = []string{}
		} else {
			out["mcp_servers"] = c.McpServers
		}
		if c.McpServersSet {
			out["mcp_servers_set"] = true
		}
	}
	// 管理员级字段（快照固化）：透传给前端展示（普通用户配置区只读/不展示）。
	if c.MaxRounds > 0 {
		out["max_rounds"] = c.MaxRounds
	}
	if c.MaxMessages > 0 {
		out["max_messages"] = c.MaxMessages
	}
	if c.MaxThinkingRounds > 0 {
		out["max_thinking_rounds"] = c.MaxThinkingRounds
	}
	// 会话选定的模型名（普通可配字段，配置区选择；空 = 回退默认）。
	if c.Model != "" {
		out["model"] = c.Model
	}
	// 会话运行模式（single | orchestrate）；空 = single。编排会话前端据此
	// 渲染子任务进度轨迹，普通会话无需关心。
	if c.Mode != "" {
		out["mode"] = c.Mode
	}
	// 编排方案（fixed | dynamic）；空 = fixed（向后兼容）。与 mode 配套回传，
	// 前端 ModeDialog / 历史会话据此回显编排方案选择。
	if c.OrchestratePlan != "" {
		out["orchestrate_plan"] = c.OrchestratePlan
	}
	return out
}
