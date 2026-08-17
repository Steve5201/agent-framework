package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Limiter
// ---------------------------------------------------------------------------

// TestLimiter_Burst 验证桶容量允许连续消费 burst 个令牌，随后拒绝。
func TestLimiter_Burst(t *testing.T) {
	l := NewLimiter(1, 3) // 每秒补 1 个，桶容量 3
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("第 %d 次应放行（桶容量 3）", i+1)
		}
	}
	if l.Allow() {
		t.Fatal("桶空后应拒绝")
	}
}

// TestLimiter_Refill 验证等待 1/rate 时长后令牌补充。
func TestLimiter_Refill(t *testing.T) {
	l := NewLimiter(10, 1) // 每秒 10 个，即每 100ms 补 1 个
	if !l.Allow() {
		t.Fatal("初始满桶应放行")
	}
	if l.Allow() {
		t.Fatal("刚消费完应拒绝")
	}
	time.Sleep(120 * time.Millisecond) // 等 1 个令牌补充
	if !l.Allow() {
		t.Fatal("补充后应放行")
	}
}

// TestLimiter_AllowN 验证批量消耗：充足时成功，不足时整体拒绝。
func TestLimiter_AllowN(t *testing.T) {
	l := NewLimiter(1, 5)
	if !l.AllowN(3) {
		t.Fatal("AllowN(3) 应成功")
	}
	if l.AllowN(3) {
		t.Fatal("剩余 2 个令牌不足以消耗 3 个，应整体拒绝")
	}
	if !l.AllowN(2) {
		t.Fatal("AllowN(2) 应成功")
	}
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// TestStore_IndependentKeys 验证不同 key 的桶相互独立。
func TestStore_IndependentKeys(t *testing.T) {
	s := NewStore(Config{Rate: 1, Burst: 1})
	if !s.Allow("ip:1.1.1.1") {
		t.Fatal("新 key 首次应放行")
	}
	if s.Allow("ip:1.1.1.1") {
		t.Fatal("同 key 第二次应拒绝")
	}
	if !s.Allow("ip:2.2.2.2") {
		t.Fatal("不同 key 应独立放行")
	}
}

// TestStore_Cleanup 验证空闲 key 被清理，活跃 key 保留。
func TestStore_Cleanup(t *testing.T) {
	s := NewStore(Config{Rate: 1, Burst: 1})
	s.Allow("active")
	s.Allow("idle")

	// 手动把 idle 的 lastHit 改旧（模拟长时间未访问）
	s.mu.Lock()
	s.limiters["idle"].lastHit = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	removed := s.Cleanup(time.Hour)
	if removed != 1 {
		t.Errorf("应移除 1 个 key，实际 %d", removed)
	}
	if _, ok := s.limiters["active"]; !ok {
		t.Error("活跃 key 不应被清理")
	}
	if _, ok := s.limiters["idle"]; ok {
		t.Error("空闲 key 应被清理")
	}
}

// ---------------------------------------------------------------------------
// HTTP 中间件
// ---------------------------------------------------------------------------

// TestMiddleware_RejectsAfterBurst 验证超出突发容量后返回 429 统一错误体。
func TestMiddleware_RejectsAfterBurst(t *testing.T) {
	store := NewStore(Config{Rate: 100, Burst: 1})
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	handler := Middleware(store, func(r *http.Request) string { return "ip:test" })(inner)

	// 第一次放行
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("首次请求 status = %d, want 200", rec.Code)
	}

	// 第二次被限流
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("超限请求 status = %d, want 429", rec2.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v", err)
	}
	if body["code"] != float64(42901) {
		t.Errorf("body.code = %v, want 42901", body["code"])
	}
}

// TestMiddleware_KeyByIP 验证 KeyByIP 优先取 X-Forwarded-For 首项。
func TestMiddleware_KeyByIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := KeyByIP(req); got != "ip:203.0.113.9" {
		t.Errorf("KeyByIP = %q, want ip:203.0.113.9", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req2.RemoteAddr = "10.0.0.5:8080"
	if got := KeyByIP(req2); got != "ip:10.0.0.5:8080" {
		t.Errorf("KeyByIP(no xff) = %q, want ip:10.0.0.5:8080", got)
	}
}

// TestMiddleware_EmptyKey 验证 keyFn 返回空时兜底为 anonymous，避免挤兑。
func TestMiddleware_EmptyKey(t *testing.T) {
	store := NewStore(Config{Rate: 1, Burst: 1})
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	handler := Middleware(store, func(r *http.Request) string { return "" })(inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("anonymous 首次应放行，status = %d", rec.Code)
	}
}
