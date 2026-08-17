package adminsvc

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// makeZip 构造一个内存 zip：entries 为 "path|content"（内容为 []byte 序列化文本）。
func makeZip(t *testing.T, entries []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		i := strings.IndexByte(e, '|')
		name, content := e[:i], e[i+1:]
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%q): %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip.Write(%q): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

// sk 构造带语义版本号的合法 SKILL.md 内容。
func sk(name, desc, ver, body string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\nmetadata:\n  version: %s\n---\n%s", name, desc, ver, body)
}

func TestSkillStoreRoundtrip(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)

	content := sk("emoji-helper", "把中文文案转成 Emoji", "1.0.0", "开心→😄")
	created, err := store.Create(context.Background(), "emoji-helper", content)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.Valid {
		t.Fatalf("skill 应为 valid，got error=%q", created.Error)
	}
	if created.ToolName != "skill_emoji_helper" {
		t.Errorf("ToolName = %q, want skill_emoji_helper", created.ToolName)
	}
	if created.Description != "把中文文案转成 Emoji" {
		t.Errorf("Description = %q", created.Description)
	}
	if created.SemVer != "1.0.0" {
		t.Errorf("SemVer = %q, want 1.0.0", created.SemVer)
	}
	if created.FileCount != 0 {
		t.Errorf("FileCount = %d, want 0", created.FileCount)
	}

	// List
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "emoji-helper" {
		t.Fatalf("List = %+v, want 1 skill emoji-helper", list)
	}

	// Get
	got, err := store.Get(context.Background(), "emoji-helper")
	if err != nil || !got.Valid {
		t.Fatalf("Get: %v %+v", err, got)
	}
	if got.Content != content {
		t.Errorf("Get content mismatch")
	}
}

func TestSkillStoreChineseName(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)

	content := sk("数据分析助手", "自动分析数据并生成报告", "1.0.0", "按步骤分析。")
	created, err := store.Create(context.Background(), "数据分析助手", content)
	if err != nil {
		t.Fatalf("Create 中文名技能: %v", err)
	}
	if !created.Valid {
		t.Fatalf("中文名技能应 valid: %s", created.Error)
	}
	// 工具名必须是 ASCII：skill_ 前缀 + 净化 slug + 哈希后缀。
	if !strings.HasPrefix(created.ToolName, "skill_") {
		t.Fatalf("工具名应为 skill_ 前缀: %q", created.ToolName)
	}
	for _, r := range created.ToolName {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("工具名含非法字符 %q: %q", r, created.ToolName)
		}
	}
	other, err := store.Create(context.Background(), "天气播报", sk("天气播报", "查询天气", "1.0.0", "正文"))
	if err != nil {
		t.Fatal(err)
	}
	if other.ToolName == created.ToolName {
		t.Fatalf("两个不同中文名技能工具名冲突: %q", created.ToolName)
	}
	// 目录名保持中文（模型经 @skills/ 路径访问）。
	if _, err := os.Stat(filepath.Join(root, "数据分析助手", "SKILL.md")); err != nil {
		t.Fatalf("中文目录未落盘: %v", err)
	}
}

func TestSkillStoreCreateDuplicate(t *testing.T) {
	store := newSkillStore(t.TempDir())
	content := sk("a", "d", "1.0.0", "body")
	if _, err := store.Create(context.Background(), "a", content); err != nil {
		t.Fatal(err)
	}
	// 同名新建 → 拒绝（创建是"建新技能"，已存在应走更新/上传）。
	_, err := store.Create(context.Background(), "a", content)
	if apperr.CodeOf(err) != apperr.CodeAlreadyExists {
		t.Fatalf("同名 Create 应返回 ALREADY_EXISTS，got %v", err)
	}
}

func TestSkillStoreInvalidName(t *testing.T) {
	store := newSkillStore(t.TempDir())
	for _, name := range []string{"../evil", "a/b", ".hidden", "有 空格", "a..b", "_underscore", "-dash", ""} {
		if _, err := store.Create(context.Background(), name, sk(name, "d", "1.0.0", "b")); err == nil {
			t.Errorf("Create(%q) 应拒绝（防目录穿越/非法字符）", name)
		}
	}
}

func TestSkillStoreFrontmatterNameMismatch(t *testing.T) {
	store := newSkillStore(t.TempDir())
	// frontmatter name 与目录名不一致 → 拒绝（避免工具名错位）
	content := sk("other", "d", "1.0.0", "body")
	_, err := store.Create(context.Background(), "mydir", content)
	if apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("name 不一致应拒绝，got %v", err)
	}
}

