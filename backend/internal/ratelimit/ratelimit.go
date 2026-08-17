// Package ratelimit 提供令牌桶限流器（P2-13）：
//   - 单桶 Limiter：固定速率补充令牌 + 突发容量（burst）；
//   - 多 key Store：按 key（IP / 用户 ID）管理独立桶，自动惰性补充；
//   - 空闲清理：长期未使用的 key 自动移除，防止内存泄漏；
//   - HTTP 中间件：按 key 限流，超出返回 429 + 统一错误体。
//
// 令牌桶原理：桶内最多存 burst 个令牌，每 1/rate 秒补充 1 个。
// 每次请求消耗 1 个令牌；桶空则拒绝。这样既能吸收突发（桶内积攒），
// 又限制了长期平均速率（补充速率封顶）。
//
// 使用示例（gateway 全局限流，按客户端 IP）：
//
//	store := ratelimit.NewStore(ratelimit.Config{Rate: 100, Burst: 50})
//	handler = ratelimit.Middleware(store, ratelimit.KeyByIP)(handler)
package ratelimit

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Steve5201/agent-backend/internal/errors"
)

// Config 令牌桶配置。
type Config struct {
	Rate  float64 // 每秒补充令牌数（长期平均速率）
	Burst int     // 桶容量（允许的最大突发请求数），必须 > 0
}

// Limiter 单 key 令牌桶。
type Limiter struct {
	mu     sync.Mutex
	tokens float64   // 当前可用令牌（可为小数，消耗按 1 计）
	last   time.Time // 上次补充时间
	rate   float64   // 每秒补充速率
	burst  float64   // 桶容量
}

// NewLimiter 创建令牌桶。rate 为每秒补充数，burst 为桶容量。
func NewLimiter(rate float64, burst int) *Limiter {
	return &Limiter{
		tokens: float64(burst), // 初始满桶，允许立即消费 burst 个请求
		last:   time.Now(),
		rate:   rate,
		burst:  float64(burst),
	}
}

// refill 惰性补充令牌：按距上次取桶的时长增加令牌，不超过桶容量。
func (l *Limiter) refill(now time.Time) {
	elapsed := now.Sub(l.last).Seconds()
	l.tokens = math.Min(l.burst, l.tokens+elapsed*l.rate)
	l.last = now
}

// Allow 消耗 1 个令牌，成功返回 true。
func (l *Limiter) Allow() bool {
	return l.AllowN(1)
}

// AllowN 尝试消耗 n 个令牌，成功返回 true（不拆开发放）。
func (l *Limiter) AllowN(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill(time.Now())
	if l.tokens < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	return true
}

// ---------------------------------------------------------------------------
// Store：多 key 限流
// ---------------------------------------------------------------------------

// Store 管理多个独立令牌桶，按 key 索引（并发安全）。
type Store struct {
	mu       sync.Mutex
	limiters map[string]*limiterEntry
	rate     float64
	burst    float64
}

type limiterEntry struct {
	lim     *Limiter
	lastHit time.Time // 最近一次访问时间，用于空闲清理
}

// NewStore 创建限流 Store，所有 key 共享同一速率与容量配置。
func NewStore(cfg Config) *Store {
	if cfg.Burst <= 0 {
		cfg.Burst = 1 // 非法配置兜底为最小桶
	}
	if cfg.Rate <= 0 {
		cfg.Rate = 1
	}
	return &Store{
		limiters: make(map[string]*limiterEntry),
		rate:     cfg.Rate,
		burst:    float64(cfg.Burst),
	}
}

// Allow 按 key 消耗 1 个令牌，成功返回 true。
func (s *Store) Allow(key string) bool {
	return s.AllowN(key, 1)
}

// AllowN 按 key 尝试消耗 n 个令牌。
func (s *Store) AllowN(key string, n int) bool {
	s.mu.Lock()
	entry, ok := s.limiters[key]
	if !ok {
		entry = &limiterEntry{lim: NewLimiter(s.rate, int(s.burst))}
		s.limiters[key] = entry
	}
	entry.lastHit = time.Now()
	s.mu.Unlock()
	return entry.lim.AllowN(n)
}

// Cleanup 移除超过 maxIdle 未被访问的 key 的令牌桶，返回移除数量。
// 建议由 StartCleanup 周期性调用，防止恶意/低频 key 长期占用内存。
func (s *Store) Cleanup(maxIdle time.Duration) int {
	cutoff := time.Now().Add(-maxIdle)
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, entry := range s.limiters {
		if entry.lastHit.Before(cutoff) {
			delete(s.limiters, key)
			removed++
		}
	}
	return removed
}

// StartCleanup 在后台周期性执行 Cleanup，直到 ctx 取消。
func (s *Store) StartCleanup(ctxDone <-chan struct{}, interval, maxIdle time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-ticker.C:
			s.Cleanup(maxIdle)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP 中间件
// ---------------------------------------------------------------------------

// KeyByIP 以客户端 IP 作为限流 key（配合代理场景可取 X-Forwarded-For 首项）。
func KeyByIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return "ip:" + first
		}
	}
	return "ip:" + r.RemoteAddr
}

// Middleware 按 key 限流的 HTTP 中间件。
// keyFn 从请求提取身份（IP / 用户 ID / 组合）；超出限流时返回
// 429 + 统一错误体 {code:42901, message, request_id}。
func Middleware(store *Store, keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if key == "" {
				// key 为空说明身份无法识别（如认证中间件未生效），直接拒绝，
				// 避免所有匿名请求共享同一个匿名桶导致互相挤兑。
				key = "anonymous"
			}
			if !store.Allow(key) {
				status, body := errors.HTTPBody(
					errors.New(errors.CodeResourceExhausted, "请求过于频繁，请稍后再试").
						WithRequestID(errors.RequestIDFromContext(r.Context())),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(body)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
