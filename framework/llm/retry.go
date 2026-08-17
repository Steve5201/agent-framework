package llm

import (
	"io"
	"math/rand"
	"net/http"
	"time"
)

// retryableTransport 对 HTTP 请求做"可重试错误"的指数退避重试。
//
// 为什么需要重试？
// 大模型 API 在高负载时经常返回 429（限流）或 5xx（临时故障），
// 一次性失败就直接放弃会大大降低可用性。自动重试是工业级标配。
//
// 重试策略（保守原则）：
//   - 只重试：网络错误、429、500/502/503/504；
//   - 不重试：4xx 业务错误（401/400 等），重试也没用；
//   - 指数退避 + 随机抖动：1s → 2s → 4s ...（防"惊群"同时重试）；
//   - 最多 maxRetries 次。
type retryableTransport struct {
	base       http.RoundTripper
	maxRetries int
}

// isRetryableStatus 判断状态码是否值得重试。
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429 限流
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

// backoff 计算第 attempt 次重试前的等待时长（指数退避 + 抖动）。
// 重试时间：约 base*2^attempt 秒，附加 ±20% 随机抖动。
func backoff(attempt int) time.Duration {
	base := time.Second
	// 上限封顶 30s，避免无限拉长
	max := 30 * time.Second
	d := base << attempt // base * 2^attempt
	if d > max {
		d = max
	}
	// 抖动：±20%
	jitter := time.Duration(rand.Int63n(int64(d) / 5)) - time.Duration(int64(d)/10)
	return d + jitter
}

// RoundTrip 执行带重试的请求。
func (t *retryableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		// body 是只读一次的流，重试前必须重建（GetBody 由 bytes.Reader 自动提供）
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := t.base.RoundTrip(req)
		if err != nil {
			// 网络错误：值得重试（可能是瞬时抖动）
			lastErr = err
			if attempt < t.maxRetries {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, err
		}

		// 无需重试：成功或非可重试错误
		if !isRetryableStatus(resp.StatusCode) || attempt == t.maxRetries {
			return resp, nil
		}

		// 可重试状态：先把响应体读完并关闭，否则连接无法复用
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if attempt < t.maxRetries {
			time.Sleep(backoff(attempt))
		}
	}
	return nil, lastErr
}