func TestSkillStoreEmptyOrInvalidContent(t *testing.T) {
	store := newSkillStore(t.TempDir())
	cases := []struct {
		name, content string
	}{
		{"empty", ""},
		{"no_frontmatter", "纯文本没有 frontmatter"},
		{"missing_desc", "---\nname: x\nmetadata:\n  version: 1.0.0\n---\nbody"},
		{"missing_version", "---\nname: x\ndescription: d\n---\nbody"},
		{"bad_version", "---\nname: x\ndescription: d\nmetadata:\n  version: v1\n---\nbody"},
		{"huge", strings.Repeat("a", maxSkillBytes+1)},
	}
	for _, c := range cases {
		if _, err := store.Create(context.Background(), c.name, c.content); err == nil {
			t.Errorf("Create(%q) 应拒绝非法内容", c.name)
		}
	}
}

func TestSkillStoreUpdateVersionSemantics(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)
	if _, err := store.Create(context.Background(), "s", sk("s", "v1", "1.0.0", "body1")); err != nil {
		t.Fatal(err)
	}

	// 同版本同内容 → 幂等（不产生历史）
	cur, err := store.Update(context.Background(), "s", sk("s", "v1", "1.0.0", "body1"), false)
	if err != nil || cur.Version != 1 || len(cur.Versions) != 0 {
		t.Fatalf("同版本同内容应幂等: %+v, err=%v", cur, err)
	}

	// 新版本号 → 发布新版本（旧内容进历史）
	cur, err = store.Update(context.Background(), "s", sk("s", "v2", "1.1.0", "body2"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 2 || len(cur.Versions) != 1 || cur.Versions[0].SemVer != "1.0.0" {
		t.Fatalf("新版本后 version=%d versions=%+v", cur.Version, cur.Versions)
	}

	// 同版本号但内容不同 → VERSION_CONFLICT
	_, err = store.Update(context.Background(), "s", sk("s", "v2改", "1.1.0", "body2-changed"), false)
	if apperr.CodeOf(err) != apperr.CodeVersionConflict {
		t.Fatalf("同版本不同内容应 VERSION_CONFLICT，got %v", err)
	}

	// overwrite=true → 覆盖该版本（当前内容被替换，不产生重复历史）
	cur, err = store.Update(context.Background(), "s", sk("s", "v2改", "1.1.0", "body2-changed"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cur.Content, "body2-changed") {
		t.Fatalf("覆盖后内容未生效: %q", cur.Content)
	}
	// 同版本覆盖：历史仍只有 1.0.0（同一版本号只能有一份）
	if len(cur.Versions) != 1 || cur.Versions[0].SemVer != "1.0.0" {
		t.Fatalf("覆盖后 versions=%+v，应只有 [1.0.0]", cur.Versions)
	}

	// 更新不存在的技能 → NotFound
	_, err = store.Update(context.Background(), "nope", sk("nope", "d", "1.0.0", "b"), false)
	if apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("Update nonexistent: code = %s, want NotFound", apperr.CodeOf(err))
	}
}

func TestSkillStoreUpdateAndDelete(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)
	if _, err := store.Create(context.Background(), "s", sk("s", "v1", "1.0.0", "body1")); err != nil {
		t.Fatal(err)
	}

	// Update 覆盖（版本不同 → 新版本）
	got, err := store.Update(context.Background(), "s", sk("s", "v2", "2.0.0", "body2"), false)
	if err != nil || !got.Valid {
		t.Fatalf("Update: %v %+v", err, got)
	}
	if got.Description != "v2" || !strings.Contains(got.Content, "body2") {
		t.Errorf("Update 后内容未生效: %+v", got)
	}

	// 历史无版本号的旧技能兼容编辑（不写版本号仍可更新）
	if err := os.WriteFile(filepath.Join(root, "s", "SKILL.md"), []byte("---\nname: s\ndescription: legacy\n---\nold"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Update(context.Background(), "s", "---\nname: s\ndescription: legacy2\n---\nold2", false)
	if err != nil {
		t.Fatalf("历史技能无版本号编辑应放行: %v", err)
	}
	if legacy.Version != 2 {
		t.Fatalf("legacy 编辑后 version = %d, want 2（历史仍只有 1.0.0 一份）", legacy.Version)
	}

	// Delete + Get 404
	if err := store.Delete(context.Background(), "s"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Windows 上目录删除可能是延迟的（索引/杀软短暂持有句柄），轮询等待真正消失。
	gone := filepath.Join(root, "s")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(gone); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Delete 后目录仍存在: %s", gone)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, err = store.Get(context.Background(), "s")
	if apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("Delete 后 Get: code = %s, want NotFound", apperr.CodeOf(err))
	}
}

func TestSkillStoreListsInvalidSkill(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)
	// 手工构造一个坏技能：SKILL.md 缺失
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 加一个正常技能
	if _, err := store.Create(context.Background(), "good", sk("good", "d", "1.0.0", "b")); err != nil {
		t.Fatal(err)
	}

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	byName := map[string]Skill{}
	for _, sk := range list {
		byName[sk.Name] = sk
	}
	if !byName["good"].Valid {
		t.Errorf("good 技能应为 valid")
	}
	if byName["broken"].Valid {
		t.Errorf("broken 技能应为 invalid")
	}
}

func TestSkillStoreVersioning(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)

	// 创建 = v1.0.0
	cur, err := store.Create(context.Background(), "s", sk("s", "v1", "1.0.0", "body1"))
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 1 || len(cur.Versions) != 0 {
		t.Fatalf("创建后 version = %d, versions = %+v; want 1 / 空", cur.Version, cur.Versions)
	}

	// 修改（新版本 2.0.0），历史含 1.0.0
	cur, err = store.Update(context.Background(), "s", sk("s", "v2", "2.0.0", "body2"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 2 {
		t.Fatalf("Update 后 version = %d, want 2", cur.Version)
	}
	if len(cur.Versions) != 1 || cur.Versions[0].SemVer != "1.0.0" {
		t.Fatalf("历史版本 = %+v, want [1.0.0]", cur.Versions)
	}

	// 内容相同 → 不产生新版本
	cur, err = store.Update(context.Background(), "s", sk("s", "v2", "2.0.0", "body2"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 2 || len(cur.Versions) != 1 {
		t.Fatalf("内容相同不应产生新版本: version=%d versions=%+v", cur.Version, cur.Versions)
	}

	// 回滚到 1.0.0 → 内容恢复；原当前 2.0.0 入历史（去重后各版本唯一）
	cur, err = store.RestoreVersion(context.Background(), "s", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cur.Content, "body1") {
		t.Fatalf("回滚后内容应为 1.0.0: %q", cur.Content)
	}
	if cur.SemVer != "1.0.0" {
		t.Fatalf("回滚后 SemVer = %q, want 1.0.0", cur.SemVer)
	}
	if len(cur.Versions) != 1 || cur.Versions[0].SemVer != "2.0.0" {
		t.Fatalf("回滚后 versions=%+v，应只有 [2.0.0]", cur.Versions)
	}

	// 回滚当前已生效版本 → 幂等
	if _, err := store.RestoreVersion(context.Background(), "s", "1.0.0"); err != nil {
		t.Fatalf("回滚当前版本应幂等成功: %v", err)
	}

	// 回滚不存在的版本 → NotFound
	_, err = store.RestoreVersion(context.Background(), "s", "9.9.9")
	if apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("回滚不存在版本: code = %s, want NotFound", apperr.CodeOf(err))
	}
}

// TestSkillStoreVersionUniquenessAcrossHistory 核心：同一 name 下同一 semver
// 只能有一份——上传/更新已存在于【历史】中的版本号也要冲突，覆盖时去重。
func TestSkillStoreVersionUniquenessAcrossHistory(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)
	if _, err := store.Create(context.Background(), "s", sk("s", "v1", "1.0.0", "body1")); err != nil {
		t.Fatal(err)
	}
	// 发布 1.1.0 → 1.0.0 入历史
	if _, err := store.Update(context.Background(), "s", sk("s", "v2", "1.1.0", "body2"), false); err != nil {
		t.Fatal(err)
	}
	// 发布 1.2.0 → 1.1.0 入历史
	if _, err := store.Update(context.Background(), "s", sk("s", "v3", "1.2.0", "body3"), false); err != nil {
		t.Fatal(err)
	}
	cur, _ := store.Get(context.Background(), "s")
	if len(cur.Versions) != 2 {
		t.Fatalf("历史应含 2 个版本: %+v", cur.Versions)
	}

	// 重新上传历史版本 1.0.0（内容不同）→ 版本冲突
	_, err := store.Update(context.Background(), "s", sk("s", "v1改", "1.0.0", "body1-changed"), false)
	if apperr.CodeOf(err) != apperr.CodeVersionConflict {
		t.Fatalf("上传历史版本号应 VERSION_CONFLICT，got %v", err)
	}
	// overwrite=true 覆盖历史版本 1.0.0：1.0.0 成为当前，原当前 1.2.0 入历史，历史只剩 2 份
	cur, err = store.Update(context.Background(), "s", sk("s", "v1改", "1.0.0", "body1-changed"), true)
	if err != nil {
		t.Fatal(err)
	}
	if cur.SemVer != "1.0.0" || !strings.Contains(cur.Content, "body1-changed") {
		t.Fatalf("覆盖后当前应为 1.0.0 新内容: %+v", cur)
	}
	if len(cur.Versions) != 2 {
		t.Fatalf("覆盖后历史应仍只有 2 份（去重）: %+v", cur.Versions)
	}
	semvers := map[string]bool{}
	for _, v := range cur.Versions {
		semvers[v.SemVer] = true
	}
	if !semvers["1.1.0"] || !semvers["1.2.0"] || semvers["1.0.0"] {
		t.Fatalf("历史版本应含 1.1.0/1.2.0 且不含 1.0.0: %+v", cur.Versions)
	}
}

func TestSkillStoreSetEnabled(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)
	if _, err := store.Create(context.Background(), "s", sk("s", "d", "1.0.0", "b")); err != nil {
		t.Fatal(err)
	}

	sk, err := store.SetEnabled(context.Background(), "s", false)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Enabled {
		t.Error("SetEnabled(false) 后应为 disabled")
	}
	// 禁用标记文件落盘
	if _, err := os.Stat(filepath.Join(root, "s", disabledFileName)); err != nil {
		t.Fatalf(".disabled 标记文件应存在: %v", err)
	}

	sk, err = store.SetEnabled(context.Background(), "s", true)
	if err != nil {
		t.Fatal(err)
	}
	if !sk.Enabled {
		t.Error("SetEnabled(true) 后应为 enabled")
	}
	if _, err := os.Stat(filepath.Join(root, "s", disabledFileName)); !os.IsNotExist(err) {
		t.Fatalf(".disabled 标记文件应被删除: %v", err)
	}

	// 禁用不存在的技能 → NotFound
	if _, err := store.SetEnabled(context.Background(), "nope", false); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("SetEnabled nonexistent: code = %s, want NotFound", apperr.CodeOf(err))
	}
}

