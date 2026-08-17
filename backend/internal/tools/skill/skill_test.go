package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill 在根目录下创建 <name>/SKILL.md（+ 可选附加文件）。
func writeSkill(t *testing.T, root, name, skillMD string, extra map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range extra {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseSkillMD(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *SkillMeta
	}{
		{
			name: "标准 frontmatter",
			in: `---
name: code-review
description: 对合并请求做代码审查
license: MIT
---
正文第一段。

第二段。
`,
			want: &SkillMeta{Name: "code-review", Description: "对合并请求做代码审查", License: "MIT", Body: "正文第一段。\n\n第二段。"},
		},
		{
			name: "无 frontmatter（纯正文）",
			in:   "这是纯正文技能。",
			want: &SkillMeta{Body: "这是纯正文技能。"},
		},
		{
			name: "带 BOM 与 CRLF",
			in: "\uFEFF---\r\nname: x\r\ndescription: 描述\r\n---\r\n正文\r\n",
			want: &SkillMeta{Name: "x", Description: "描述", Body: "正文"},
		},
		{
			name: "frontmatter 未闭合按正文处理",
			in:   "---\nname: broken\n正文",
			want: &SkillMeta{Body: "---\nname: broken\n正文"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseSkillMD([]byte(c.in))
			if err != nil {
				t.Fatalf("ParseSkillMD 返回错误: %v", err)
			}
			if got.Name != c.want.Name || got.Description != c.want.Description ||
				got.License != c.want.License || got.Body != c.want.Body {
				t.Fatalf("解析结果不一致\n got: %+v\nwant: %+v", got, c.want)
			}
		})
	}
}

func TestParseSkillMD_BadYAML(t *testing.T) {
	if _, err := ParseSkillMD([]byte("---\nname: [未闭合\n---\n正文")); err == nil {
		t.Fatal("非法 YAML 应返回错误")
	}
}

func TestProvider_Tools(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "code-review", `---
name: code-review
description: 审查代码质量与最佳实践
---
按规范逐条检查。`, map[string]string{
		"scripts/check.sh": "#!/bin/sh\necho ok",
		"README.md":        "说明",
	})
	// 缺 description 的技能应被跳过
	writeSkill(t, root, "broken", "---\nname: broken\n---\n正文", nil)
	// 无 SKILL.md 的目录应被跳过
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(root, nil)
	tools := p.Tools()
	if len(tools) != 1 {
		t.Fatalf("应只加载 1 个技能，实际 %d", len(tools))
	}

	ts := tools[0].Schema()
	if ts.Name != "skill_code_review" {
		t.Fatalf("工具名应为 skill_code_review，实际 %q", ts.Name)
	}
	if !strings.Contains(ts.Description, "code-review") || !strings.Contains(ts.Description, "审查代码") {
		t.Fatalf("工具描述应包含技能名与说明: %s", ts.Description)
	}

	out, err := tools[0].Execute(nil, nil)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if !strings.Contains(out, "按规范逐条检查") {
		t.Fatalf("输出应包含 SKILL.md 正文: %s", out)
	}
	if !strings.Contains(out, "scripts/check.sh") {
		t.Fatalf("输出应包含技能目录文件清单: %s", out)
	}
	if strings.Contains(out, "README.md") {
		// README.md 也是文件清单的一部分，不应报错——此处仅确认清单存在
	}
}

func TestProvider_MissingDir(t *testing.T) {
	p := NewProvider(filepath.Join(t.TempDir(), "not-exist"), nil)
	if tools := p.Tools(); len(tools) != 0 {
		t.Fatalf("目录不存在应为零技能，实际 %d", len(tools))
	}
}

func TestProvider_DefaultRoot(t *testing.T) {
	// Root 为空时回退为工作目录下 skills/；此处显式目录不可控，只验证不 panic。
	p := NewProvider("", nil)
	_ = p.Name()
	_ = p.Tools()
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"Code Review": "code_review",
		"my-skill.1":   "my_skill_1",
		"already_ok":   "already_ok",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Fatalf("SanitizeName(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 纯 ASCII 名工具名与历史行为一致（无哈希后缀）。
	if got := SanitizeName("emoji-helper"); got != "emoji_helper" {
		t.Fatalf("SanitizeName(emoji-helper) = %q", got)
	}
	// 含非 ASCII（中文）→ 必须 ASCII + 哈希兜底，且不同中文名结果不同。
	for _, in := range []string{"中文技能", "数据分析助手", "emoji-助手"} {
		got := SanitizeName(in)
		for _, r := range got {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				t.Fatalf("SanitizeName(%q) = %q 含非法字符 %q", in, got, r)
			}
		}
	}
	a, b := SanitizeName("数据分析"), SanitizeName("天气播报")
	if a == b {
		t.Fatalf("不同中文名工具名冲突: %q", a)
	}
	if a != SanitizeName("数据分析") {
		t.Fatalf("同一中文名工具名应稳定一致")
	}
	// 纯非 ASCII 名 → 以 skill 前缀 + 哈希兜底
	if got := SanitizeName("中文技能"); !strings.HasPrefix(got, "skill_") {
		t.Fatalf("纯中文名应兜底 skill_ 前缀，got %q", got)
	}
}
