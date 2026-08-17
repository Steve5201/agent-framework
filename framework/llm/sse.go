package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// SSE 协议要点（Server-Sent Events）：
//
//	服务端持续推送以 "data:" 开头的文本行，事件间用空行分隔：
//
//	data: {"id":"...","choices":[{"delta":{"content":"你"},"finish_reason":null}]}
//	<空行>
//	data: {"id":"...","choices":[{"delta":{"content":"好"},"finish_reason":null}]}
//	<空行>
//	data: [DONE]
//
// 每行 data 之后的内容就是一段 JSON，逐个解析即可还原完整回答。
// 类比：把一整段回答"切成一片片"传过来，客户端边收边展示。

// sseStream 基于 HTTP 响应体的 SSE 流迭代器，实现 Stream 接口。
//
// 读取使用 bufio.Reader.ReadBytes：与 bufio.Scanner 不同，ReadBytes 对单行
// 长度没有固定上限（内部自动扩容）。render_html 等大参数工具的分片可能
// 单行就达数百 KB~MB，Scanner 的 maxToken（曾设为 1MB）会成为截断源。
type sseStream struct {
	resp   *http.Response
	reader *bufio.Reader
}

// newSSEStream 包装响应体为迭代器。调用方必须 Close() 以释放连接。
func newSSEStream(resp *http.Response) *sseStream {
	// 初始缓冲 64KB 覆盖常见小分片；超长行由 ReadBytes 自动扩容，无上限。
	return &sseStream{resp: resp, reader: bufio.NewReaderSize(resp.Body, 64*1024)}
}

// Next 返回下一个流事件。流正常结束时返回 io.EOF。
// 这是 Stream 接口的核心实现。
func (s *sseStream) Next() (StreamEvent, error) {
	for {
		// ReadBytes 在 EOF 前的最后一行（无换行符）会同时返回数据与 io.EOF，
		// 因此先处理数据、再判错误。
		line, err := s.reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)

			// 跳过空行（事件分隔符）和非 data 行（如注释、event 字段）
			if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
				if err != nil {
					return s.end(err)
				}
				continue
			}

			// 取 "data: " 之后的内容
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))

			// 结束标记
			if bytes.Equal(data, []byte("[DONE]")) {
				return StreamEvent{Done: true}, nil
			}

			ev, perr := parseChunk(data)
			if perr != nil {
				return StreamEvent{}, perr
			}
			return ev, nil
		}

		// 无数据可读：扫描结束。
		return s.end(err)
	}
}

// end 收敛流结束语义：正常结束返回 io.EOF，读取异常返回明确错误。
func (s *sseStream) end(err error) (StreamEvent, error) {
	if errors.Is(err, io.EOF) {
		return StreamEvent{}, io.EOF
	}
	if err == nil {
		return StreamEvent{}, io.EOF
	}
	return StreamEvent{}, fmt.Errorf("llm: 读取 SSE 流失败: %w", err)
}

// Close 释放底层连接。
func (s *sseStream) Close() error {
	return s.resp.Body.Close()
}

// chunk 对应协议返回的单个 SSE 数据块结构（只取关心的字段）。
type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// parseChunk 把单个 data 行解析为 StreamEvent。
func parseChunk(data []byte) (StreamEvent, error) {
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil {
		return StreamEvent{}, fmt.Errorf("llm: 解析 SSE 数据失败: %w", err)
	}

	ev := StreamEvent{}
	if c.Usage != nil {
		// 部分厂商在流末尾附带 usage
		ev.Usage = c.Usage
	}

	// 有的块 choices 为空（如仅含 usage 的块），跳过即可
	if len(c.Choices) == 0 {
		return ev, nil
	}

	choice := c.Choices[0]
	ev.Content = choice.Delta.Content
	ev.Reasoning = choice.Delta.ReasoningContent

	for _, tc := range choice.Delta.ToolCalls {
		ev.ToolCalls = append(ev.ToolCalls, ToolCallDelta{
			Index:     tc.Index,
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	// finish_reason 出现在最后一个内容块，标记即将结束，并携带模型结束原因
	if choice.FinishReason != "" {
		ev.Done = true
		ev.FinishReason = choice.FinishReason
	}
	return ev, nil
}
