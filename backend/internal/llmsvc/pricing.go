package llmsvc

import "math"

// CostUSD 计算一次调用成本（美元）。
//
// 计费模型：价格 = 输入 token 单价 × 输入量 + 输出 token 单价 × 输出量，
// 其中单价按"每百万 token"计（厂商通行报价口径）。
//
// 结果四舍五入到 6 位小数，与 usage_logs.cost_usd NUMERIC(10,6) 对齐。
func CostUSD(promptTokens, completionTokens int, promptPricePer1M, completionPricePer1M float64) float64 {
	promptCost := float64(promptTokens) / 1e6 * promptPricePer1M
	completionCost := float64(completionTokens) / 1e6 * completionPricePer1M
	return math.Round((promptCost+completionCost)*1e6) / 1e6
}
