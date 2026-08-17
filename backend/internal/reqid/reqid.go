// Package reqid 提供 request_id 的生成（crypto/rand），
// 供 HTTP（middleware）与 gRPC（grpcx）全链路透传复用。
// context 的写入/读取见 internal/errors 包的
// NewContextWithRequestID / RequestIDFromContext。
package reqid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Generate 生成 32 位十六进制随机 ID。
// 极端情况下 crypto/rand 失败时，退回基于纳秒时间戳的 ID，保证不阻塞调用方。
func Generate() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
