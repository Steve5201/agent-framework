// Package skill 实现 Skill 能力源（Anthropic Agent Skills 开放格式）。
//
// Skill 目录标准（与 Claude 生态通用的 Agent Skills 格式一致）：
//
//	skills/
//	  my-skill/
//	    SKILL.md          ← 必填：YAML frontmatter（name/description）+ 正文使用指引
//	    scripts/          ← 可选：可执行脚本（agent 用 code_executor 运行）
//	    assets/           ← 可选：静态资源
//	    requirements.txt  ← 可选：脚本依赖说明
//
// SKILL.md 结构：
//
//	---
//	name: my-skill
//	description: 一句话说明该技能解决什么问题、何时使用
//	license: MIT            ← 可选
//	metadata:               ← 可选
//	  version: 1.0.0
//	---
//	正文：给模型的使用指引（怎么做、注意事项、脚本用法）
//
// 执行模型（业界标准）：Skill 本质是"给模型的指令包"——模型调用 skill
// 工具拿到完整指引与目录结构，然后按指引用 file_ops 读脚本、用
// code_executor 执行脚本完成实际动作，与 Claude 的 skill 工作流一致。
package skill

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SemVerRe 语义版本号正则（严格 x.y.z 三段，如 1.0.0 / 2.3.4）。
// 版本号是同一技能多版本管理的唯一标识：同版本号内容不同 → 冲突。
var SemVerRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// SkillMeta SKILL.md 解析结果：frontmatter 元数据 + 正文。
type SkillMeta struct {
	// Name 技能名（frontmatter name；缺省回退为目录名）。
	Name string `yaml:"name"`
	// DisplayName 可选：技能展示名（frontmatter display_name）。用于前端 UI
	// 展示友好中文名；空 = 回退用 Name。不影响工具名（工具名始终基于 Name
	// 生成，保持稳定引用），仅影响"技能【X】"描述里的展示名。
	DisplayName string `yaml:"display_name"`
	// Description 一句话描述，写给模型判断何时调用。
	Description string `yaml:"description"`
	// License 可选：技能授权声明。
	License string `yaml:"license"`
	// Version 可选：语义化版本号（frontmatter 顶层 version 或 metadata.version）。
	// 用于同一技能多版本区分与管理端展示；缺省为空串。
	Version string `yaml:"version"`
	// Metadata 可选：Anthropic Agent Skills 规范元数据对象。
	Metadata skillMetadata `yaml:"metadata"`
	// Body SKILL.md 正文（frontmatter 之后的内容，去首尾空白）。
	Body string
}

// skillMetadata 规范元数据（metadata: { author, version, license, ... }）。
type skillMetadata struct {
	Version string `yaml:"version"`
}

// ParseSkillMD 解析 SKILL.md 内容。
//
// frontmatter 规则（与主流 Markdown frontmatter 一致）：文件以 "---" 开头，
// 到下一个 "---" 之间的内容按 YAML 解析；无 frontmatter 时视为纯正文，
// Name 留空由调用方回退为目录名。
func ParseSkillMD(data []byte) (*SkillMeta, error) {
	body := string(data)
	body = strings.TrimPrefix(body, "\uFEFF") // 去 BOM
	m := &SkillMeta{}

	if !strings.HasPrefix(body, "---") {
		m.Body = strings.TrimSpace(body)
		return m, nil
	}

	// 定位第二个 "---" 行作为 frontmatter 结束。
	rest := strings.TrimPrefix(body, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		// 无闭合 frontmatter：视为整段正文。
		m.Body = strings.TrimSpace(body)
		return m, nil
	}
	raw := rest[:idx]
	after := rest[idx+len("\n---"):]
	after = strings.TrimPrefix(after, "\r\n")
	after = strings.TrimLeft(after, "\r\n")

	if err := yaml.Unmarshal([]byte(raw), m); err != nil {
		return nil, fmt.Errorf("skill: SKILL.md frontmatter 解析失败: %w", err)
	}
	// 版本号统一语义：metadata.version 优先（Anthropic 规范），回退顶层 version。
	// 两者都缺失 = 无版本号（管理端可据此提示用户补充，或回退为整数版本）。
	if strings.TrimSpace(m.Metadata.Version) != "" {
		m.Version = strings.TrimSpace(m.Metadata.Version)
	}
	m.Version = strings.TrimSpace(m.Version)
	m.Body = strings.TrimSpace(after)
	return m, nil
}

// Validate 校验 skill 是否可用：name 与 description 必填
// （name 缺省可用 dirName 兜底后再校验）。
func (m *SkillMeta) Validate(dirName string) error {
	if m.Name == "" {
		m.Name = dirName
	}
	if m.Name == "" {
		return fmt.Errorf("skill: name 缺失（frontmatter 或目录名均为空）")
	}
	if m.Description == "" {
		return fmt.Errorf("skill: %q 缺少 description（模型无法判断何时使用）", m.Name)
	}
	if m.Body == "" {
		return fmt.Errorf("skill: %q 的 SKILL.md 正文为空（无使用指引）", m.Name)
	}
	return nil
}

// ValidVersion 版本号是否为合法语义版本号（x.y.z）。空串返回 false。
func (m *SkillMeta) ValidVersion() bool { return SemVerRe.MatchString(m.Version) }
