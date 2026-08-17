package adminsvc

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/tools/skill"
)

// maxSkillBytes 单份 SKILL.md 写入上限（2MB，与 agent 读取上限一致）。
const maxSkillBytes = 2 << 20

// versionsDirName 版本历史目录名（技能目录内隐藏目录 .versions）。
const versionsDirName = ".versions"

// disabledFileName 禁用标记文件名（存在 = 技能禁用，agent 扫描时跳过）。
const disabledFileName = ".disabled"

// skillNameRe 技能目录名合法字符：中文/字母/数字/下划线/连字符，首字符须为
// 中文/字母/数字。排除了点与路径分隔符 → 天然防目录穿越；目录名是技能唯一
// 标识，也是工具名来源（非 ASCII 名由 SanitizeName 哈希成唯一 ASCII 工具名）。
var skillNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_-]{0,49}$`)

// ---------------------------------------------------------------------------
// 存储层
// ---------------------------------------------------------------------------

// SkillStore 文件态技能存储：技能 = <root>/<name>/SKILL.md（Anthropic Agent Skills）。
type SkillStore struct {
	root     string
	maxBytes int64      // zip 上传/解压单文件大小上限（字节，P4-L env 可配）
	mu       sync.Mutex // 写操作串行化（建/删/改互斥，防并发覆盖）
}

// newSkillStore 创建技能存储。
func newSkillStore(root string) *SkillStore {
	return &SkillStore{root: root, maxBytes: int64(defaultSkillUploadMaxMB) << 20}
}

// For 返回指定智能体域的子存储：技能 = <root>/<agentID>/<name>/SKILL.md。
// agentID 须已通过 agentScopeFor 的白名单校验（防目录穿越）；空值视同默认域。
// 语义：SkillStore 表示"单个智能体的技能目录"，For 从顶层根目录切片出该域。
func (s *SkillStore) For(agentID string) *SkillStore {
	if agentID == "" {
		agentID = defaultAgentID
	}
	return &SkillStore{root: filepath.Join(s.root, agentID), maxBytes: s.maxBytes}
}

// Skill 技能对外视图（管理端 JSON 契约）。
type Skill struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	License     string         `json:"license,omitempty"`
	SemVer      string         `json:"semver,omitempty"` // frontmatter 语义版本号（metadata.version/version，可选）
	ToolName    string         `json:"tool_name"`        // 注册的工具名（skill_<净化名>）
	Content     string         `json:"content"`          // SKILL.md 完整内容（frontmatter + 正文）
	FileCount   int            `json:"file_count"`       // 目录内其它文件数（不含 SKILL.md 与内部文件）
	Files       []string       `json:"files,omitempty"`  // 目录内其它文件的相对路径（含子目录，便于验证结构与引用）
	UpdatedAt   time.Time      `json:"updated_at"`
	Valid       bool           `json:"valid"` // 解析是否通过（无效技能在列表中仍可见，供修复）
	Error       string         `json:"error,omitempty"`
	Enabled     bool           `json:"enabled"`            // 是否启用（禁用 = agent 不注册其工具）
	Version     int            `json:"version"`            // 当前生效版本号（从 1 起，内部槽序号/兜底展示）
	Versions    []SkillVersion `json:"versions,omitempty"` // 历史版本（不含当前，按语义版本号倒序）
}

// SkillVersion 技能历史版本元信息（内容存于 <dir>/.versions/v<N>/SKILL.md）。
// 版本身份 = 语义版本号：同一技能（name）下同一 semver 只能有一份。
type SkillVersion struct {
	SemVer    string    `json:"semver"` // 该历史版本的语义版本号（frontmatter 解析）
	UpdatedAt time.Time `json:"updated_at"`
	Size      int       `json:"size"`
}

// dirPath 校验技能名并返回其目录路径。名字不合法直接拒绝（防穿越）。
func (s *SkillStore) dirPath(name string) (string, error) {
	if !skillNameRe.MatchString(name) {
		return "", apperr.New(apperr.CodeInvalidArgument,
			"技能名仅支持中文/字母/数字/下划线/连字符，且首字符须为中文/字母/数字（1~50 字符）")
	}
	return filepath.Join(s.root, name), nil
}

// List 列出全部技能（目录按名称排序，输出稳定）。
// 目录不存在 = 空列表；单个技能无效不会中断整体（列表中标注 valid:false）。
func (s *SkillStore) List(_ context.Context) ([]Skill, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, apperr.Wrap(apperr.CodeInternal, "读取技能目录失败", err)
	}
	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sk, _ := s.readDir(e.Name()) // 单个技能读取失败在 readDir 内已降级为 invalid 条目
		out = append(out, sk)
	}
	return out, nil
}

// Get 获取单个技能；不存在返回 404。
func (s *SkillStore) Get(_ context.Context, name string) (Skill, error) {
	dir, err := s.dirPath(name)
	if err != nil {
		return Skill{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return Skill{}, apperr.New(apperr.CodeNotFound, "技能不存在")
		}
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "访问技能目录失败", err)
	}
	return s.readDir(name)
}

// Create 创建新技能（目录 + SKILL.md，当前版本 = frontmatter 语义版本号）。
//
// 版本化语义（用户需求：同一技能通过文件里的版本号区分）：
//   - 技能不存在 → 新建；要求 frontmatter 含 name/description/正文 + 合法语义版本号；
//   - 技能已存在 → 拒绝（ALREADY_EXISTS）。同名新版本请走 Update（编辑）或
//     Upload（zip），两者都按版本号语义处理（不同版本=新版本，同版本=覆盖/拒绝）。
func (s *SkillStore) Create(_ context.Context, name, content string) (Skill, error) {
	dir, err := s.dirPath(name)
	if err != nil {
		return Skill{}, err
	}
	if _, err := validateNewSkillContent(name, content); err != nil {
		return Skill{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(dir); err == nil {
		return Skill{}, apperr.New(apperr.CodeAlreadyExists,
			fmt.Sprintf("技能「%s」已存在。如需更新内容请编辑该技能（改版本号 = 发布新版本，同版本号需确认覆盖）。", name))
	} else if !os.IsNotExist(err) {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "访问技能目录失败", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "创建技能目录失败", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "写入 SKILL.md 失败", err)
	}
	return s.readDir(name)
}

// Update 全量更新技能内容，按版本号语义决定"新版本 / 幂等 / 版本冲突"。
//   - 内容与当前相同 → 幂等返回（不产生历史）；
//   - 新版本号 ≠ 当前 → 快照旧内容到 .versions/v<N> 后替换（正常发布）；
//   - 新版本号 == 当前且内容不同 → 默认返回 VERSION_CONFLICT，overwrite=true 时覆盖；
//   - 不存在返回 404。update 对历史无版本号的旧技能保持兼容（不做冲突判定）。
func (s *SkillStore) Update(_ context.Context, name, content string, overwrite bool) (Skill, error) {
	dir, err := s.dirPath(name)
	if err != nil {
		return Skill{}, err
	}
	if _, err := validateSkillContent(name, content); err != nil {
		return Skill{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return Skill{}, apperr.New(apperr.CodeNotFound, "技能不存在")
		}
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "访问技能目录失败", err)
	}
	old, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "读取当前 SKILL.md 失败", err)
	}
	newMeta, err := validateNewSkillContent(name, content)
	if err != nil {
		// 可能是无版本号的历史技能兼容编辑。仅当版本号完全缺失时放行；
		// 版本号存在但不合法 → 明确拒绝（版本号是版本管理标识，必须规范）。
		meta2, verr := validateSkillContent(name, content)
		if verr != nil {
			return Skill{}, verr
		}
		if meta2.Version != "" {
			return Skill{}, apperr.New(apperr.CodeInvalidArgument,
				fmt.Sprintf("技能 %q 的版本号 %q 不合法，须为 x.y.z 语义版本号", name, meta2.Version))
		}
		newMeta = meta2
	}
	if err := s.applyVersion(dir, string(old), content, newMeta, overwrite, false); err != nil {
		return Skill{}, err
	}
	return s.readDir(name)
}

// Delete 删除整个技能目录；不存在返回 404。
func (s *SkillStore) Delete(_ context.Context, name string) error {
	dir, err := s.dirPath(name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return apperr.New(apperr.CodeNotFound, "技能不存在")
		}
		return apperr.Wrap(apperr.CodeInternal, "访问技能目录失败", err)
	}
	if err := removeDirAll(dir); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "删除技能目录失败", err)
	}
	return nil
}

// removeDirAll 递归删除目录（先删内容、再删目录本身）。
// 语义与 os.RemoveAll 一致，但通过显式 os.Remove 逐个删除：
// 在 Windows 及部分受限文件系统上，RemoveAll 对"刚写入的目录"
// 可能因句柄短暂占用而静默失败，两步走更可靠。
func removeDirAll(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if err := removeDirAll(p); err != nil {
				return err
			}
		} else if err := os.Remove(p); err != nil {
			return err
		}
	}
	return os.Remove(dir)
}

// readDir 读取并解析单个技能目录。SKILL.md 缺失或解析失败 → 降级为
// valid:false 的条目（不返回错误），保证列表对无效技能可见、可修复。
func (s *SkillStore) readDir(name string) (Skill, error) {
	dir := filepath.Join(s.root, name)
	path := filepath.Join(dir, "SKILL.md")

	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{Name: name, Valid: false, Error: "缺少 SKILL.md 或读取失败"}, nil
	}
	sk := Skill{Name: name, Content: string(data)}
	if fi, err := os.Stat(path); err == nil {
		sk.UpdatedAt = fi.ModTime()
	}

	// 启用状态：.disabled 标记文件存在 = 禁用。
	if _, err := os.Stat(filepath.Join(dir, disabledFileName)); err == nil {
		sk.Enabled = false
	} else {
		sk.Enabled = true
	}
	// 版本信息：当前版本号 + 历史版本列表。
	sk.Version = s.currentVersion(dir)
	sk.Versions = s.readVersions(dir)

	meta, err := skill.ParseSkillMD(data)
	if err != nil {
		sk.Valid, sk.Error = false, "SKILL.md 解析失败: "+err.Error()
		return sk, nil
	}
	if err := meta.Validate(name); err != nil {
		sk.Valid, sk.Error = false, err.Error()
		return sk, nil
	}
	sk.Description = meta.Description
	sk.License = meta.License
	sk.SemVer = meta.Version
	sk.ToolName = "skill_" + skill.SanitizeName(meta.Name)
	sk.Valid = true
	sk.FileCount, sk.Files = countSkillFiles(dir)
	return sk, nil
}

// countSkillFiles 统计技能目录内非 SKILL.md 的文件数并列出相对路径（含子目录），
// 排除管理端内部文件（.versions 版本历史、.disabled 标记等隐藏路径）。
// 返回 (数量, 相对路径列表)。相对路径用于管理端展示/验证 zip 结构是否保留。
func countSkillFiles(dir string) (int, []string) {
	n := 0
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d.IsDir() && d.Name() != "." && strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || filepath.Base(p) == "SKILL.md" {
			return nil
		}
		if rel, rerr := filepath.Rel(dir, p); rerr == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		n++
		return nil
	})
	sort.Strings(files)
	return n, files
}

// ---------------------------------------------------------------------------
// 版本管理（版本身份 = 语义版本号；同一 name 下同一 semver 只能有一份）
// ---------------------------------------------------------------------------

// slotInfo 内部版本槽信息：.versions/v<N>/SKILL.md。
type slotInfo struct {
	slot    int    // 槽序号（v<N>）
	semver  string // 槽内容解析出的语义版本号（frontmatter metadata.version）
	content string // 槽内容
}

// listSlots 扫描 .versions/v<N> 目录，解析每个槽的语义版本号。
// 槽无 SKILL.md 或解析失败 → 跳过（视为损坏槽，不影响其它版本）。
func (s *SkillStore) listSlots(dir string) []slotInfo {
	vdir := filepath.Join(dir, versionsDirName)
	entries, err := os.ReadDir(vdir)
	if err != nil {
		return nil
	}
	var out []slotInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "v%d", &n); err != nil {
			continue
		}
		content, err := os.ReadFile(filepath.Join(vdir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		meta, err := skill.ParseSkillMD(content)
		if err != nil || meta.Version == "" {
			continue // 历史槽必须带语义版本号才算一个版本（旧格式槽忽略）
		}
		out = append(out, slotInfo{slot: n, semver: meta.Version, content: string(content)})
	}
	return out
}

// slotHasSemver 是否已有同版本号的历史槽。
func slotHasSemver(slots []slotInfo, semver string) bool {
	for _, sl := range slots {
		if sl.semver == semver {
			return true
		}
	}
	return false
}

// snapshot 把当前内容写入 .versions/v<N>/SKILL.md 作为历史版本，返回其版本号。
// 版本语义：槽号 = 最大槽号 + 1（无快照时 = 1）。调用方需持有 s.mu。
func (s *SkillStore) snapshot(dir, content string) (int, error) {
	ver := s.currentVersion(dir)
	vdir := filepath.Join(dir, versionsDirName, fmt.Sprintf("v%d", ver))
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(vdir, "SKILL.md"), []byte(content), 0o644); err != nil {
		return 0, err
	}
	return ver, nil
}

// currentVersion 当前生效版本号 = 最大槽号 + 1；无任何槽 = 1。
// 该字段仅作内部槽序号/兜底展示，版本身份以 semver 为准。
func (s *SkillStore) currentVersion(dir string) int {
	max := 0
	entries, err := os.ReadDir(filepath.Join(dir, versionsDirName))
	if err != nil {
		return 1
	}
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "v%d", &n); err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max + 1
}

// readVersions 列出历史版本元信息（不含当前 SKILL.md 本身），按槽号倒序。
func (s *SkillStore) readVersions(dir string) []SkillVersion {
	vdir := filepath.Join(dir, versionsDirName)
	var out []SkillVersion
	for _, sl := range s.listSlots(dir) {
		v := SkillVersion{SemVer: sl.semver}
		if fi, err := os.Stat(filepath.Join(vdir, fmt.Sprintf("v%d", sl.slot), "SKILL.md")); err == nil {
			v.UpdatedAt = fi.ModTime()
			v.Size = int(fi.Size())
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SemVer > out[j].SemVer })
	return out
}

// SetEnabled 启用/禁用技能：创建或删除 .disabled 标记文件。
// 禁用后 agent 热加载即移除该技能工具，无需重启。
func (s *SkillStore) SetEnabled(_ context.Context, name string, enabled bool) (Skill, error) {
	dir, err := s.dirPath(name)
	if err != nil {
		return Skill{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return Skill{}, apperr.New(apperr.CodeNotFound, "技能不存在")
		}
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "访问技能目录失败", err)
	}
	flag := filepath.Join(dir, disabledFileName)
	if enabled {
		if err := os.Remove(flag); err != nil && !os.IsNotExist(err) {
			return Skill{}, apperr.Wrap(apperr.CodeInternal, "启用技能失败", err)
		}
	} else {
		if err := os.WriteFile(flag, []byte("disabled\n"), 0o644); err != nil {
			return Skill{}, apperr.Wrap(apperr.CodeInternal, "禁用技能失败", err)
		}
	}
	return s.readDir(name)
}

// RestoreVersion 回滚技能到指定语义版本号：该版本内容写回当前 SKILL.md，
// 并保留回滚留痕（原当前内容入历史，若其版本号尚未在历史中）。同版本号只留一份
// ——被回滚的版本槽删除（该版本号成为当前）。版本不存在或已删除返回 404。
func (s *SkillStore) RestoreVersion(_ context.Context, name, semver string) (Skill, error) {
	dir, err := s.dirPath(name)
	if err != nil {
		return Skill{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return Skill{}, apperr.New(apperr.CodeNotFound, "技能不存在")
		}
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "访问技能目录失败", err)
	}
	cur, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "读取当前 SKILL.md 失败", err)
	}
	curMeta, _ := skill.ParseSkillMD(cur)
	if curMeta.Version == semver {
		return s.readDir(name) // 当前已是该版本 → 幂等
	}
	slots := s.listSlots(dir)
	var target *slotInfo
	for i := range slots {
		if slots[i].semver == semver {
			target = &slots[i]
			break
		}
	}
	if target == nil {
		return Skill{}, apperr.New(apperr.CodeNotFound, "版本不存在")
	}
	// 回滚留痕：当前内容入历史（版本号去重）。
	if curMeta.Version != "" && !slotHasSemver(slots, curMeta.Version) {
		if _, err := s.snapshot(dir, string(cur)); err != nil {
			return Skill{}, apperr.Wrap(apperr.CodeInternal, "写入版本快照失败", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(target.content), 0o644); err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "回滚写入 SKILL.md 失败", err)
	}
	// 被回滚的版本成为当前 → 删除其历史槽（同版本号只留一份）。
	if err := removeDirAll(filepath.Join(dir, versionsDirName, fmt.Sprintf("v%d", target.slot))); err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "清理版本槽失败", err)
	}
	return s.readDir(name)
}

// validateSkillContent 校验待写入的 SKILL.md：格式 + 必填项 + 命名一致性 + 大小上限。
// 返回解析后的元数据（避免调用方二次解析）。version 为可选项（旧技能/回滚兼容）。
func validateSkillContent(dirName, content string) (*skill.SkillMeta, error) {
	if strings.TrimSpace(content) == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "SKILL.md 内容不能为空")
	}
	if len(content) > maxSkillBytes {
		return nil, apperr.New(apperr.CodeInvalidArgument, fmt.Sprintf("SKILL.md 超过 %d 字节上限", maxSkillBytes))
	}
	meta, err := skill.ParseSkillMD([]byte(content))
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidArgument, "SKILL.md 格式不合法: "+err.Error())
	}
	if err := meta.Validate(dirName); err != nil {
		return nil, apperr.New(apperr.CodeInvalidArgument, err.Error())
	}
	// 一致性约束：frontmatter 的 name（若填写）必须与目录名一致，
	// 否则会出现"目录名 A、工具名 B"的错位，两个技能可能注册成同名工具。
	if meta.Name != "" && meta.Name != dirName {
		return nil, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("frontmatter 的 name（%q）必须与技能名（%q）一致", meta.Name, dirName))
	}
	return meta, nil
}

// validateNewSkillContent 建立新技能的校验：在基础校验之上额外要求合法的
// 语义版本号（metadata.version / version，x.y.z）——版本号是版本管理的唯一
// 标识，缺失则无法判定"新版本还是同版本冲突"，直接拒绝。
func validateNewSkillContent(dirName, content string) (*skill.SkillMeta, error) {
	meta, err := validateSkillContent(dirName, content)
	if err != nil {
		return nil, err
	}
	if !meta.ValidVersion() {
		return nil, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("技能 %q 缺少合法语义版本号：请在 frontmatter 提供 metadata.version（如 1.0.0），格式须为 x.y.z", meta.Name))
	}
	return meta, nil
}

// versionConflictErr 构造版本冲突错误（同版本号已存在但内容不同，需覆盖或改版本号）。
func versionConflictErr(name, ver string) *apperr.Error {
	return apperr.New(apperr.CodeVersionConflict,
		fmt.Sprintf("技能「%s」已存在版本 v%s（同一版本号只能有一份）。请修改 frontmatter 版本号后重试，或确认覆盖该版本（覆盖后旧内容被替换）。", name, ver))
}

// verDisplay 版本号展示：空版本号（旧格式技能）显示为"（无版本号）"。
func verDisplay(v string) string {
	if v == "" {
		return "（无版本号）"
	}
	return "v" + v
}

// applyVersion 依据"新旧版本号 + 内容是否相同 + 是否显式覆盖"决定如何处理
// 已存在技能，保证**同一 name 下同一 semver 副本只能有一份**：
//
//   - 新版本号在"当前 + 历史"中都未出现 → 发布新版本：快照旧内容 → 替换；
//   - 新版本号已存在且内容相同 → 编辑场景幂等（同一副本，无操作）；
//   - 新版本号已存在但内容不同 → 未 overwrite 则版本冲突拒绝；overwrite=true 时
//     覆盖：新内容成为当前，旧历史槽删除（同版本号只留一份），原当前内容入历史；
//   - 历史无版本号（旧格式）→ 不判定冲突，直接替换（兼容历史数据）。
//
// confirmOnNameExists=true（上传 zip 场景）：只要名字已存在就必须显式确认
// （overwrite=true）才落盘，否则一律返回 409 提示——同名同版本同内容（完全相同的
// 副本）也返回提示，保证用户对任何"覆盖"动作始终知情，杜绝静默覆盖。false
// （编辑场景）时保留"内容相同=幂等"的快捷路径，避免无改动保存也弹确认。
//
// 调用方需持有 s.mu。
func (s *SkillStore) applyVersion(dir, curContent, newContent string, newMeta *skill.SkillMeta, overwrite, confirmOnNameExists bool) error {
	newVer := newMeta.Version
	curMeta, _ := skill.ParseSkillMD([]byte(curContent))
	curVer := curMeta.Version
	slots := s.listSlots(dir)
	name := filepath.Base(dir)

	// 新版本号是否已存在（当前或历史）？
	existingSlot := -1
	var slotContent string
	for _, sl := range slots {
		if sl.semver == newVer {
			existingSlot = sl.slot
			slotContent = sl.content
			break
		}
	}
	dupOfCurrent := newVer != "" && newVer == curVer
	dupOfSlot := existingSlot >= 0 && slotContent == newContent

	if confirmOnNameExists {
		// 上传场景：名字已存在 → 未显式确认时一律返回 409，提示文案精确到场景
		// （前端直接展示 + 二次确认后带 overwrite=true 重试）。
		if !overwrite {
			switch {
			case dupOfCurrent && curContent == newContent:
				return apperr.New(apperr.CodeVersionConflict,
					fmt.Sprintf("技能「%s」已存在版本 v%s 且内容一致。继续上传将覆盖当前副本并切换为生效版本，是否覆盖？", name, newVer))
			case dupOfCurrent:
				return apperr.New(apperr.CodeVersionConflict,
					fmt.Sprintf("技能「%s」已存在版本 v%s（内容不同）。继续上传将覆盖当前副本并切换为生效版本，是否覆盖？", name, newVer))
			case existingSlot >= 0:
				return apperr.New(apperr.CodeVersionConflict,
					fmt.Sprintf("技能「%s」的版本 v%s 已存在于历史版本。继续上传将覆盖该版本并切换为生效版本，是否覆盖？", name, newVer))
			default:
				return apperr.New(apperr.CodeVersionConflict,
					fmt.Sprintf("技能「%s」已存在（当前版本 v%s）。新版本 v%s 将发布并切换为生效版本（原版本自动进入历史可回滚），是否继续？", name, verDisplay(curVer), newVer))
			}
		}
	} else {
		if (dupOfCurrent || dupOfSlot) && curContent == newContent {
			return nil // 编辑场景：同一副本 → 幂等（不产生任何变化）
		}
		if (existingSlot >= 0 || dupOfCurrent) && !overwrite {
			return versionConflictErr(name, newVer)
		}
	}

	// 快照当前版本（若当前版本号已入历史则跳过，避免重复）。
	if curVer != "" && curVer != newVer && !slotHasSemver(slots, curVer) {
		if _, err := s.snapshot(dir, curContent); err != nil {
			return apperr.Wrap(apperr.CodeInternal, "写入版本快照失败", err)
		}
	}
	// 覆盖历史槽：删除旧槽（新版本号成为当前，同版本号只留一份）。
	if existingSlot >= 0 {
		if err := removeDirAll(filepath.Join(dir, versionsDirName, fmt.Sprintf("v%d", existingSlot))); err != nil {
			return apperr.Wrap(apperr.CodeInternal, "清理旧版本槽失败", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(newContent), 0o644); err != nil {
		return apperr.Wrap(apperr.CodeInternal, "写入 SKILL.md 失败", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// zip 上传
// ---------------------------------------------------------------------------

// Upload 上传并解压 zip 技能包，技能名/版本号从包内 SKILL.md 自动提取，
// 用户无需（也不应）手工填写——提取不到关键信息直接拒绝上传。
//
//   - 定位：在 zip 内自动定位含 SKILL.md 的内容根（任意层级，取最浅），
//     该根下的全部目录结构原样保留（ref/、scripts/、docs/ 及跨目录相对引用）。
//   - 名称：frontmatter 的 name → 内容根目录名（包裹目录名）→ 上传文件名
//     （去掉 .zip）逐级回退；仍为空则拒绝。
//   - 版本：要求 frontmatter 提供合法语义版本号（metadata.version，x.y.z），
//     否则拒绝（版本号是版本管理的唯一标识）。
//   - 同名处理：名字已存在 → 一律返回 409（VERSION_CONFLICT）提示，提示文案精确区分
//     "同版本（覆盖当前副本）"与"新版本（发布并切换生效）"；用户确认后带
//     overwrite=true 重试：版本不同 = 发布新版本（旧内容快照留痕），版本相同 =
//     覆盖当前副本（同版本号只留一份）。
//
// 防 zip-slip 路径穿越。
func (s *SkillStore) Upload(_ context.Context, fileName string, zipData []byte, overwrite bool) (Skill, error) {
	if len(zipData) == 0 {
		return Skill{}, apperr.New(apperr.CodeInvalidArgument, "上传的 zip 为空")
	}
	if int64(len(zipData)) > s.maxBytes {
		return Skill{}, apperr.New(apperr.CodeInvalidArgument, fmt.Sprintf("zip 超过 %d 字节上限", s.maxBytes))
	}

	// 解压到临时目录（位于技能根内，与目标目录同文件系统，便于原子替换）。
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "创建技能根目录失败", err)
	}
	tmp, err := os.MkdirTemp(s.root, ".upload-")
	if err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "创建上传临时目录失败", err)
	}
	defer func() { _ = removeDirAll(tmp) }() // 清理临时目录

	if err := unzipSafe(tmp, zipData, s.maxBytes); err != nil {
		return Skill{}, err
	}
	// 定位技能内容根（含 SKILL.md 的目录，任意层级取最浅）。
	contentRoot, err := findSkillRoot(tmp)
	if err != nil {
		return Skill{}, err
	}
	content, err := os.ReadFile(filepath.Join(contentRoot, "SKILL.md"))
	if err != nil {
		return Skill{}, apperr.Wrap(apperr.CodeInvalidArgument, "zip 内缺少可读的 SKILL.md", err)
	}
	// 从 SKILL.md 解析名称与版本；名称缺失时回退内容根目录名。
	meta, err := skill.ParseSkillMD(content)
	if err != nil {
		return Skill{}, apperr.New(apperr.CodeInvalidArgument, "SKILL.md 格式不合法: "+err.Error())
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		// 回退①：SKILL.md 在 zip 内的包裹目录名（如 my-skill/SKILL.md → my-skill）。
		// contentRoot == tmp（平铺 zip）时无包裹目录，跳过此回退。
		if rel, rerr := filepath.Rel(tmp, contentRoot); rerr == nil && rel != "." && rel != "" {
			name = filepath.Base(contentRoot)
		}
	}
	if name == "" {
		// 回退②：上传文件名（去掉 .zip）。
		if f := strings.TrimSuffix(filepath.Base(fileName), ".zip"); f != "" {
			name = f
		}
	}
	if name == "" {
		return Skill{}, apperr.New(apperr.CodeInvalidArgument,
			"无法从 zip 提取技能名：请在 SKILL.md frontmatter 提供 name 字段")
	}
	dir, err := s.dirPath(name)
	if err != nil {
		return Skill{}, err
	}
	if !meta.ValidVersion() {
		return Skill{}, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("技能 %q 缺少合法语义版本号：请在 SKILL.md frontmatter 提供 metadata.version（如 1.0.0），格式须为 x.y.z", name))
	}
	// 命名一致性（frontmatter name 与最终技能名一致）+ 必填项 + 大小。
	if _, err := validateSkillContent(name, string(content)); err != nil {
		return Skill{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	exists := false
	if _, err := os.Stat(dir); err == nil {
		exists = true
	} else if !os.IsNotExist(err) {
		return Skill{}, apperr.Wrap(apperr.CodeInternal, "访问技能目录失败", err)
	}

	if exists {
		// 同名：按版本号语义处理（新版本 / 同版本冲突或覆盖），再整包替换。
		cur, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			return Skill{}, apperr.Wrap(apperr.CodeInternal, "读取当前 SKILL.md 失败", err)
		}
		if err := s.applyVersion(dir, string(cur), string(content), meta, overwrite, true); err != nil {
			return Skill{}, err
		}
		if err := replaceSkillDir(dir, contentRoot); err != nil {
			return Skill{}, apperr.Wrap(apperr.CodeInternal, "替换技能目录失败", err)
		}
	} else {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Skill{}, apperr.Wrap(apperr.CodeInternal, "创建技能目录失败", err)
		}
		if err := moveDirContents(contentRoot, dir); err != nil {
			return Skill{}, apperr.Wrap(apperr.CodeInternal, "写入技能目录失败", err)
		}
	}
	return s.readDir(name)
}

// unzipSafe 安全解压：校验每个 entry 路径不越界（防 zip-slip）。
// 路径校验跨平台加固：zip 内路径统一转正斜杠判断——绝对路径（/、\、C:）与
// 父目录引用（..）一律拒绝（Windows 上 filepath.IsAbs 对 "\x" 会误判为非绝对，
// 因此不能依赖它）。
func unzipSafe(dst string, data []byte, maxBytes int64) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "不是合法的 zip 文件: "+err.Error())
	}
	for _, f := range zr.File {
		slashName := strings.ReplaceAll(f.Name, "\\", "/")
		// 统一用 slashName 计算目标路径：zip 生成方（如 Windows 上的
		// filepath.Join）常写入字面反斜杠分隔的条目名。先归一为正斜杠再
		// FromSlash/Clean，保证 Windows 与 Linux 解压行为一致
		// （否则 Linux 会把 `docs\guide.md` 当单一文件名拍平落盘）。
		clean := filepath.Clean(filepath.FromSlash(slashName))
		if clean == "." {
			continue // "./" 之类的无害目录标记，跳过
		}
		if strings.HasPrefix(slashName, "/") || filepath.VolumeName(clean) != "" ||
			filepath.IsAbs(clean) || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return apperr.New(apperr.CodeInvalidArgument, fmt.Sprintf("zip 内含非法路径 %q", f.Name))
		}
		target := filepath.Join(dst, clean)
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(filepath.Separator)) {
			return apperr.New(apperr.CodeInvalidArgument, fmt.Sprintf("zip 内含越界路径 %q", f.Name))
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return apperr.Wrap(apperr.CodeInternal, "创建解压目录失败", err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return apperr.Wrap(apperr.CodeInternal, "创建解压目录失败", err)
		}
		src, err := f.Open()
		if err != nil {
			return apperr.New(apperr.CodeInvalidArgument, "读取 zip 条目失败: "+err.Error())
		}
		// 限制单文件解压大小（防 zip 炸弹）。
		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = src.Close()
			return apperr.Wrap(apperr.CodeInternal, "写入解压文件失败", err)
		}
		_, cerr := io.Copy(dstFile, io.LimitReader(src, maxBytes+1))
		_ = src.Close()
		_ = dstFile.Close()
		if cerr != nil {
			return apperr.Wrap(apperr.CodeInternal, "解压文件失败", err)
		}
	}
	return nil
}

// findSkillRoot 在解压目录内定位含 SKILL.md 的内容根。
//   - tmp/SKILL.md 存在 → tmp 本身；
//   - 否则扫描整棵目录树，取"直接包含 SKILL.md 且路径最浅"的目录
//     （容忍任意层级包裹目录、多个顶层目录/散落文件并存）；
//   - 找不到 → 错误。
func findSkillRoot(tmp string) (string, error) {
	if _, err := os.Stat(filepath.Join(tmp, "SKILL.md")); err == nil {
		return tmp, nil
	}
	type hit struct {
		dir   string
		depth int
	}
	var best *hit
	err := filepath.WalkDir(tmp, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		depth := strings.Count(filepath.ToSlash(p), "/")
		if best == nil || depth < best.depth {
			best = &hit{dir: filepath.Dir(p), depth: depth}
		}
		return nil
	})
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInternal, "扫描解压目录失败", err)
	}
	if best == nil {
		return "", apperr.New(apperr.CodeInvalidArgument,
			"zip 内未找到 SKILL.md（技能必须包含 SKILL.md 主文件）")
	}
	return best.dir, nil
}

// replaceSkillDir 用 from 目录内容整体替换 dir（技能目录），
// 保留管理端内部状态（.versions 版本历史、.disabled 标记）。
func replaceSkillDir(dir, from string) error {
	var keeps []struct{ name, tmp string }
	for _, n := range []string{versionsDirName, disabledFileName} {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			t := filepath.Join(filepath.Dir(dir), fmt.Sprintf(".keep-%d-%s", time.Now().UnixNano(), n))
			if err := os.Rename(p, t); err != nil {
				return err
			}
			keeps = append(keeps, struct{ name, tmp string }{n, t})
		}
	}
	if err := removeDirAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, k := range keeps {
		if err := os.Rename(k.tmp, filepath.Join(dir, k.name)); err != nil {
			return err
		}
	}
	return moveDirContents(from, dir)
}

// moveDirContents 把 from 目录下全部条目移动到 to 目录（同文件系统 rename）。
func moveDirContents(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 模块层（路由 + HTTP handlers）
// ---------------------------------------------------------------------------

// skillsModule 技能管理模块。
type skillsModule struct{ s *Service }

func newSkillsModule(s *Service) Module { return skillsModule{s: s} }

func (m skillsModule) Key() string  { return "skills" }
func (m skillsModule) Name() string { return "技能管理" }
func (m skillsModule) Description() string {
	return "上传/编辑/删除技能（Anthropic Agent Skills），保存后 agent 热加载生效"
}
func (m skillsModule) Implemented() bool { return true }

func (m skillsModule) Register(mux *http.ServeMux, _ *Service) {
	mux.HandleFunc("GET /v1/admin/skills", m.s.handleListSkills)
	mux.HandleFunc("POST /v1/admin/skills", m.s.handleCreateSkill)
	mux.HandleFunc("POST /v1/admin/skills/upload", m.s.handleUploadSkill)
	mux.HandleFunc("GET /v1/admin/skills/{name}", m.s.handleGetSkill)
	mux.HandleFunc("PUT /v1/admin/skills/{name}", m.s.handleUpdateSkill)
	mux.HandleFunc("PATCH /v1/admin/skills/{name}/enabled", m.s.handleSetSkillEnabled)
	mux.HandleFunc("POST /v1/admin/skills/{name}/versions/{version}/restore", m.s.handleRestoreSkillVersion)
	mux.HandleFunc("DELETE /v1/admin/skills/{name}", m.s.handleDeleteSkill)
}

func (s *Service) handleListSkills(w http.ResponseWriter, r *http.Request) {
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	skills, err := s.skills.For(agent).List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": skills, "agent_id": agent})
}

type createSkillReq struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (s *Service) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	var req createSkillReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	sk, err := s.skills.For(agent).Create(r.Context(), req.Name, req.Content)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("skill created", zap.String("agent", agent), zap.String("skill", sk.Name))
	writeJSON(w, http.StatusCreated, map[string]any{"skill": sk, "agent_id": agent})
}

func (s *Service) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	sk, err := s.skills.For(agent).Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": sk, "agent_id": agent})
}

func (s *Service) handleUpdateSkill(w http.ResponseWriter, r *http.Request) {
	var req createSkillReq // 复用：更新只接受 content — 更新以路径 name 为准
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "true"
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	sk, err := s.skills.For(agent).Update(r.Context(), r.PathValue("name"), req.Content, overwrite)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("skill updated", zap.String("agent", agent), zap.String("skill", sk.Name), zap.Bool("overwrite", overwrite))
	writeJSON(w, http.StatusOK, map[string]any{"skill": sk, "agent_id": agent})
}

func (s *Service) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.skills.For(agent).Delete(r.Context(), name); err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("skill deleted", zap.String("agent", agent), zap.String("skill", name))
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadSkill 上传 zip 技能包（multipart：仅 file）。
// 技能名与版本号由后端从 zip 内 SKILL.md 自动提取，用户无需填写。
// 资源域经 agent_id 查询参数指定（超管）；agent_admin/admin 由后端锁定自身归属。
func (s *Service) handleUploadSkill(w http.ResponseWriter, r *http.Request) {
	// 限制整体请求体大小（zip 上限 + 表单开销），防内存/磁盘打爆。
	limit := int64(s.skillMaxBytes)
	r.Body = http.MaxBytesReader(w, r.Body, limit+64<<10)
	if err := r.ParseMultipartForm(limit); err != nil {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument,
			"上传请求不合法（zip 过大或非 multipart 格式）: "+err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少文件字段 file"))
		return
	}
	defer file.Close()
	if header.Size > limit {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("zip 超过 %d 字节上限", limit)))
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		writeError(w, r, apperr.Wrap(apperr.CodeInternal, "读取上传文件失败", err))
		return
	}
	// 同版本号覆盖需显式确认（?overwrite=true），否则返回 409 由前端提示。
	overwrite := r.URL.Query().Get("overwrite") == "true"
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	sk, err := s.skills.For(agent).Upload(r.Context(), header.Filename, data, overwrite)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("skill uploaded", zap.String("agent", agent), zap.String("skill", sk.Name),
		zap.String("file", header.Filename), zap.String("version", sk.SemVer),
		zap.Int("files", len(sk.Files)))
	if s.log != nil {
		s.log.Debug("skill upload structure", zap.String("agent", agent), zap.String("skill", sk.Name),
			zap.String("tree", strings.Join(sk.Files, ", ")))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"skill": sk, "agent_id": agent})
}

type setEnabledReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Service) handleSetSkillEnabled(w http.ResponseWriter, r *http.Request) {
	var req setEnabledReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	name := r.PathValue("name")
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	sk, err := s.skills.For(agent).SetEnabled(r.Context(), name, req.Enabled)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("skill enabled changed", zap.String("agent", agent), zap.String("skill", name), zap.Bool("enabled", req.Enabled))
	writeJSON(w, http.StatusOK, map[string]any{"skill": sk, "agent_id": agent})
}

func (s *Service) handleRestoreSkillVersion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	version := r.PathValue("version")
	if !skill.SemVerRe.MatchString(version) {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "版本号必须为 x.y.z 语义版本号（如 1.1.0）"))
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	sk, err := s.skills.For(agent).RestoreVersion(r.Context(), name, version)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.logInfo("skill version restored", zap.String("agent", agent), zap.String("skill", name), zap.String("version", version))
	writeJSON(w, http.StatusOK, map[string]any{"skill": sk, "agent_id": agent})
}

// logInfo 日志辅助（log 可空）。
func (s *Service) logInfo(msg string, fields ...zap.Field) {
	if s.log != nil {
		s.log.Info(msg, fields...)
	}
}
