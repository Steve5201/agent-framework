package builtin

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func codeExecArgs(lang, code string, timeout int) json.RawMessage {
	m := map[string]any{"language": lang, "code": code}
	if timeout > 0 {
		m["timeout_seconds"] = timeout
	}
	b, _ := json.Marshal(m)
	return b
}

func TestCodeExecutorTool_Blacklist(t *testing.T) {
	tool := &CodeExecutorTool{}
	ctx := context.Background()
	cases := []struct{ lang, code string }{
		{"shell", "rm -rf /tmp/x"},
		{"shell", "sudo apt update"},
		{"shell", "mkfs.ext4 /dev/sda"},
		{"shell", "echo hi | dd of=/dev/sda"},
		{"shell", "shutdown -h now"},
		{"shell", ":(){ :|:& };:"},
		{"python", "import os\nos.system('rm -rf /')"},
	}
	for _, c := range cases {
		_, err := tool.Execute(ctx, codeExecArgs(c.lang, c.code, 0))
		if err == nil || !strings.Contains(err.Error(), "黑名单") {
			t.Errorf("代码 %q 应被黑名单拒绝，实际 err=%v", c.code, err)
		}
	}
}

func TestCodeExecutorTool_UnknownLanguage(t *testing.T) {
	_, err := (&CodeExecutorTool{}).Execute(context.Background(), codeExecArgs("javascript", "console.log(1)", 0))
	if err == nil || !strings.Contains(err.Error(), "未知 language") {
		t.Fatalf("未知语言应报错，实际 err=%v", err)
	}
}

func TestCodeExecutorTool_DevRedirect(t *testing.T) {
	// 放行：/dev/null、/dev/urandom 等字符设备是安全常见用法（如丢弃 stderr）。
	for _, code := range []string{
		`fc-list :lang=zh 2>/dev/null | head -20`,
		`ls /usr/share/fonts/ 2>/dev/null`,
		`echo hi > /dev/null`,
		`find / -name "*.ttc" 2>/dev/null | head`,
	} {
		if hit := CheckDangerousCommand(code); hit != "" {
			t.Errorf("代码 %q 应放行，实际被拒: %s", code, hit)
		}
	}
	// 拦截：向真实块设备（磁盘/分区）重定向写入是危险操作。
	for _, code := range []string{
		`echo x > /dev/sda`,
		`cat x > /dev/nvme0n1p1`,
		`echo x >/dev/mapper/data`,
		`echo x > /dev/mmcblk0`,
		`echo x >/dev/loop1`,
	} {
		if hit := CheckDangerousCommand(code); hit == "" {
			t.Errorf("代码 %q 应被拦截（块设备重定向），实际放行", code)
		}
	}
}

func TestCodeExecutorTool_Allowlist(t *testing.T) {
	ctx := context.Background()
	// 配置白名单：仅允许 echo / python print。
	tool := &CodeExecutorTool{Allowlist: []string{`\becho\b`, `^print\(`}}

	// 命中白名单 → 不被白名单拒绝（无解释器时后续可能报缺解释器，但非白名单拦截）。
	if _, err := tool.Execute(ctx, codeExecArgs("shell", "echo hi", 0)); err != nil && strings.Contains(err.Error(), "白名单") {
		t.Errorf("命中白名单的 echo 不应被白名单拒绝，实际 err=%v", err)
	}
	if _, err := tool.Execute(ctx, codeExecArgs("python", "print(1+1)", 0)); err != nil && strings.Contains(err.Error(), "白名单") {
		t.Errorf("命中白名单的 print 不应被白名单拒绝，实际 err=%v", err)
	}

	// 未命中白名单且不在黑名单 → 明确拒绝（白名单拦截优先于解释器校验）。
	_, err := tool.Execute(ctx, codeExecArgs("shell", "date", 0))
	if err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("未命中白名单的命令应被拒绝，实际 err=%v", err)
	}

	// 空白名单（默认）= 不限制，date 不被白名单拦截（走正常执行流程）。
	empty := &CodeExecutorTool{}
	if _, err := empty.Execute(ctx, codeExecArgs("shell", "date", 0)); err != nil && strings.Contains(err.Error(), "白名单") {
		t.Fatalf("空白名单不应限制命令，实际 err=%v", err)
	}
}

func TestCodeExecutorTool_SuccessShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("当前环境无 sh 解释器，跳过")
	}
	root := t.TempDir()
	tool := &CodeExecutorTool{Root: root}
	out, err := tool.Execute(context.Background(), codeExecArgs("shell", "echo hello-builtin", 0))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "hello-builtin") || !strings.Contains(out, "退出码：0") {
		t.Fatalf("输出异常: %s", out)
	}
}

func TestCodeExecutorTool_Timeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("当前环境无 sh 解释器，跳过")
	}
	tool := &CodeExecutorTool{DefaultTimeout: 1 * time.Second}
	_, err := tool.Execute(context.Background(), codeExecArgs("shell", "sleep 5", 0))
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时执行应报错，实际 err=%v", err)
	}
}
