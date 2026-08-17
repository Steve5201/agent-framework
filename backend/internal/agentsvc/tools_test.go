// tools_test.go —— agentsvc 工具集单测（P2 通用工具）。
//
// 覆盖：get_current_time 通用工具（Schema 说明书 / 缺省时区 / 指定时区 /
// 非法时区报错）+ DefaultToolSet 默认注册集可正常执行。
package agentsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
)

func TestGetCurrentTimeTool_Schema(t *testing.T) {
	s := getCurrentTimeTool{}.Schema()
	if s.Name != "get_current_time" {
		t.Fatalf("Name = %q, want get_current_time", s.Name)
	}
	if s.Permission != schema.PermissionL0Pure {
		t.Fatalf("Permission = %v, want L0 纯计算", s.Permission)
	}
	if !strings.Contains(s.Description, "时区") {
		t.Fatalf("Description 未说明时区参数: %s", s.Description)
	}
}

func TestGetCurrentTimeTool_Execute(t *testing.T) {
	ctx := context.Background()

	t.Run("缺省参数-本地时区", func(t *testing.T) {
		out, err := (getCurrentTimeTool{}).Execute(ctx, nil)
		if err != nil {
			t.Fatalf("Execute() 意外错误: %v", err)
		}
		if !strings.Contains(out, "当前时间：") || !strings.Contains(out, "时区") {
			t.Fatalf("输出缺少时间或时区信息: %s", out)
		}
	})

	t.Run("指定时区-UTC", func(t *testing.T) {
		args, _ := json.Marshal(getCurrentTimeArgs{Timezone: "UTC"})
		out, err := (getCurrentTimeTool{}).Execute(ctx, args)
		if err != nil {
			t.Fatalf("Execute() 意外错误: %v", err)
		}
		if !strings.Contains(out, "UTC") {
			t.Fatalf("输出未体现 UTC 时区: %s", out)
		}
	})

	t.Run("非法时区-报错", func(t *testing.T) {
		args, _ := json.Marshal(getCurrentTimeArgs{Timezone: "Not/AZone"})
		if _, err := (getCurrentTimeTool{}).Execute(ctx, args); err == nil {
			t.Fatal("非法时区应返回错误，实际 nil")
		}
	})
}

func TestDefaultToolSet_CommonToolsRegistered(t *testing.T) {
	reg, err := DefaultToolSet()
	if err != nil {
		t.Fatalf("DefaultToolSet() 失败: %v", err)
	}
	ctx := context.Background()

	// get_current_time：无参可执行（一般智能体标配通用工具）
	res, err := reg.Execute(ctx, schema.ToolCall{Name: "get_current_time"}, true)
	if err != nil {
		t.Fatalf("get_current_time 执行失败: %v", err)
	}
	if !strings.Contains(res.Content, "当前时间：") {
		t.Fatalf("get_current_time 输出异常: %s", res.Content)
	}

	// calculator：L0 计算工具可用（内置表达式计算器）
	calcArgs, _ := json.Marshal(map[string]string{"expression": "2+3"})
	res, err = reg.Execute(ctx, schema.ToolCall{Name: "calculator", Arguments: calcArgs}, true)
	if err != nil {
		t.Fatalf("calculator 执行失败: %v", err)
	}
	if res.Content != "5" {
		t.Fatalf("calculator 输出 = %q, want 5", res.Content)
	}
}
