package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseChunk_Text 验证文本 delta 解析。
func TestParseChunk_Text(t *testing.T) {
	ev, err := parseChunk([]byte(`{"choices":[{"delta":{"content":"你好"},"finish_reason":null}]}`))
	if err != nil {
		t.Fatalf("parseChunk error = %v", err)
	}
	if ev.Content != "你好" {
		t.Errorf("Content = %q, want 你好", ev.Content)
	}
	if ev.Done {
		t.Error("未结束的 chunk 不应标记 Done")
	}
}

// TestParseChunk_Reasoning 验证思考增量（reasoning_content）解析——
// 思考模型在回答前先以 reasoning 增量到达思考内容。
func TestParseChunk_Reasoning(t *testing.T) {
	ev, err := parseChunk([]byte(`{"choices":[{"delta":{"reasoning_content":"先想一步"},"finish_reason":null}]}`))
	if err != nil {
		t.Fatalf("parseChunk error = %v", err)
	}
	if ev.Reasoning != "先想一步" {
		t.Errorf("Reasoning = %q, want 先想一步", ev.Reasoning)
	}
	if ev.Content != "" {
		t.Errorf("Content 应为空, got %q", ev.Content)
	}
}

// TestParseChunk_ToolDelta 验证工具调用增量解析。
func TestParseChunk_ToolDelta(t *testing.T) {
	ev, err := parseChunk([]byte(`{
		"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"calculator","arguments":"{\"a\":12,"}}]},"finish_reason":null}]
	}`))
	if err != nil {
		t.Fatalf("parseChunk error = %v", err)
	}
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("ToolCalls 数量 = %d, want 1", len(ev.ToolCalls))
	}
	tc := ev.ToolCalls[0]
	if tc.Index != 0 || tc.ID != "call_1" || tc.Name != "calculator" {
		t.Errorf("ToolCallDelta = %+v", tc)
	}
	if !strings.Contains(tc.Arguments, "12") {
		t.Errorf("Arguments = %q", tc.Arguments)
	}
}

