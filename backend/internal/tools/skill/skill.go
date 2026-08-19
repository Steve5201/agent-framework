package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// maxSkillBytes 单份 SKILL.md 读取上限（2MB）：防异常大文件撑爆上下文。
// 现代模型上下文窗口大，开源技能（如 slide-maker）SKILL.md 常达几百 KB，
// 因此从 64KB 放宽到 2MB；读取后仍受上下文窗口自然限制。
const maxSkillBytes = 2 << 20

// Provider Skill 工具提供者：扫描技能目录，把每个技能注册为一个工具。
//
// 目录结构约定（见包注释）：<Root>/<skill-name>/SKILL.md。Root 为空时
// 回退为工作目录下 skills/（与 file_ops 工作目录边界一致，模型可继续用
// file_ops/code_executor 访问技能目录内的脚本与资源）。
type Provider struct {
	// Root 技能根目录；空 = <workdir>/skills。
	Root string
	// log 可选：nil 时静默跳过异常技能（不记日志）。
	log *zap.Logger
}

// NewProvider 创建 Skill 提供者。Root 空 = 工作目录下 skills/。
func NewProvider(root string, log *zap.Logger) *Provider {
	if root == "" {
		root = filepath.Join(defaultWorkdir(), "skills")
	}
	return &Provider{Root: root, log: log}
}

// defaultWorkdir 运行时工作目录（容器内为 /app，与 file_ops 同边界）。
func defaultWorkdir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// Name 实现 tools.ToolProvider 接口。
func (p *Provider) Name() string { return "skill" }

// Tools 实现 tools.ToolProvider 接口：扫描技能目录并构建技能工具。
//
// 容错策略：目录不存在 = 零技能（正常）；单个技能解析失败 = 跳过并记
// 警告日志（不拖垮其它技能），因此本方法不返回错误。
func (p *Provider) Tools() []tool.Tool {
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		p.warn("读取技能目录失败，技能未加载", zap.String("dir", p.Root), zap.Error(err))
		return nil
	}

	// 按目录名排序，保证注册顺序稳定（工具列表展示一致）。
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	out := make([]tool.Tool, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue // 忽略技能目录外的散落文件
		}
		dir := filepath.Join(p.Root, e.Name())
		// 禁用的技能：目录内存在 .disabled 标记文件（管理端开关写入）。
		if _, err := os.Stat(filepath.Join(dir, ".disabled")); err == nil {
			p.warn("技能已禁用，跳过", zap.String("skill", e.Name()))
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			p.warn("技能缺少 SKILL.md，已跳过", zap.String("skill", e.Name()), zap.Error(err))
			continue
		}
		meta, err := ParseSkillMD(data)
		if err != nil {
			p.warn("技能 SKILL.md 解析失败，已跳过", zap.String("skill", e.Name()), zap.Error(err))
			continue
		}
		if err := meta.Validate(e.Name()); err != nil {
			p.warn("技能元数据不合法，已跳过", zap.String("skill", e.Name()), zap.Error(err))
			continue
		}
		out = append(out, &skillTool{root: p.Root, dir: dir, meta: meta})
	}
	return out
}

func (p *Provider) warn(msg string, fields ...zap.Field) {
	if p.log != nil {
		p.log.Warn(msg, fields...)
	}
}

// skillTool 单个技能工具：调用后返回 SKILL.md 完整指引 + 目录文件清单，
// 模型据此用 file_ops/code_executor 完成实际动作。
type skillTool struct {
	root string // 技能根目录（用于计算相对路径）
	dir  string // 技能目录
	meta *SkillMeta
}

