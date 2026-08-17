package llm

import (
	"context"
	"testing"
)

// TestWithHeader：验证 context 注入/追加/覆盖/隔离语义。
func TestWithHeader(t *testing.T) {
	ctx := context.Background()

	// 空 ctx 读取 → 空 map
	if got := headersFromContext(ctx); len(got) != 0 {
		t.Fatalf("空 ctx 应返回空 map，got %v", got)
	}

	ctx1 := WithHeader(ctx, "X-User-Id", "42")
	if got := headersFromContext(ctx1)["X-User-Id"]; got != "42" {
		t.Fatalf("注入失败: got %q", got)
	}

	// 追加第二个 header
	ctx2 := WithHeader(ctx1, "X-Trace", "abc")
	m2 := headersFromContext(ctx2)
	if m2["X-User-Id"] != "42" || m2["X-Trace"] != "abc" {
		t.Fatalf("追加失败: %v", m2)
	}

	// 同名覆盖（后者生效）
	ctx3 := WithHeader(ctx2, "X-User-Id", "99")
	if got := headersFromContext(ctx3)["X-User-Id"]; got != "99" {
		t.Fatalf("覆盖失败: got %q", got)
	}

	// 不可变性：ctx1 不应被 ctx3 的覆盖影响
	if got := headersFromContext(ctx1)["X-User-Id"]; got != "42" {
		t.Fatalf("父 ctx 被污染: got %q", got)
	}
}
