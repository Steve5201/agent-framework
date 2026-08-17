package llmsvc

import "testing"

func TestCostUSD_ZeroTokens(t *testing.T) {
	if got := CostUSD(0, 0, 0.27, 1.10); got != 0 {
		t.Errorf("零 token 成本应为 0，got %f", got)
	}
}

func TestCostUSD_Basic(t *testing.T) {
	// 100 万输入 token @ $0.27 + 100 万输出 token @ $1.10 = $1.37
	got := CostUSD(1_000_000, 1_000_000, 0.27, 1.10)
	if got != 1.37 {
		t.Errorf("CostUSD = %f, want 1.37", got)
	}
}

func TestCostUSD_Small(t *testing.T) {
	// 1000 输入 + 2000 输出
	got := CostUSD(1000, 2000, 0.27, 1.10)
	// 0.001*0.27 + 0.002*1.10 = 0.00027 + 0.0022 = 0.00247
	if got != 0.00247 {
		t.Errorf("CostUSD = %f, want 0.00247", got)
	}
}

func TestCostUSD_Rounding(t *testing.T) {
	// 结果应四舍五入到 6 位小数，避免浮点长尾
	got := CostUSD(1, 1, 0.27, 1.10)
	if got != 0.000001 {
		t.Errorf("CostUSD = %.8f, want 0.000001", got)
	}
}
