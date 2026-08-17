// vision_test.go —— 视觉解析（路线 A·OpenAIVision + 环境变量装配）单测。
// 覆盖：OpenAI 多模态请求结构（model / image_url data URL / auth）、成功描述、
// HTTP 错误、空 choices、NewVisionFromEnv 的降级与装配。全程 httptest，不触真实 API。
package agentsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestOpenAIVision_Describe(t *testing.T) {
	// 验证请求体（model / content parts / image_url data URL / auth header），返回固定描述。
	var gotAuth string
	var gotPayload visionChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"这是一张课程表，包含周一至周五的课程安排"}}]}`))
	}))
	t.Cleanup(srv.Close)

	v := &OpenAIVision{baseURL: srv.URL, apiKey: "sk-test", model: "qwen-vl-plus", log: zap.NewNop()}
	desc, err := v.Describe(context.Background(), []byte{0x01, 0x02, 0x03}, "image/png")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(desc, "课程表") {
		t.Fatalf("描述内容不符: %q", desc)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotPayload.Model != "qwen-vl-plus" {
		t.Fatalf("model = %q", gotPayload.Model)
	}
	if len(gotPayload.Messages) != 1 || len(gotPayload.Messages[0].Content) != 2 {
		t.Fatalf("content parts 结构不符: %+v", gotPayload.Messages)
	}
	imgPart := gotPayload.Messages[0].Content[1]
	if imgPart.Type != "image_url" || !strings.HasPrefix(imgPart.ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("image_url part 不符: %+v", imgPart)
	}
}

func TestOpenAIVision_Describe_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"image format not supported"}}`))
	}))
	t.Cleanup(srv.Close)

	v := &OpenAIVision{baseURL: srv.URL, apiKey: "k", model: "m", log: zap.NewNop()}
	if _, err := v.Describe(context.Background(), []byte{1}, "image/svg+xml"); err == nil {
		t.Fatal("400 应返回错误")
	} else if !strings.Contains(err.Error(), "image format not supported") {
		t.Fatalf("错误应带服务端 message: %v", err)
	}
}

func TestOpenAIVision_Describe_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(srv.Close)

	v := &OpenAIVision{baseURL: srv.URL, apiKey: "k", model: "m", log: zap.NewNop()}
	if _, err := v.Describe(context.Background(), []byte{1}, "image/png"); err == nil {
		t.Fatal("空 choices 应返回错误")
	}
}

func TestNewVisionFromEnv(t *testing.T) {
	t.Run("未配置 VISION_MODEL → Noop 降级", func(t *testing.T) {
		t.Setenv("VISION_MODEL", "")
		if _, ok := NewVisionFromEnv(zap.NewNop()).(NoopVision); !ok {
			t.Fatal("应降级为 NoopVision")
		}
	})

	t.Run("配置 VISION_MODEL → OpenAIVision，密钥复用 DEEPSEEK_API_KEY", func(t *testing.T) {
		t.Setenv("VISION_MODEL", "qwen-vl-plus")
		t.Setenv("VISION_BASE_URL", "https://example.com/v1/")
		t.Setenv("VISION_API_KEY", "")
		t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek")
		v, ok := NewVisionFromEnv(zap.NewNop()).(*OpenAIVision)
		if !ok {
			t.Fatal("应装配 OpenAIVision")
		}
		if v.baseURL != "https://example.com/v1" { // 去尾斜杠
			t.Fatalf("baseURL = %q", v.baseURL)
		}
		if v.apiKey != "sk-deepseek" {
			t.Fatalf("apiKey 应复用 DEEPSEEK_API_KEY: %q", v.apiKey)
		}
	})

	t.Run("独立 VISION_API_KEY 优先于 DEEPSEEK_API_KEY", func(t *testing.T) {
		t.Setenv("VISION_MODEL", "glm-4v")
		t.Setenv("VISION_API_KEY", "sk-vision")
		t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek")
		v, ok := NewVisionFromEnv(zap.NewNop()).(*OpenAIVision)
		if !ok {
			t.Fatal("应装配 OpenAIVision")
		}
		if v.apiKey != "sk-vision" {
			t.Fatalf("apiKey = %q, want sk-vision", v.apiKey)
		}
	})

	t.Run("VISION_FALLBACK_MODEL 装配备用后端（缺省复用主 base/key）", func(t *testing.T) {
		t.Setenv("VISION_MODEL", "glm-4.6v-flash")
		t.Setenv("VISION_BASE_URL", "https://open.bigmodel.cn/api/paas/v4")
		t.Setenv("VISION_API_KEY", "sk-primary")
		t.Setenv("VISION_FALLBACK_MODEL", "glm-4.1v-thinking-flash")
		t.Setenv("VISION_FALLBACK_BASE_URL", "")
		t.Setenv("VISION_FALLBACK_API_KEY", "")
		v, ok := NewVisionFromEnv(zap.NewNop()).(*OpenAIVision)
		if !ok {
			t.Fatal("应装配 OpenAIVision")
		}
		if v.fallback == nil {
			t.Fatal("应装配备用后端")
		}
		if v.fallback.model != "glm-4.1v-thinking-flash" {
			t.Fatalf("fallback model = %q", v.fallback.model)
		}
		if v.fallback.baseURL != v.baseURL || v.fallback.apiKey != v.apiKey {
			t.Fatalf("备用应复用主 base/key, fb=%+v main=%+v", v.fallback, v)
		}
	})

	t.Run("无 VISION_FALLBACK_MODEL → 不装配备用", func(t *testing.T) {
		t.Setenv("VISION_MODEL", "glm-4.6v-flash")
		t.Setenv("VISION_FALLBACK_MODEL", "")
		v, ok := NewVisionFromEnv(zap.NewNop()).(*OpenAIVision)
		if !ok {
			t.Fatal("应装配 OpenAIVision")
		}
		if v.fallback != nil {
			t.Fatal("不应有备用后端")
		}
	})
}

