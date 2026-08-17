package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestRetry_429ThenSuccess 验证 429 后重试成功。
func TestRetry_429ThenSuccess(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer ts.Close()

	c, err := NewOpenAICompatible(Config{Name: "t", BaseURL: ts.URL, APIKey: "k", MaxRetries: 3})
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	resp, err := c.Chat(context.Background(), &Request{})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q", resp.Content)
	}
	if calls.Load() != 2 {
		t.Errorf("请求次数 = %d, want 2（1 次 429 + 1 次成功）", calls.Load())
	}
}

// TestRetry_NotRetry4xx 验证 401 不被重试（重试无意义）。
func TestRetry_NotRetry4xx(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
	}))
	defer ts.Close()

	c, err := NewOpenAICompatible(Config{Name: "t", BaseURL: ts.URL, APIKey: "k", MaxRetries: 3})
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if _, err := c.Chat(context.Background(), &Request{}); err == nil {
		t.Fatal("401 应返回错误")
	}
	if calls.Load() != 1 {
		t.Errorf("请求次数 = %d, want 1（401 不重试）", calls.Load())
	}
}

// TestRetry_Exhaust5xx 验证持续 5xx 时重试耗尽仍返回错误。
func TestRetry_Exhaust5xx(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"server busy"}}`))
	}))
	defer ts.Close()

	c, err := NewOpenAICompatible(Config{Name: "t", BaseURL: ts.URL, APIKey: "k", MaxRetries: 2})
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if _, err := c.Chat(context.Background(), &Request{}); err == nil {
		t.Fatal("持续 503 应返回错误")
	}
	// 1 次初始 + 2 次重试 = 3
	if calls.Load() != 3 {
		t.Errorf("请求次数 = %d, want 3", calls.Load())
	}
}

// TestBackoff_Increasing 验证退避时间随次数增长且封顶。
func TestBackoff_Increasing(t *testing.T) {
	a0 := backoff(0)
	a1 := backoff(1)
	a2 := backoff(2)
	a10 := backoff(10)

	if a0 >= a1 || a1 >= a2 {
		t.Errorf("退避应递增: %v, %v, %v", a0, a1, a2)
	}
	if a10 > 35_000_000_000 { // 封顶约 30s + 抖动
		t.Errorf("退避应封顶: %v", a10)
	}
}

// TestIsRetryableStatus 验证可重试状态码判定。
func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504}
	notRetryable := []int{200, 400, 401, 403, 404, 422}

	for _, code := range retryable {
		if !isRetryableStatus(code) {
			t.Errorf("%d 应可重试", code)
		}
	}
	for _, code := range notRetryable {
		if isRetryableStatus(code) {
			t.Errorf("%d 不应重试", code)
		}
	}
}
