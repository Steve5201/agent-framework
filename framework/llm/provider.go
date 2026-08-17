package llm

import "context"

// Provider 大模型接入的统一抽象。
//
// 每接入一家厂商（DeepSeek/OpenAI/Kimi/智谱/Anthropic...）就实现
// 一个 Provider。上层 agent 只依赖本接口，不关心底层是哪个厂商。
//
// 类比 C++ 的抽象基类（含纯虚函数）：
//   - 调用方持有 Provider 引用，运行时动态绑定具体实现；
//   - 新增厂商 = 新增派生类，不改调用方代码。
//
// 厂商接入清单（均为 OpenAI 兼容端点，一个实现全覆盖）：
//   - DeepSeek   https://api.deepseek.com/v1
//   - OpenAI     https://api.openai.com/v1
//   - Moonshot   https://api.moonshot.cn/v1
//   - 智谱GLM    https://open.bigmodel.cn/api/paas/v4
//   - 通义Qwen   https://dashscope.aliyuncs.com/compatible-mode/v1
type Provider interface {
	// Name 返回供应商名称，用于日志、路由与多模型切换。
	Name() string

	// Chat 非流式对话：阻塞直到收到完整响应。
	Chat(ctx context.Context, req *Request) (*Response, error)

	// ChatStream 流式对话：返回逐 token 迭代器。
	// 调用方必须负责 Close()。
	ChatStream(ctx context.Context, req *Request) (Stream, error)
}
