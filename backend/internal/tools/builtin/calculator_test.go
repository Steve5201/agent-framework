package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
)

func TestCalculatorTool_Schema(t *testing.T) {
	s := CalculatorTool{}.Schema()
	if s.Name != "calculator" {
		t.Fatalf("Name = %q, want calculator", s.Name)
	}
	if s.Permission != schema.PermissionL0Pure {
		t.Fatalf("Permission = %v, want L0 纯计算", s.Permission)
	}
	if s.RequiresApproval() {
		t.Fatal("L0 纯计算工具不应要求用户确认")
	}
	if len(s.Required) != 1 || s.Required[0] != "expression" {
		t.Fatalf("Required = %v, want [expression]", s.Required)
	}
}

func TestCalculatorTool_Execute(t *testing.T) {
	cases := []struct{ expr, want string }{
		{"2+3", "5"},
		{"2*3+4", "10"},
		{"(2+3)*4", "20"},
		{"10/4", "2.5"},
		{"2^10", "1024"},
		{"7%3", "1"},
		{"-5+2", "-3"},
		{"+5", "5"},
		{"2.5*4", "10"},
		{"2^2^3", "256"}, // 幂右结合
		{"pi", "3.141592653589793"},
		{"2*(pi+1)", "8.283185307179586"},
	}
	for _, c := range cases {
		args, _ := json.Marshal(map[string]string{"expression": c.expr})
		out, err := (CalculatorTool{}).Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("表达式 %q 计算失败: %v", c.expr, err)
		}
		if out != c.want {
			t.Errorf("表达式 %q = %q, want %q", c.expr, out, c.want)
		}
	}
}

func TestCalculatorTool_Errors(t *testing.T) {
	ctx := context.Background()
	// 空表达式在 Execute 层直接拒绝。
	if _, err := (CalculatorTool{}).Execute(ctx, nil); err == nil {
		t.Fatal("空参数应返回错误")
	}
	for _, expr := range []string{"1/0", "7%0", "1+", "(2+3", "1 2", "sqrt(4)", "10^100000"} {
		args, _ := json.Marshal(map[string]string{"expression": expr})
		if _, err := (CalculatorTool{}).Execute(ctx, args); err == nil {
			t.Errorf("表达式 %q 应报错，实际成功", expr)
		}
	}
}

func TestCalculatorTool_ResultTooLarge(t *testing.T) {
	// 结果超 100 位应拒绝（防撑爆上下文）：2^1000 ≈ 1e301，约 302 位。
	args, _ := json.Marshal(map[string]string{"expression": "2^1000"})
	_, err := (CalculatorTool{}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "过大") {
		t.Fatalf("超大结果应报错，实际 err=%v", err)
	}
}