func TestSkillStoreUploadAutoName(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)

	// 前端只传文件，不传名字：名称从 frontmatter name 提取。
	skillMD := sk("my-script", "多文件脚本技能", "1.0.0", "用 scripts/run.py 完成任务")
	zipData := makeZip(t, []string{
		"my-script/SKILL.md|" + skillMD,
		"my-script/scripts/run.py|print('hi')",
		"my-script/README.md|docs",
	})

	sk, err := store.Upload(context.Background(), "随便.zip", zipData, false)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if sk.Name != "my-script" {
		t.Fatalf("应自动提取名字 my-script，got %q", sk.Name)
	}
	if !sk.Valid {
		t.Fatalf("上传后应为 valid: %s", sk.Error)
	}
	if sk.Version != 1 {
		t.Errorf("上传后 version = %d, want 1", sk.Version)
	}
	if sk.SemVer != "1.0.0" {
		t.Errorf("上传后 SemVer = %q, want 1.0.0", sk.SemVer)
	}
	if sk.FileCount != 2 {
		t.Errorf("FileCount = %d, want 2（run.py + README.md）", sk.FileCount)
	}
	// 脚本文件已落盘（原始结构保留）
	if _, err := os.Stat(filepath.Join(root, "my-script", "scripts", "run.py")); err != nil {
		t.Fatalf("run.py 未解压落盘: %v", err)
	}
}

