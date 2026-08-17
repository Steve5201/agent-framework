package llmsvc

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

// 协议转换：入站 OpenAI 兼容请求 → framework llm.Request。
// framework 的 llm.OpenAICompatible 是"客户端"，接受 llm.Request
// （内部再转回 OpenAI 协议发往厂商）；llm-gateway 作为"服务端"，
// 需要把入站请求先归一化为同一中间格式。

// toLLMRequest 校验并转换入站请求。
// 模型优先级：入站请求显式指定 > 服务端默认（model 参数）。
func toLLMRequest(req *chatCompletionRequest, model string) (*llm.Request, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages 不能为空")
	}
	msgs, err := toSchemaMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := toSchemaTools(req.Tools)
	if err != nil {
		return nil, err
	}
	m := model
	if req.Model != "" {
		m = req.Model
	}
	out := &llm.Request{
		Model:       m,
		Messages:    msgs,
		Tools:       tools,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
	}
	// 思考模式透传：入站带 thinking 开关或 reasoning_effort 才设置，
	// 未带则沿用上游厂商默认（思考开启）。
	if req.Thinking != nil || req.ReasoningEffort != "" {
		enabled := true
		if req.Thinking != nil {
			enabled = req.Thinking.Type == "enabled"
		}
		out.Thinking = &llm.ThinkingConfig{
			Enabled:         enabled,
			ReasoningEffort: req.ReasoningEffort,
		}
	}
	return out, nil
}

// toSchemaMessages 协议消息 → schema.Message。
func toSchemaMessages(msgs []openAIMessage) ([]schema.Message, error) {
	out := make([]schema.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "" {
			return nil, fmt.Errorf("消息缺少 role 字段")
		}
		sm := schema.Message{
			Role:       schema.Role(m.Role),
			Content:    m.Content,
			Reasoning:  m.ReasoningContent,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			sm.ToolCalls = append(sm.ToolCalls, schema.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			})
		}
		out = append(out, sm)
	}
	return out, nil
}

// toSchemaTools 协议工具声明 → schema.ToolSchema。
// 参数 JSON Schema 原样透传（framework 转发时同样原样序列化）。
func toSchemaTools(tools []openAITool) ([]schema.ToolSchema, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]schema.ToolSchema, 0, len(tools))
	for _, t := range tools {
		if t.Function.Name == "" {
			return nil, fmt.Errorf("工具缺少 name 字段")
		}
		params := t.Function.Parameters
		if len(params) == 0 {
			// 无参数声明给空对象占位，避免协议校验失败
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, schema.ToolSchema{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  params,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 出站：framework llm.Response / StreamEvent → OpenAI 兼容协议
// ---------------------------------------------------------------------------

// buildChatCompletionResponse 非流式：llm.Response → OpenAI 协议响应。
// 思考模型的思考内容随 message.reasoning_content 返回（与 content 同级）。
func buildChatCompletionResponse(id, model string, resp *llm.Response) chatCompletionResponse {
	msg := outMessage{Role: "assistant", Content: resp.Content, ReasoningContent: resp.Reasoning}
	for _, tc := range resp.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, outToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: tc.Name, Arguments: string(tc.Arguments)},
		})
	}
	finish := resp.FinishReason
	if finish == "" {
		finish = "stop"
	}
	return chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []outChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finish,
		}},
		Usage: outUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
}
