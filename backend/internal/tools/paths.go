package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// 路径安全（需求约束：file_ops 路径必须限制在 os.Getwd() 下）。
//
// 本文件是"工作目录边界"的唯一实现，被三处共用，保证边界一致：
//   - file_ops 工具：智能体读写文件；
//   - code_executor 工具：执行代码时的工作目录；
//   - agent 服务的 HTTP /files 端点：前端渲染本地媒体。
//
// 防护手段：
//  1. 拼接 + Clean 后，用 filepath.Rel 校验结果仍在根目录内（防 ../ 逃逸）；
//  2. 符号链接逃逸：目标已存在时 EvalSymlinks 解析真实路径再校验一次
//     （防止根目录内的软链指向外部敏感文件）；
//  3. 根目录本身若为软链，先解析到真实目录再比较。
func ResolveInRoot(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("tools: 路径不能为空")
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("tools: 解析工作目录 %q 失败: %w", root, err)
	}
	// 根目录为软链时，先解析到真实目录，避免比较基准失真。
	if real, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = real
	}

	var full string
	if filepath.IsAbs(p) {
		full = filepath.Clean(p)
	} else {
		full = filepath.Join(rootAbs, p)
	}
	full = filepath.Clean(full)

	rel, err := filepath.Rel(rootAbs, full)
	if err != nil {
		return "", fmt.Errorf("tools: 路径 %q 非法: %v", p, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("tools: 路径 %q 超出工作目录 %q，禁止访问", p, rootAbs)
	}

	// 符号链接逃逸防护：目标存在时，其真实路径也必须落在根目录内。
	if real, err := filepath.EvalSymlinks(full); err == nil {
		rel2, err2 := filepath.Rel(rootAbs, real)
		if err2 != nil || rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("tools: 路径 %q 指向工作目录之外的软链目标，禁止访问", p)
		}
	}
	return full, nil
}

// ResolveWorkPath 便捷入口：根目录取当前进程的工作目录（os.Getwd()）。
// file_ops 工具与 /files HTTP 服务都通过它解析路径。
func ResolveWorkPath(p string) (string, error) {
	root, err := filepath.Abs(".")
	if err != nil {
		return "", fmt.Errorf("tools: 获取工作目录失败: %w", err)
	}
	return ResolveInRoot(root, p)
}

// SkillsPathPrefix 技能资源的虚拟路径前缀（file_ops / skill 工具共用）。
//
// 技能文件位于技能根目录（默认 <工作目录>/skills），而 file_ops 默认只解析
// 用户工作区（用户隔离时 = <工作目录>/users/<uid>），模型按普通相对路径
// 永远够不到技能资源——因此约定一个显式虚拟命名空间 @skills/<技能名>/…，
// file_ops 对它按"技能根目录 + 只读"解析，skill 工具返回的文件清单也统一
// 用该前缀，保证"清单里的路径"与"file_ops 能读到的路径"完全一致。
const SkillsPathPrefix = "@skills/"

// IsSkillsPath 判断路径是否指向技能虚拟命名空间（@skills 或 @skills/…）。
func IsSkillsPath(p string) bool {
	return p == SkillsPathPrefix[:len(SkillsPathPrefix)-1] || strings.HasPrefix(p, SkillsPathPrefix)
}

// ResolveSkillsPath 把 @skills/<技能名>/<相对路径> 解析为技能根目录下的绝对路径。
// 复用 ResolveInRoot 的安全校验（防 ../ 与软链逃逸），返回物理路径。
// 非技能路径返回错误（调用方应先 IsSkillsPath 判断）。
func ResolveSkillsPath(skillsRoot, p string) (string, error) {
	if skillsRoot == "" {
		return "", fmt.Errorf("tools: 技能目录未配置，无法访问 %s 资源", SkillsPathPrefix)
	}
	rest := strings.TrimPrefix(p, SkillsPathPrefix)
	if strings.TrimSpace(rest) == "" {
		rest = "."
	}
	return ResolveInRoot(skillsRoot, rest)
}
