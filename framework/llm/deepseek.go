package llm

// DeepSeek 连接常量：供调用方拼装 llm.Config 使用。
//
// 说明：DeepSeek 使用 OpenAI 兼容协议，与其它厂商（OpenAI/Kimi/智谱...）
// 共用统一构造器 llm.NewOpenAICompatible，无需专属工厂函数——
// 厂商差异只体现在 BaseURL 与默认模型名上。
//
// 注意：旧模型名 deepseek-chat / deepseek-reasoner 已于 2026-07-24 停用，
// 现使用统一 V4 架构的 deepseek-v4-flash 与 deepseek-v4-pro。
const (
	// DeepSeekBaseURL DeepSeek 官方 OpenAI 兼容端点。
	DeepSeekBaseURL = "https://api.deepseek.com"

	// DeepSeekFlashModel 高速低成本模型（通用对话，本项目默认）。
	DeepSeekFlashModel = "deepseek-v4-flash"

	// DeepSeekProModel 旗舰推理模型（复杂分析 / Agent 任务）。
	DeepSeekProModel = "deepseek-v4-pro"
)