func TestSkillStoreUploadPreservesStructure(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)

	// 多层结构 + 顶层散落文件并存：主 SKILL.md 相对引用 docs/ref.md 与 ref/x.md
	skillMD := sk("nested-skill", "嵌套结构技能", "1.0.0", "详见 docs/ref.md 与 ref/x.md")
	zipData := makeZip(t, []string{
		"nested-skill/SKILL.md|" + skillMD,
		"nested-skill/docs/ref.md|# 参考文档",
		"nested-skill/ref/x.md|# 说明",
		"nested-skill/assets/img/pic.png|imgdata",
		"README.md|顶层散落文件（不应干扰定位）",
	})

	sk, err := store.Upload(context.Background(), "nested.zip", zipData, false)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if sk.Name != "nested-skill" {
		t.Fatalf("应提取包裹目录名 nested-skill，got %q", sk.Name)
	}
	for _, rel := range []string{
		"docs/ref.md", "ref/x.md", "assets/img/pic.png",
	} {
		if _, err := os.Stat(filepath.Join(root, "nested-skill", rel)); err != nil {
			t.Fatalf("结构未保留 %s: %v", rel, err)
		}
	}
}

func TestSkillStoreUploadBackslashZipEntries(t *testing.T) {
	// 回归：Windows 生成 zip 时常用 filepath.Join，条目名是字面反斜杠
	// （如 `ref-demo\SKILL.md`）。unzipSafe 必须先把反斜杠归一为正斜杠再落盘，
	// 否则 Linux 会把 `docs\guide.md` 拍平成含反斜杠的单一文件名，
	// findSkillRoot 找不到 SKILL.md → 上传 400、磁盘结构损坏。
	root := t.TempDir()
	store := newSkillStore(root)

	skillMD := sk("ref-demo", "反斜杠条目名技能", "1.0.0", "详见 docs/guide.md 与 ref/a.md")
	zipData := makeZip(t, []string{
		`ref-demo\SKILL.md|` + skillMD,
		`ref-demo\docs\guide.md|# 指南`,
		`ref-demo\ref\a.md|# 参考`,
	})

	skv, err := store.Upload(context.Background(), "ref-demo.zip", zipData, false)
	if err != nil {
		t.Fatalf("Upload(反斜杠条目名): %v", err)
	}
	if skv.Name != "ref-demo" {
		t.Fatalf("技能名应为 ref-demo，got %q", skv.Name)
	}
	// 物理落盘必须是真实目录嵌套，而非含反斜杠的扁平文件名。
	for _, rel := range []string{"docs/guide.md", "ref/a.md"} {
		if _, err := os.Stat(filepath.Join(root, "ref-demo", rel)); err != nil {
			t.Fatalf("结构未保留 %s（应建真实目录而非拍平）: %v", rel, err)
		}
	}
	if len(skv.Files) != 2 || skv.Files[0] != "docs/guide.md" || skv.Files[1] != "ref/a.md" {
		t.Fatalf("files 应为 [docs/guide.md ref/a.md]，got %v", skv.Files)
	}
}