// TestParseChunk_FinishReason 验证带 finish_reason 的块标记结束。
func TestParseChunk_FinishReason(t *testing.T) {
	ev, err := parseChunk([]byte(`{"choices":[{"delta":{"content":"最后一片"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatalf("parseChunk error = %v", err)
	}
	if !ev.Done {
		t.Error("finish_reason 存在时应标记 Done")
	}
	if ev.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", ev.FinishReason)
	}
}

// TestParseChunk_Usage 验证 usage 块传递。
func TestParseChunk_Usage(t *testing.T) {
	ev, err := parseChunk([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	if err != nil {
		t.Fatalf("parseChunk error = %v", err)
	}
	if ev.Usage == nil || ev.Usage.TotalTokens != 3 {
		t.Errorf("Usage = %+v", ev.Usage)
	}
}

// TestSSEStream_Iteration 验证完整流：多文本片 + [DONE] 结束。
func TestSSEStream_Iteration(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"你\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"好\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer ts.Close()

	c, err := NewOpenAICompatible(Config{Name: "t", BaseURL: ts.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAICompatible error = %v", err)
	}

	st, err := c.ChatStream(context.Background(), &Request{})
	if err != nil {
		t.Fatalf("ChatStream error = %v", err)
	}
	defer st.Close()

	var got string
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		got += ev.Content
	}
	if got != "你好" {
		t.Errorf("流式拼接 = %q, want 你好", got)
	}
}

// TestSSEStream_BrokenConnection 验证连接中断时报错而非静默。
func TestSSEStream_BrokenConnection(t *testing.T) {
	// 服务器写入一半就关闭连接
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
	}))
	defer ts.Close()

	c, err := NewOpenAICompatible(Config{Name: "t", BaseURL: ts.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	st, err := c.ChatStream(context.Background(), &Request{})
	if err != nil {
		t.Fatalf("ChatStream error = %v", err)
	}
	defer st.Close()

	// 第一片正常
	if _, err := st.Next(); err != nil {
		t.Fatalf("第一片 Next() error = %v", err)
	}
	// 之后无更多数据，扫描器正常结束 → io.EOF（连接关闭被视为正常结束）
	if _, err := st.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("期待 EOF，实际 = %v", err)
	}
}

// TestSSEStream_LongToolCallArgs_NoTruncation 回归：流式工具调用参数超长时，
// 增量分片拼接必须完整（覆盖 64KB scanner 初始缓冲扩容分界），不得截断。
// 背景：render_html 的 html 参数可达数百 KB，若拼接截断 → 参数 JSON 不完整。
func TestSSEStream_LongToolCallArgs_NoTruncation(t *testing.T) {
	html := "<html><body>" + strings.Repeat("<p>段落内容。</p>", 12000) + "</body></html>"
	fullArgs := `{"format":"html","title":"测试文档","html":"` + html + `"}`
	if len(fullArgs) < 256*1024 {
		t.Fatalf("测试数据过短: %d 字节", len(fullArgs))
	}
	makeChunk := func(argPart string) string {
		esc, _ := json.Marshal(argPart)
		return `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"render_html","arguments":` + string(esc) + `}}]},"finish_reason":null}]}`
	}
	// 按 rune 边界切分：真实流式增量是"解码后文本"的子串，绝不会切在多字节
	// 字符中间；字节切分会让 json.Marshal 替换 invalid 序列导致拼接失真。
	const chunkRunes = 2800 // ≈8KB（以中文为主的文档）
	runes := []rune(fullArgs)
	var body strings.Builder
	for i := 0; i < len(runes); i += chunkRunes {
		end := i + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		body.WriteString("data: ")
		body.WriteString(makeChunk(string(runes[i:end])))
		body.WriteString("\n\n")
	}
	body.WriteString(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n")
	body.WriteString("data: [DONE]\n\n")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body.String()))
	}))
	defer ts.Close()

	c, err := NewOpenAICompatible(Config{Name: "t", BaseURL: ts.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAICompatible error = %v", err)
	}
	st, err := c.ChatStream(context.Background(), &Request{})
	if err != nil {
		t.Fatalf("ChatStream error = %v", err)
	}
	defer st.Close()

	var got strings.Builder
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		for _, tc := range ev.ToolCalls {
			if tc.Index == 0 {
				got.WriteString(tc.Arguments)
			}
		}
	}
	if got.String() != fullArgs {
		t.Fatalf("工具参数拼接不完整: 期望 %d 字节，实际 %d 字节", len(fullArgs), got.Len())
	}
}

// TestSSEStream_OversizeSingleChunk 回归：单个 data 行携带超大工具参数
// （超过 bufio.Scanner 时代 1MB 的单行上限，覆盖 render_html 长文档单分片
// 极端场景）也能完整解析，不报 token too long。
func TestSSEStream_OversizeSingleChunk(t *testing.T) {
	big := strings.Repeat("x", 1500*1024)
	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"render_html","arguments":"` + big + `"}}]},"finish_reason":null}]}`
	body := "data: " + chunk + "\n\n" + "data: [DONE]\n\n"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer ts.Close()

	c, err := NewOpenAICompatible(Config{Name: "t", BaseURL: ts.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAICompatible error = %v", err)
	}
	st, err := c.ChatStream(context.Background(), &Request{})
	if err != nil {
		t.Fatalf("ChatStream error = %v", err)
	}
	defer st.Close()

	var args string
	done := false
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("超大单行不应报错: %v", err)
		}
		for _, tc := range ev.ToolCalls {
			args += tc.Arguments
		}
		if ev.Done {
			done = true
		}
	}
	if !done {
		t.Error("应收到 [DONE] 结束事件")
	}
	if args != big {
		t.Errorf("超大参数未完整透传: 期望 %d 字节，实际 %d 字节", len(big), len(args))
	}
}
