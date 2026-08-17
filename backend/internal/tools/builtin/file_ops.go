package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// maxReadBytes 单次读取文件大小上限（1MB）：防止大文件撑爆 LLM 上下文。
const maxReadBytes = 1 << 20

// ensureSandboxWorkspace 委托 sandbox-service 初始化用户工作区：发送一条最小
// 命令触发其 ensureWorkspace 流程（sandbox 主进程以 root 创建 /work/users/<uid>
// 并修正属主 = 派生 uid、属组 = app 组、mode 2770）。仅当用户目录不存在时由
// file_ops 调用，幂等；成功返回后即可安全读写用户工作区。
func ensureSandboxWorkspace(ctx context.Context, sandboxURL string, uid int64) error {
	body, err := json.Marshal(sandboxExecRequest{
		UserID:   uid,
		Language: "shell",
		Code:     "true", // 空操作命令：仅触发工作区初始化，不产生副作用
	})
	if err != nil {
		return fmt.Errorf("构造沙盒请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(sandboxURL, "/")+"/v1/exec", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造沙盒请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("沙盒服务不可达: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("沙盒初始化请求失败（HTTP %d）: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var r sandboxExecResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return fmt.Errorf("解析沙盒响应失败: %w", err)
	}
	if r.Error != "" {
		return fmt.Errorf("沙盒拒绝初始化: %s", r.Error)
	}
	return nil
}

// FileOpsTool L2 文件操作工具：在智能体工作目录内读写文件。
//
// 根目录 = 服务进程的工作目录（os.Getwd()，容器里为 /app），与 /files
// HTTP 媒体服务共用同一路径边界（tools.ResolveInRoot），保证"模型能访问的
// 与前端能渲染的"完全一致——本地图片/视频文件模型用 file_ops 读内容，
// 前端用 <files 基址>/files/<相对路径> 渲染。
//
// 用户隔离（阶段2）：调用上下文携带合法 user_id（X-User-Id）时，实际解析
// 根目录自动切换为 <Root>/users/<uid>，与 sandbox 容器内 code_executor 的
// /work/users/<uid> 指向同一份宿主数据——file_ops 与沙盒代码执行共享同一
// 用户工作区。展示路径统一为相对全局根（含 users/<uid>/ 前缀），模型据此
// 生成 /files/<完整相对路径> URL，前端渲染即落在对应用户目录内。
//
// 技能资源（只读）：路径以 @skills/ 开头时解析到技能根目录（SkillsRoot，
// 缺省 <工作目录>/skills），仅允许 read/list/search/stat，禁止 write——
// 技能由管理端维护，模型只能按 skill 工具给出的 @skills/ 清单读取内容。
type FileOpsTool struct {
	// Root 全局工作根目录；空 = 运行时 os.Getwd()。
	Root string
	// SkillsRoot 技能根目录（与 skill Provider.Root 同源，缺省 <工作目录>/skills）。
	SkillsRoot string
	// SandboxURL 沙盒服务地址（可选）。非空且用户隔离时，若用户工作区
	// <Root>/users/<uid> 尚未创建，file_ops 先触发一次沙盒初始化——sandbox
	// 主进程以 root 创建目录并修正属主（uid=派生uid, 属组=app 组），避免
	// 本进程（非 root）创建出属主错误的用户目录导致沙盒用户无法访问。
	// 留空 = 本地降级（无沙盒），保持旧行为自建目录。
	SandboxURL string
}

// fileOpsArgs 文件操作参数。
type fileOpsArgs struct {
	Action     string `json:"action"`                // read | write | list | stat | search
	Path       string `json:"path"`                  // 相对工作目录的相对路径
	Content    string `json:"content,omitempty"`     // write 时必填
	Recursive  bool   `json:"recursive,omitempty"`   // list 时是否递归
	Tree       bool   `json:"tree,omitempty"`        // list 时输出目录树（缩进层级，理清嵌套结构）
	Depth      int    `json:"depth,omitempty"`       // tree 时最大嵌套深度（0 = 不限制）
	Query      string `json:"query,omitempty"`       // search 时必填：搜索关键词
	NameOnly   bool   `json:"name_only,omitempty"`   // search 时仅按文件名匹配（默认 false = 同时匹配文件内容）
	MaxResults int    `json:"max_results,omitempty"` // search 结果上限（1~200，默认 50）
}

// Schema 实现 Tool 接口。
func (t *FileOpsTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "file_ops",
		Description: "文件操作（写需用户确认）：在你的用户工作区（当前工作目录）内读取/写入/列出/搜索/查看文件。path 一律使用相对当前工作目录的相对路径（如 docs/report.md），禁止使用绝对路径或 ../ 越界访问。你的当前目录 = 用户工作区（users/<uid>/，uid 前缀可在 read/write/list 的返回信息里看到，交付文件时用它拼 users/<uid>/xxx 相对路径）；不要访问 users/<uid> 之外的目录（不存在或无权限，必然失败）。技能资源（只读）：技能内文件以 @skills/<技能名>/… 前缀访问（如 @skills/my-skill/ref/doc.md），仅支持 read/list/search/stat，禁止 write；@skills/ 下个别文件可能很大（如 scripts/*.py 可达数百 KB），整读会注入海量 token 撑爆上下文——读取前先 stat 查看大小，超 50KB 的只按 SKILL.md 指引使用、不要整读。action 取值：read=读取文本文件内容（上限 1MB，超限截断）；write=写入或覆盖文件（自动创建父目录，写操作会真实改动磁盘）；list=列出目录内容，每行形如 \"- [目录] 子目录名/\" 或 \"- [文件] 文件名（大小）\"，文件名即真实名称、无任何前缀标记（recursive=true 可递归，tree=true 则输出带缩进的目录树，depth 限制嵌套深度）；search=在目录下按文件名或内容递归搜索文件，返回命中列表（query 为关键词，name_only=true 时仅匹配文件名，max_results 控制结果上限）；stat=查看文件类型/大小/修改时间。本地图片/视频等媒体文件用文本读取会失败，应让前端用 /files/ URL 直接渲染。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"action":{"type":"string","description":"操作类型：read|write|list|search|stat"},
				"path":{"type":"string","description":"相对工作目录的文件或目录路径，如 docs/report.md"},
				"content":{"type":"string","description":"write 操作写入的文本内容"},
				"recursive":{"type":"boolean","description":"list 时是否递归列出子目录，默认 false"},
				"tree":{"type":"boolean","description":"list 时是否输出缩进目录树（理清嵌套结构），默认 false；tree=true 时自动递归"},
				"depth":{"type":"integer","description":"tree 时的最大嵌套深度（1~10，0 = 不限制，默认 0）"},
				"query":{"type":"string","description":"search 的搜索关键词"},
				"name_only":{"type":"boolean","description":"search 时仅按文件名匹配，默认 false（同时匹配文件内容）"},
				"max_results":{"type":"integer","description":"search 结果上限（1~200，默认 50）"}
			}
		}`),
		Required:   []string{"action", "path"},
		Permission: schema.PermissionL2Write,
	}
}

// Execute 实现 Tool 接口：按 action 分发到各操作。
func (t *FileOpsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p fileOpsArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("file_ops: 参数解析失败: %w", err)
	}

	// 技能资源命名空间（@skills/…）：只读映射到技能根目录。
	if tools.IsSkillsPath(p.Path) {
		if p.Action == "write" {
			return "", fmt.Errorf("file_ops: 技能资源为只读，禁止 write（技能由管理端维护，请用管理端编辑）")
		}
		skillsRoot, err := t.effectiveSkillsRoot()
		if err != nil {
			return "", err
		}
		full, err := tools.ResolveSkillsPath(skillsRoot, p.Path)
		if err != nil {
			return "", err
		}
		prefix := tools.SkillsPathPrefix
		return t.dispatch(ctx, p, full, skillsRoot, prefix)
	}

	// 全局根（展示与安全边界基址）：
	//   root        实际解析根（用户隔离时 = 全局根/users/<uid>）
	//   displayBase 展示基址（非空时所有输出路径转成相对它的"完整相对路径"，
	//               含 users/<uid>/ 前缀，模型可直接拼 /files URL）。
	globalRoot, err := filepath.Abs(t.Root)
	if err != nil || globalRoot == "." {
		if abs, aerr := filepath.Abs("."); aerr == nil {
			globalRoot = abs
		}
	}
	root := globalRoot
	var displayBase string
	if uid, ok := UserIDFromContext(ctx); ok {
		uidStr := strconv.FormatInt(uid, 10)
		// 用户隔离：解析根默认 = <全局根>/users/<uid>。路径若已带当前用户前缀
		// （users/<uid>/…，如 /files 渲染协议与聊天文档注入的全局相对路径），
		// 直接相对全局根解析，避免与隔离根重复拼接（users/<uid>/users/<uid>/…）。
		// 仅放行"当前用户"的前缀；其它 users/<xxx>/ 仍按普通相对路径解析
		// （落在 <uid> 工作区内 → 不存在 → 拒绝），跨用户隔离边界不变。
		if p.Path == "users/"+uidStr || strings.HasPrefix(p.Path, "users/"+uidStr+"/") {
			root = globalRoot
		} else {
			root = filepath.Join(globalRoot, "users", uidStr)
		}
		displayBase = globalRoot
		// 用户工作区尚未由 sandbox 初始化时，先委托沙盒创建（root 进程修正属主）。
		// 目录已存在则零开销跳过；仅在"首次访问"触发一次 HTTP。
		if t.SandboxURL != "" {
			if _, err := os.Stat(root); os.IsNotExist(err) {
				if ierr := ensureSandboxWorkspace(ctx, t.SandboxURL, uid); ierr != nil {
					return "", fmt.Errorf("file_ops: 用户工作区初始化失败: %v", ierr)
				}
			}
		}
	}

	full, err := tools.ResolveInRoot(root, p.Path)
	if err != nil {
		return "", err
	}
	return t.dispatch(ctx, p, full, displayBase, "")
}

// dispatch 按 action 分发（普通路径 displayBase 与技能路径 skillsRoot 共用同一套实现，
// 差异仅在展示基址与虚拟前缀）。
func (t *FileOpsTool) dispatch(ctx context.Context, p fileOpsArgs, full, base, prefix string) (string, error) {
	switch p.Action {
	case "read":
		return fileOpsRead(full, base, prefix)
	case "write":
		return fileOpsWrite(full, base, prefix, p.Content)
	case "list":
		if p.Tree {
			return fileOpsTree(ctx, full, base, prefix, p.Depth)
		}
		return fileOpsList(ctx, full, base, prefix, p.Recursive)
	case "stat":
		return fileOpsStat(full, base, prefix)
	case "search":
		if p.Query == "" {
			return "", fmt.Errorf("file_ops: search 必须提供 query（搜索关键词）")
		}
		return fileOpsSearch(ctx, full, base, prefix, p.Query, p.NameOnly, p.MaxResults)
	default:
		return "", fmt.Errorf("file_ops: 未知 action %q（仅支持 read|write|list|search|stat）", p.Action)
	}
}

// effectiveSkillsRoot 技能根目录：优先 SkillsRoot 配置，缺省 <工作目录>/skills
// （与 skill.Provider 的默认值保持一致，保证 @skills/ 路径两边解析结果一致）。
func (t *FileOpsTool) effectiveSkillsRoot() (string, error) {
	root := t.SkillsRoot
	if root == "" {
		root = filepath.Join(absOr("."), "skills")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("file_ops: 解析技能目录失败: %w", err)
	}
	return abs, nil
}

// absOr 解析为绝对路径（失败回退给定值）。
func absOr(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// displayPath 把实际路径转成展示路径：
//   - base 为空（未隔离）→ 原样返回绝对路径（历史行为）；
//   - base 非空（用户隔离 / 技能资源）→ 返回相对 base 的路径，
//     prefix 非空时（@skills/）拼在路径前，模型可直接回填给 file_ops 使用。
//
// 越界或解析失败时回退原路径，保证错误信息可读。
func displayPath(base, prefix, full string) string {
	if base == "" {
		return full
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return full
	}
	if rel == "." {
		if prefix != "" {
			return prefix[:len(prefix)-1] // "@skills"（去尾斜杠）
		}
		return base
	}
	return prefix + filepath.ToSlash(rel)
}

// fileOpsRead 读取文本文件内容（1MB 上限）。
func fileOpsRead(full, base, prefix string) (string, error) {
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file_ops: 文件不存在: %s", displayPath(base, prefix, full))
		}
		return "", fmt.Errorf("file_ops: 读取文件信息失败: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("file_ops: %s 是目录，请用 list 查看目录内容", displayPath(base, prefix, full))
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("file_ops: 读取文件失败: %w", err)
	}
	truncated := len(data) > maxReadBytes
	if truncated {
		data = data[:maxReadBytes]
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file_ops: %s 为二进制/非 UTF-8 文件，无法以文本读取（本地媒体请让前端用 /files/ URL 渲染）", displayPath(base, prefix, full))
	}
	out := string(data)
	if truncated {
		out += fmt.Sprintf("\n……（文件超过 1MB，仅显示前 %d 字节）", maxReadBytes)
	}
	return out, nil
}

// fileOpsWrite 写入/覆盖文件（自动创建父目录）。
func fileOpsWrite(full, base, prefix, content string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("file_ops: 创建目录失败: %w", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("file_ops: 写入文件失败: %w", err)
	}
	return fmt.Sprintf("已写入 %s（%d 字节）", displayPath(base, prefix, full), len(content)), nil
}

// fileOpsList 列出目录内容（可选递归），输出对 LLM 无歧义的 Markdown 列表。
//
// 格式（类型用 [文件]/[目录] 明确标注，名称就是真实文件名）：
//
//	目录 /app（1 个目录，2 个文件）：
//	- [目录] sub/
//	- [文件] hello.txt（12 B）
//	- [文件] 0地图0.png（153.7 KB）
//
// 历史教训：早期用 "F 名 (N B)" / "D 名/" 前缀标注类型，被模型误把 "F "
// 当成文件名的一部分，导致拼接 /files URL 出错（如 F 0地图0.png）。改用
// 明确标注后名称即文件名本身，模型可直接用于 /files/<相对路径> 渲染。
func fileOpsList(ctx context.Context, full, base, prefix string, recursive bool) (string, error) {
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file_ops: 目录不存在: %s", displayPath(base, prefix, full))
		}
		return "", fmt.Errorf("file_ops: 读取目录信息失败: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file_ops: %s 不是目录，请用 read/stat 处理文件", displayPath(base, prefix, full))
	}

	// 展示基址：隔离模式下用全局根（条目路径含 users/<uid>/ 前缀，可直接拼 /files URL）；
	// 技能资源用技能根（条目带 @skills/ 前缀）；未隔离时退回目录本身（历史行为）。
	relBase := full
	if base != "" {
		relBase = base
	}

	// 统一收集条目（非递归 = 单层；递归 = WalkDir 全量）。
	type item struct {
		rel  string // 相对展示基址的路径（含虚拟前缀，可回填给 file_ops）
		dir  bool
		size int64
	}
	items := make([]item, 0)
	collect := func(p string, d os.DirEntry) error {
		rel, _ := filepath.Rel(relBase, p)
		if rel == "." {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		it := item{rel: prefix + filepath.ToSlash(rel), dir: d.IsDir()}
		if !d.IsDir() {
			if fi, e := d.Info(); e == nil {
				it.size = fi.Size()
			}
		}
		items = append(items, it)
		return nil
	}

	if !recursive {
		entries, err := os.ReadDir(full)
		if err != nil {
			return "", fmt.Errorf("file_ops: 读取目录失败: %w", err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if err := collect(filepath.Join(full, e.Name()), e); err != nil {
				return "", fmt.Errorf("file_ops: 列目录中断: %w", err)
			}
		}
	} else {
		err = filepath.WalkDir(full, func(p string, d os.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			return collect(p, d)
		})
		if err != nil {
			return "", fmt.Errorf("file_ops: 递归列目录失败: %w", err)
		}
	}

	dirs, files := 0, 0
	for _, it := range items {
		if it.dir {
			dirs++
		} else {
			files++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "目录 %s（%d 个目录，%d 个文件）：\n", displayPath(base, prefix, full), dirs, files)
	for _, it := range items {
		if it.dir {
			fmt.Fprintf(&b, "- [目录] %s/\n", it.rel)
		} else {
			fmt.Fprintf(&b, "- [文件] %s（%s）\n", it.rel, formatSize(it.size))
		}
	}
	return b.String(), nil
}

// maxTreeEntries 目录树输出的条目上限（防止超大目录刷屏上下文）。
const maxTreeEntries = 500

// fileOpsTree 输出目录的缩进树（理清嵌套结构，而非"某层有哪些文件"）。
//
// 用空格缩进表示层级，避免模型把 box-drawing 字符误认成文件名：
//
//	目录 /app（2 个目录，3 个文件）：
//	- docs/
//	  - report.md（12 B）
//	  - 2026/
//	    - q1.md（1.0 KB）
//	- scripts/
//	  - run.py
//
// depth > 0 时限制嵌套深度（1 = 仅直接子项），超深目录整棵跳过；条目超上限截断。
func fileOpsTree(ctx context.Context, full, base, prefix string, depth int) (string, error) {
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file_ops: 目录不存在: %s", displayPath(base, prefix, full))
		}
		return "", fmt.Errorf("file_ops: 读取目录信息失败: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file_ops: %s 不是目录，请用 read/stat 处理文件", displayPath(base, prefix, full))
	}
	if depth < 0 || depth > 10 {
		depth = 0
	}

	relBase := full
	if base != "" {
		relBase = base
	}

	type node struct {
		rel  string
		dir  bool
		size int64
	}
	var nodes []node
	dirs, files := 0, 0
	truncated := false

	err = filepath.WalkDir(full, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil // 跳过不可读条目
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, _ := filepath.Rel(relBase, p)
		if rel == "." {
			return nil
		}
		// depth 限制：条目层级 = 相对展示基址的路径段数，超深则整棵子树跳过。
		if depth > 0 && len(strings.Split(rel, string(filepath.Separator))) > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(nodes) >= maxTreeEntries {
			truncated = true
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		n := node{rel: rel, dir: d.IsDir()}
		if d.IsDir() {
			dirs++
		} else {
			files++
			if fi, e := d.Info(); e == nil {
				n.size = fi.Size()
			}
		}
		nodes = append(nodes, n)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("file_ops: 读取目录树失败: %w", err)
	}

	// 按相对路径排序：同层子项自然聚在一起（父路径前缀相同），层级不乱。
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].rel < nodes[j].rel })

	var b strings.Builder
	fmt.Fprintf(&b, "目录 %s（%d 个目录，%d 个文件）：\n", displayPath(base, prefix, full), dirs, files)
	for _, n := range nodes {
		parts := strings.Split(n.rel, string(filepath.Separator))
		indent := strings.Repeat("  ", len(parts)-1)
		if n.dir {
			fmt.Fprintf(&b, "%s- %s/\n", indent, parts[len(parts)-1])
		} else {
			fmt.Fprintf(&b, "%s- %s（%s）\n", indent, parts[len(parts)-1], formatSize(n.size))
		}
	}
	if truncated {
		fmt.Fprintf(&b, "……（条目超过 %d，已截断；可加大 depth 或对子目录单独 list）\n", maxTreeEntries)
	}
	return b.String(), nil
}

// formatSize 人类可读文件大小（B/KB/MB/GB…，保留 1 位小数）。
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// searchMaxScanBytes 内容搜索时每个文件最多扫描的字节数（前 256KB）。
const searchMaxScanBytes = 256 << 10

// fileOpsSearch 在目录下按文件名/内容递归搜索，返回命中列表（只读操作）。
//
// 输出与 list 风格一致（类型 + 名称 + 命中方式），名称即真实文件名：
//
//	在 /app 下搜索 "report"（命中 2 条）：
//	- [文件] docs/report.md（文件名匹配，12.3 KB）
//	- [文件] notes/2026.md（内容匹配，2.1 KB）
func fileOpsSearch(ctx context.Context, full, base, prefix, query string, nameOnly bool, maxResults int) (string, error) {
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file_ops: 目录不存在: %s", displayPath(base, prefix, full))
		}
		return "", fmt.Errorf("file_ops: 读取目录信息失败: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file_ops: %s 不是目录，请用 read/stat 处理文件", displayPath(base, prefix, full))
	}
	if maxResults <= 0 || maxResults > 200 {
		maxResults = 50
	}
	q := strings.ToLower(query)

	relBase := full
	if base != "" {
		relBase = base
	}

	type hit struct {
		rel  string
		kind string // 文件名 / 内容
		size int64
		dir  bool
	}
	hits := make([]hit, 0, 16)

	err = filepath.WalkDir(full, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil // 跳过不可读条目，不中断整个搜索
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, _ := filepath.Rel(relBase, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if strings.Contains(strings.ToLower(d.Name()), q) {
				hits = append(hits, hit{rel: prefix + filepath.ToSlash(rel), kind: "文件名", dir: true})
			}
			return nil // 目录不递归其自身做内容匹配；子目录继续遍历
		}
		// 文件名匹配（任何模式都生效）
		kind := ""
		if strings.Contains(strings.ToLower(d.Name()), q) {
			kind = "文件名"
		}
		// 内容匹配（默认开启；仅当文件名未命中时读文件判断，省 IO）
		if kind == "" && !nameOnly {
			if f, e := os.Open(p); e == nil {
				buf := make([]byte, searchMaxScanBytes)
				n, _ := f.Read(buf)
				_ = f.Close()
				if utf8.Valid(buf[:n]) && strings.Contains(strings.ToLower(string(buf[:n])), q) {
					kind = "内容"
				}
			}
		}
		if kind != "" {
			var size int64
			if fi, e := d.Info(); e == nil {
				size = fi.Size()
			}
			hits = append(hits, hit{rel: prefix + filepath.ToSlash(rel), kind: kind, size: size})
		}
		if len(hits) >= maxResults {
			return filepath.SkipAll // 达到上限提前结束
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("file_ops: 搜索失败: %w", err)
	}

	if len(hits) == 0 {
		return fmt.Sprintf("在 %s 下未找到与 %q 匹配的文件或目录", displayPath(base, prefix, full), query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "在 %s 下搜索 %q（命中 %d 条）：\n", displayPath(base, prefix, full), query, len(hits))
	for _, it := range hits {
		if it.dir {
			fmt.Fprintf(&b, "- [目录] %s/（%s匹配）\n", it.rel, it.kind)
		} else {
			fmt.Fprintf(&b, "- [文件] %s（%s匹配，%s）\n", it.rel, it.kind, formatSize(it.size))
		}
	}
	return b.String(), nil
}

// fileOpsStat 查看文件/目录信息。
func fileOpsStat(full, base, prefix string) (string, error) {
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file_ops: 路径不存在: %s", displayPath(base, prefix, full))
		}
		return "", fmt.Errorf("file_ops: 读取信息失败: %w", err)
	}
	kind := "文件"
	if info.IsDir() {
		kind = "目录"
	}
	return fmt.Sprintf("路径：%s\n类型：%s\n大小：%s\n修改时间：%s",
		displayPath(base, prefix, full), kind, formatSize(info.Size()), info.ModTime().Format(time.RFC3339)), nil
}

// 编译期断言：FileOpsTool 实现 Tool 接口。
var _ tool.Tool = (*FileOpsTool)(nil)