func TestSkillStoreUploadFlatZipFilenameFallback(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)
	// 平铺 zip（SKILL.md 在根、无 frontmatter name）→ 回退上传文件名（中文名）。
	zipData := makeZip(t, []string{
		"SKILL.md|---\ndescription: 平铺技能\nmetadata:\n  version: 1.0.0\n---\n正文",
	})
	sk, err := store.Upload(context.Background(), "数据分析.zip", zipData, false)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if sk.Name != "数据分析" {
		t.Fatalf("平铺 zip 应回退文件名作为技能名，got %q", sk.Name)
	}
	if _, err := os.Stat(filepath.Join(root, "数据分析", "SKILL.md")); err != nil {
		t.Fatalf("中文技能目录未落盘: %v", err)
	}
}

func TestSkillStoreUploadVersionConflict(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)

	v1 := sk("my-script", "第一版", "1.0.0", "body1")
	if _, err := store.Upload(context.Background(), "a.zip", makeZip(t, []string{"SKILL.md|" + v1}), false); err != nil {
		t.Fatal(err)
	}
	// 同版本号不同内容 → VERSION_CONFLICT
	v1b := sk("my-script", "第一版改动", "1.0.0", "body1-changed")
	_, err := store.Upload(context.Background(), "b.zip", makeZip(t, []string{"SKILL.md|" + v1b}), false)
	if apperr.CodeOf(err) != apperr.CodeVersionConflict {
		t.Fatalf("同版本不同内容上传应 VERSION_CONFLICT，got %v", err)
	}
	// overwrite=true → 覆盖
	cur, err := store.Upload(context.Background(), "c.zip", makeZip(t, []string{"SKILL.md|" + v1b}), true)
	if err != nil {
		t.Fatal(err)
	}
	if cur.SemVer != "1.0.0" || !strings.Contains(cur.Content, "body1-changed") {
		t.Fatalf("覆盖后当前应为 1.0.0 新内容: %+v", cur)
	}
	if len(cur.Versions) != 0 {
		t.Fatalf("同版本覆盖不应产生重复历史: %+v", cur.Versions)
	}
	// 新版本号但名字已存在 → 仍须显式确认（409 提示发布新版本并切换生效）
	v2 := sk("my-script", "第二版", "2.0.0", "body2")
	_, err = store.Upload(context.Background(), "d.zip", makeZip(t, []string{"SKILL.md|" + v2}), false)
	if apperr.CodeOf(err) != apperr.CodeVersionConflict {
		t.Fatalf("同名新版本上传未确认应 VERSION_CONFLICT，got %v", err)
	}
	// overwrite=true → 发布新版本并切换生效
	cur, err = store.Upload(context.Background(), "d.zip", makeZip(t, []string{"SKILL.md|" + v2}), true)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 2 || cur.SemVer != "2.0.0" {
		t.Fatalf("新版本后 version=%d semver=%q", cur.Version, cur.SemVer)
	}
	if len(cur.Versions) != 1 || cur.Versions[0].SemVer != "1.0.0" {
		t.Fatalf("新版本后历史应只有 [1.0.0]: %+v", cur.Versions)
	}
}

