package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testProvider(t *testing.T, srv *httptest.Server) Provider {
	t.Helper()
	p, err := NewOpenAICompatible(Config{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "BAAI/bge-m3",
		Dim:     4,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	return p
}

func writeEmbedResp(w http.ResponseWriter, dims int) {
	vecs := [][]float64{
		{0.1, 0.2, 0.3, 0.4}, {0.5, 0.6, 0.7, 0.8},
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": []map[string]any{
			{"embedding": vecs[0], "index": 0},
			{"embedding": vecs[1], "index": 1},
		},
	})
}

// TestEmbedBatch_Success 验证请求构造与响应解析、顺序保持。
func TestEmbedBatch_Success(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 1<<10)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		writeEmbedResp(w, 4)
	}))
	defer srv.Close()

	p := testProvider(t, srv)
	vecs, err := p.EmbedBatch(context.Background(), []string{"第一段", "第二段"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 4 {
		t.Fatalf("向量数量/维度错误: %d x %d", len(vecs), len(vecs[0]))
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization header 错误: %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"BAAI/bge-m3"`) || !strings.Contains(gotBody, "第一段") {
		t.Errorf("请求体缺少 model/input: %s", gotBody)
	}
	if p.Name() == "" || p.Dim() != 4 {
		t.Errorf("Name/Dim 异常: %s/%d", p.Name(), p.Dim())
	}
}

// TestEmbedBatch_Empty 空输入不发请求。
func TestEmbedBatch_Empty(t *testing.T) {
	called := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	}))
	defer srv.Close()

	p := testProvider(t, srv)
	vecs, err := p.EmbedBatch(context.Background(), nil)
	if err != nil || vecs != nil || called.Load() {
		t.Fatalf("空输入应直接返回 nil（err=%v called=%v）", err, called.Load())
	}
}

// TestEmbedBatch_RetryOn5xx 500 → 重试 → 200。
func TestEmbedBatch_RetryOn5xx(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeEmbedResp(w, 4)
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", Dim: 4,
		Timeout: time.Second, MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.EmbedBatch(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if n.Load() != 2 {
		t.Errorf("应请求 2 次，实际 %d", n.Load())
	}
}

// TestEmbedBatch_NonRetryable 4xx 不重试。
func TestEmbedBatch_NonRetryable(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, `{"error":{"message":"bad model"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", Dim: 4,
		Timeout: time.Second, MaxRetries: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.EmbedBatch(context.Background(), []string{"a"}); err == nil {
		t.Fatal("400 应报错")
	}
	if n.Load() != 1 {
		t.Errorf("4xx 不应重试，实际 %d 次", n.Load())
	}
}

// TestEmbedBatch_DimMismatch 响应维度与配置不一致报错。
func TestEmbedBatch_DimMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{0.1, 0.2}, "index": 0}},
		})
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", Dim: 4,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.EmbedBatch(context.Background(), []string{"a"}); err == nil {
		t.Fatal("维度不一致应报错")
	}
}

// TestNewOpenAICompatible_Validate 构造校验：BaseURL/Model 必填；APIKey 可空
// （本地 Ollama 无需密钥，仅配置了 key 才带 Authorization）。
func TestNewOpenAICompatible_Validate(t *testing.T) {
	if _, err := NewOpenAICompatible(Config{BaseURL: "http://x", Model: "m"}); err != nil {
		t.Errorf("缺 APIKey 不应报错（本地 Ollama 无需密钥）: %v", err)
	}
	if _, err := NewOpenAICompatible(Config{BaseURL: "http://x", APIKey: "k"}); err == nil {
		t.Error("缺 Model 应报错")
	}
	if _, err := NewOpenAICompatible(Config{Model: "m", APIKey: "k"}); err == nil {
		t.Error("缺 BaseURL 应报错")
	}
}

// TestEmbedBatch_NoAuthWithoutKey 未配置 APIKey 时不携带 Authorization 头。
func TestEmbedBatch_NoAuthWithoutKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeEmbedResp(w, 4)
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(Config{
		BaseURL: srv.URL, Model: "bge-m3", Dim: 4, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.EmbedBatch(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("未配置 APIKey 不应携带 Authorization: %q", gotAuth)
	}
}

// TestUnavailableProvider 降级 Provider：服务照常启动，向量化返回 ErrNotConfigured。
func TestUnavailableProvider(t *testing.T) {
	p := NewUnavailable()
	if p.Name() != "unavailable" || p.Dim() != 0 {
		t.Errorf("Name/Dim 异常: %s/%d", p.Name(), p.Dim())
	}
	if _, err := p.EmbedBatch(context.Background(), []string{"x"}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("应返回 ErrNotConfigured，实际: %v", err)
	}
}
