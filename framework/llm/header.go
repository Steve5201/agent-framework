package llm

import "context"

// ---- 请求头注入（context 传递）----
//
// 用途：OpenAICompatible 客户端是进程内共享单例（provider 由 cmd 注入、
// 所有请求复用），而部分请求头（如 X-User-Id）是"每请求不同"的租户信息。
// 若把 header 写进 Config 会变成全局固定值，因此设计为经 context 传递：
// 调用方在发起一次对话前 WithHeader 包装 ctx，客户端在构造 HTTP 请求时读取。
//
// 类比 C++：等价于在调用链上给每个请求附加一份"随调用传递的元数据"，
// 而不是把它放进共享对象里。

// ctxHeaderKey 自定义请求头在 context 中的存储键。
type ctxHeaderKey struct{}

// WithHeader 返回一个携带自定义请求头的新 ctx（不可变追加；同名后者覆盖前者）。
// 该 header 会被 OpenAICompatible 客户端注入到对上游的 HTTP 请求中。
func WithHeader(ctx context.Context, key, value string) context.Context {
	m := headersFromContext(ctx)
	// 复制一份再追加，避免与父 ctx 共享 map（父 ctx 可能被其它协程复用）。
	next := make(map[string]string, len(m)+1)
	for k, v := range m {
		next[k] = v
	}
	next[key] = value
	return context.WithValue(ctx, ctxHeaderKey{}, next)
}

// headersFromContext 读取 ctx 中携带的自定义请求头（不存在时返回空 map）。
func headersFromContext(ctx context.Context) map[string]string {
	if m, ok := ctx.Value(ctxHeaderKey{}).(map[string]string); ok {
		return m
	}
	return map[string]string{}
}

// ContextHeader 读取 ctx 中携带的指定自定义请求头值（如 X-User-Id）。
// 未设置时返回空串。供工具层（file_ops / code_executor 等）按"当前调用者"
// 做用户级隔离：调用方（如 agent-service）在发起对话前 WithHeader 注入，
// 工具执行时从这里取回，实现"同一份代码、每用户独立行为"。
func ContextHeader(ctx context.Context, key string) string {
	return headersFromContext(ctx)[key]
}