// TestSkillStoreUploadSameContentStillConfirms 回归：同名同版本同内容（完全相同的副本）
// 也必须返回 409 提示，杜绝"静默幂等覆盖"（历史 bug：直接 201 不弹确认）。
func TestSkillStoreUploadSameContentStillConfirms(t *testing.T) {
	root := t.TempDir()
	store := newSkillStore(root)

	v1 := sk("my-script", "第一版", "1.0.0", "body1")
	if _, err := store.Upload(context.Background(), "a.zip", makeZip(t, []string{"SKILL.md|" + v1}), false); err != nil {
		t.Fatal(err)
	}
	// 完全相同的内容再次上传：不得静默 201，必须返回 VERSION_CONFLICT 提示。
	_, err := store.Upload(context.Background(), "a.zip", makeZip(t, []string{"SKILL.md|" + v1}), false)
	if apperr.CodeOf(err) != apperr.CodeVersionConflict {
		t.Fatalf("同名同版本同内容上传应 VERSION_CONFLICT（提示覆盖），got %v", err)
	}
	// 确认覆盖（overwrite=true）→ 成功且不产生重复历史。
	cur, err := store.Upload(context.Background(), "a.zip", makeZip(t, []string{"SKILL.md|" + v1}), true)
	if err != nil {
		t.Fatal(err)
	}
	if cur.SemVer != "1.0.0" || len(cur.Versions) != 0 {
		t.Fatalf("覆盖后应仍是 1.0.0 且无重复历史: %+v", cur)
	}
}

func TestSkillStoreUploadRejects(t *testing.T) {
	store := newSkillStore(t.TempDir())

	// zip-slip：路径穿越到技能根外
	evil := makeZip(t, []string{"../evil/SKILL.md|" + sk("evil", "d", "1.0.0", "b")})
	if _, err := store.Upload(context.Background(), "e.zip", evil, false); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("zip-slip 应被拒绝: %v", err)
	}
	// 绝对路径
	abs := makeZip(t, []string{"/etc/SKILL.md|" + sk("e", "d", "1.0.0", "b")})
	if _, err := store.Upload(context.Background(), "a.zip", abs, false); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("绝对路径应被拒绝: %v", err)
	}
	// 无 SKILL.md
	noSkill := makeZip(t, []string{"a.txt|hi"})
	if _, err := store.Upload(context.Background(), "n.zip", noSkill, false); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("缺少 SKILL.md 应被拒绝: %v", err)
	}
	// 有 SKILL.md 但缺版本号 → 拒绝（关键信息缺失）
	noVersion := makeZip(t, []string{"s/SKILL.md|---\nname: s\ndescription: d\n---\nbody"})
	if _, err := store.Upload(context.Background(), "v.zip", noVersion, false); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("缺版本号应被拒绝: %v", err)
	}
	// 有 SKILL.md 但缺 name（且平铺、无文件名兜底）→ 拒绝
	noName := makeZip(t, []string{"SKILL.md|---\ndescription: d\nmetadata:\n  version: 1.0.0\n---\nbody"})
	if _, err := store.Upload(context.Background(), "", noName, false); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("缺 name 应被拒绝: %v", err)
	}
}