// Schema 实现 tool.Tool 接口：name = skill_<净化名>，L1 只读。
func (t *skillTool) Schema() schema.ToolSchema {
	// @skills/ 虚拟路径必须与 file_ops 一致：用真实目录相对路径（可能含中文/连字符），
	// 而不是净化后的工具名（两者在中文名/连字符名下会错位）。
	path := t.meta.Name
	if rel, err := filepath.Rel(t.root, t.dir); err == nil {
		path = filepath.ToSlash(rel)
	}
	// 展示名：display_name 优先（前端 UI 用），空则回退 name。工具名始终基于
	// name 生成（稳定），展示名独立不影响引用。
	display := t.meta.Name
	if t.meta.DisplayName != "" {
		display = t.meta.DisplayName
	}
	return schema.ToolSchema{
		Name: "skill_" + SanitizeName(t.meta.Name),
		Description: fmt.Sprintf("技能【%s】：%s。调用后返回该技能的使用指引（SKILL.md）与目录文件清单（@skills/%s/… 虚拟路径，每项附文件大小）。按指引用 file_ops 读取 @skills/<技能名>/ 下的文档/脚本（read 操作），需要执行脚本时用 file_ops 读取脚本内容后用 code_executor 执行其代码完成任务。注意：清单中标明的大小决定文件能否整读——超过 50KB 的（如大型 scripts/*.py 源码）整读会注入海量 token 撑爆上下文，只按 SKILL.md 指引使用、不要 read 全文；小文件可直接 read。",
			display, t.meta.Description, path),
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"instructions":{"type":"string","description":"可选：本次任务的具体要求/补充说明，帮助按需调整执行方式"}
			}
		}`),
		Permission: schema.PermissionL1Read,
	}
}

// Execute 实现 tool.Tool 接口：读取 SKILL.md 并列出技能目录文件。
func (t *skillTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	data, err := os.ReadFile(filepath.Join(t.dir, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("skill: 读取 %s/SKILL.md 失败: %w", t.meta.Name, err)
	}
	if len(data) > maxSkillBytes {
		data = data[:maxSkillBytes]
	}
	meta := t.meta
	// 直接返回 frontmatter+正文（完整指引），不依赖 ParseSkillMD 二次解析。
	guide := string(data)

	files, err := listSkillFiles(t.root, t.dir)
	if err != nil {
		return "", fmt.Errorf("skill: 列出技能文件失败: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "技能 %s 使用指引（技能名：%s%s）：\n\n", meta.Name, meta.Name, t.versionSuffix(meta))
	b.WriteString(guide)
	if len(guide) >= maxSkillBytes {
		fmt.Fprintf(&b, "\n……（SKILL.md 超过 %dKB，仅显示前 %d 字节）\n", maxSkillBytes>>10, maxSkillBytes)
	}
	fmt.Fprintf(&b, "\n\n技能目录文件清单（@skills/ 虚拟路径，可用 file_ops 读取内容 / 列目录）：\n")
	if len(files) == 0 {
		b.WriteString("（无其它文件）\n")
	}
	for _, f := range files {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	fmt.Fprintf(&b, "\n提示：SKILL.md 内以相对路径引用的文件（如 ref/x.md）对应上述 @skills/<技能名>/ 开头的完整路径；读取或执行前请以清单中的路径为准。\n")
	return b.String(), nil
}

// versionSuffix 返回版本号后缀（用于展示，如 "，版本 1.0.0"）。
func (t *skillTool) versionSuffix(meta *SkillMeta) string {
	if meta.Version == "" {
		return ""
	}
	return "，版本 " + meta.Version
}

// listSkillFiles 列出技能目录内全部文件（@skills/<技能名>/<相对技能目录> 虚拟路径），
// 递归、跳过 SKILL.md 与管理端内部路径（.versions/.disabled 等隐藏项）。
// 清单路径与 file_ops 的 @skills/ 命名空间一一对应，模型可直接回填读取。
// 每项附文件大小（如 `xxx (527KB)`），模型据此识别大文件避免整读撑爆上下文。
func listSkillFiles(root, dir string) ([]string, error) {
	relDir, err := filepath.Rel(root, dir)
	if err != nil {
		return nil, err
	}
	base := tools.SkillsPathPrefix + filepath.ToSlash(relDir)

	var out []string
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			// 跳过管理端内部目录（版本历史等）
			if d.Name() != "." && strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil // 跳过 .disabled 等隐藏标记文件
		}
		if d.Name() == "SKILL.md" {
			return nil // 清单不列主文件本身（指引已内联）
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		entry := base + "/" + filepath.ToSlash(rel)
		if fi, ierr := d.Info(); ierr == nil && !fi.IsDir() {
			entry += fmt.Sprintf(" (%dKB)", (fi.Size()+1023)>>10)
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// SanitizeName 生成工具名（注册用，必须纯 ASCII [a-z0-9_]）。
//
// 策略：ASCII 字母/数字/下划线保留（小写），其余字符转下划线后去除首尾；
// 若结果为空、或原技能名含非 ASCII 字符（如中文）→ 追加 8 位十六进制哈希
// 保证工具名唯一（skill_<slug>_<hash8>）。哈希只对非 ASCII 名触发，
// 纯 ASCII 名工具名与历史行为一致，不破坏既有注册名。
func SanitizeName(s string) string {
	var b strings.Builder
	hasNonASCII := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r > 127:
			hasNonASCII = true
			b.WriteByte('_')
		default:
			b.WriteByte('_')
		}
	}
	out := strings.ToLower(strings.Trim(b.String(), "_"))
	if out == "" || hasNonASCII {
		if out == "" {
			out = "skill"
		}
		sum := sha256.Sum256([]byte(s))
		out = out + "_" + hex.EncodeToString(sum[:4])
	}
	return out
}

// 编译期断言：Provider 实现 ToolProvider，skillTool 实现 tool.Tool。
var (
	_ tools.ToolProvider = (*Provider)(nil)
	_ tool.Tool          = (*skillTool)(nil)
)