func TestOpenAIVision_Describe_MainFailureFallsBack(t *testing.T) {
	// 主模型 429（免费模型限流）→ 自动切备用模型成功。
	var primaryCalls, fallbackCalls int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	t.Cleanup(primary.Close)
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"备用模型描述"}}]}`))
	}))
	t.Cleanup(fallbackSrv.Close)

	v := &OpenAIVision{
		baseURL: primary.URL, apiKey: "k", model: "primary", log: zap.NewNop(),
		fallback: &visionBackend{baseURL: fallbackSrv.URL, apiKey: "k2", model: "fallback", log: zap.NewNop()},
	}
	desc, err := v.Describe(context.Background(), []byte{1}, "image/png")
	if err != nil {
		t.Fatalf("应切备用成功: %v", err)
	}
	if !strings.Contains(desc, "备用模型") {
		t.Fatalf("描述应来自备用模型: %q", desc)
	}
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("调用次数 primary=%d fallback=%d, want 1/1", primaryCalls, fallbackCalls)
	}
}

func TestOpenAIVision_Describe_MainSuccessSkipsFallback(t *testing.T) {
	var fallbackCalls int
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"主模型描述"}}]}`))
	}))
	t.Cleanup(primary.Close)
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
	}))
	t.Cleanup(fallbackSrv.Close)

	v := &OpenAIVision{
		baseURL: primary.URL, apiKey: "k", model: "m", log: zap.NewNop(),
		fallback: &visionBackend{baseURL: fallbackSrv.URL, apiKey: "k", model: "f", log: zap.NewNop()},
	}
	desc, err := v.Describe(context.Background(), []byte{1}, "image/png")
	if err != nil || !strings.Contains(desc, "主模型") {
		t.Fatalf("主成功应直接返回, desc=%q err=%v", desc, err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("主成功不应调备用, fallbackCalls=%d", fallbackCalls)
	}
}

func TestOpenAIVision_Describe_FallbackAllFail(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(primary.Close)
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(fallbackSrv.Close)

	v := &OpenAIVision{
		baseURL: primary.URL, apiKey: "k", model: "m", log: zap.NewNop(),
		fallback: &visionBackend{baseURL: fallbackSrv.URL, apiKey: "k", model: "f", log: zap.NewNop()},
	}
	_, err := v.Describe(context.Background(), []byte{1}, "image/png")
	if err == nil {
		t.Fatal("主备都失败应返回错误")
	}
	if !strings.Contains(err.Error(), "主模型") || !strings.Contains(err.Error(), "备用模型") {
		t.Fatalf("聚合错误应含主备信息: %v", err)
	}
}

func TestOpenAIVision_Describe_ReasoningContent(t *testing.T) {
	// 深度思考类模型：content 为空、答案在 reasoning_content，应回退解析。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"深度思考得出的图片描述"}}]}`))
	}))
	t.Cleanup(srv.Close)

	v := &OpenAIVision{baseURL: srv.URL, apiKey: "k", model: "thinking", log: zap.NewNop()}
	desc, err := v.Describe(context.Background(), []byte{1}, "image/png")
	if err != nil {
		t.Fatalf("应回退取 reasoning_content: %v", err)
	}
	if !strings.Contains(desc, "深度思考") {
		t.Fatalf("desc = %q", desc)
	}
}

func TestOpenAIVision_Describe_SendsThinkingConfig(t *testing.T) {
	// thinking=disabled → 请求体携带 {"thinking":{"type":"disabled"}}（VISION_THINKING 装配）。
	var got bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req visionChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Thinking != nil && req.Thinking.Type == "disabled"
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	v := &OpenAIVision{baseURL: srv.URL, apiKey: "k", model: "m", log: zap.NewNop(), thinking: &visionThinking{Type: "disabled"}}
	if _, err := v.Describe(context.Background(), []byte{1}, "image/png"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !got {
		t.Fatal("请求体应携带 thinking=disabled")
	}
}

func TestOpenAIVision_Describe_NoThinkingWhenNil(t *testing.T) {
	// thinking 为 nil（VISION_THINKING 未配置）→ 请求体不带 thinking（厂商默认）。
	var got bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req visionChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Thinking != nil
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	v := &OpenAIVision{baseURL: srv.URL, apiKey: "k", model: "m", log: zap.NewNop()}
	if _, err := v.Describe(context.Background(), []byte{1}, "image/png"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got {
		t.Fatal("thinking 为 nil 时不应发送该参数")
	}
}

func TestThinkingFromEnv(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string // "" = 期望 nil
	}{
		{"disabled 关闭思考", "disabled", "disabled"},
		{"enabled 开启思考", "enabled", "enabled"},
		{"大写容忍", "DISABLED", "disabled"},
		{"空值不发送", "", ""},
		{"非法值不发送", "maybe", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISION_THINKING", tc.value)
			th := thinkingFromEnv()
			if tc.want == "" {
				if th != nil {
					t.Fatalf("应返回 nil, got %+v", th)
				}
				return
			}
			if th == nil || th.Type != tc.want {
				t.Fatalf("thinking = %+v, want %q", th, tc.want)
			}
		})
	}
}
