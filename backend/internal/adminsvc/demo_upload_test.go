package adminsvc

// demo_upload_test.go —— 用仓库 testdata/ 下的真实 zip 做文件系统级验证：
//  1. 技能 zip（含 ref/、docs/ 嵌套）上传后，磁盘上必须保留 ref/a.md 目录结构，
//     绝不允许变成名为 "ref a.md" 的单个文件（用户实测的拍平症状）；
//  2. 平铺 zip（SKILL.md 在根）同样保留嵌套；
//  3. 本地 MCP 代码 zip（python + utils/ 子目录）上传后 cwd 相对化 + 代码完整落盘。
//
// 运行：cd backend && go test ./internal/adminsvc -run Demo -v
// 输出会打印上传后的实际磁盘目录树，可直接核对。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdataPath 定位仓库根 testdata/（相对本包目录 backend/internal/adminsvc）。
func testdataPath(elem ...string) string {
	return filepath.Join(append([]string{"..", "..", "..", "testdata"}, elem...)...)
}

// dumpTree 打印目录树（含 SKILL.md 与各子目录文件），供 go test -v 观察。
func dumpTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, ".versions") || strings.HasPrefix(rel, ".disabled") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		marker := "文件"
		if d.IsDir() {
			marker = "目录"
		}
		line := strings.Repeat("  ", strings.Count(rel, string(filepath.Separator))) + "[" + marker + "] " + filepath.ToSlash(rel)
		out = append(out, line)
		t.Log(line)
		return nil
	})
	return out
}

// assertFileOnDisk 断言磁盘上存在该相对路径（必须是"文件夹+文件"结构）。
func assertFileOnDisk(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("磁盘上缺少 %s（结构未保留）: %v", rel, err)
	}
}

// assertNoFlatFile 断言磁盘上不存在拍平后的伪文件（如 "ref a.md"）。
func assertNoFlatFile(t *testing.T, root, fake string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(fake))); err == nil {
		t.Fatalf("出现拍平伪文件 %s（应为 %s 目录结构）！", fake, strings.ReplaceAll(fake, " ", "/"))
	}
}

// TestSkillUploadDemoZip_WrapperAndFlat 用真实 zip 验证：
// 包裹式（ref-demo.zip，ref-demo/SKILL.md…）与平铺式（ref-demo-flat.zip，SKILL.md 在根）。
// 两个 zip 的 SKILL.md name 都是 ref-demo，因此每个子用例用独立的服务实例（独立技能根），
// 避免"同名已存在 → 409 需确认"（新版上传语义）干扰结构断言。
func TestSkillUploadDemoZip_WrapperAndFlat(t *testing.T) {
	for name, zipName := range map[string]string{"wrapper": "ref-demo.zip", "flat": "ref-demo-flat.zip"} {
		t.Run(name, func(t *testing.T) {
			_, srv, root := newHTTPService(t)
			data, err := os.ReadFile(testdataPath("skills", zipName))
			if err != nil {
				t.Fatalf("读取演示 zip 失败: %v", err)
			}
			status, body := doReq(t, srv, multipartUpload(t, srv, nil, "file", zipName, data, ""))
			if status != 201 {
				t.Fatalf("上传失败 %d: %+v", status, body)
			}
			// 多租户：默认域 tutor → <skills>/tutor/<name>/。
			skillDir := filepath.Join(root, "skills", "tutor", "ref-demo")
			// 必须保留嵌套结构（文件夹 ref + 文件 a.md）。
			assertFileOnDisk(t, skillDir, "SKILL.md")
			assertFileOnDisk(t, skillDir, "ref/a.md")
			assertFileOnDisk(t, skillDir, "docs/guide.md")
			// 绝不允许出现拍平的伪文件 "ref a.md" / "docs guide.md"。
			assertNoFlatFile(t, skillDir, "ref a.md")
			assertNoFlatFile(t, skillDir, "docs guide.md")
			t.Logf("== 上传后磁盘目录树（%s.zip）==", zipName)
			dumpTree(t, skillDir)
			// 响应 files 字段必须包含嵌套路径。
			if sk, ok := body["skill"].(map[string]any); ok {
				if files, ok := sk["files"].([]any); ok {
					joined := ""
					for _, f := range files {
						joined += f.(string) + " "
					}
					if !strings.Contains(joined, "ref/a.md") {
						t.Fatalf("响应 files 应包含 ref/a.md，实际: %s", joined)
					}
				}
			}
		})
	}
}

// TestMcpUploadLocal_DemoZip 用真实 python MCP zip 验证：入口检测、cwd 相对化、嵌套代码落盘。
func TestMcpUploadLocal_DemoZip(t *testing.T) {
	serversDir := filepath.Join(t.TempDir(), "mcp-servers")
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp.json"), serversDir)

	data, err := os.ReadFile(testdataPath("mcp", "calculator-mcp.zip"))
	if err != nil {
		t.Fatalf("读取演示 MCP zip 失败: %v", err)
	}
	cfg, err := store.UploadLocal(t.Context(), "", "", "calculator-mcp.zip", data, false)
	if err != nil {
		t.Fatalf("UploadLocal: %v", err)
	}
	if cfg.Name != "calculator-mcp" || cfg.Command != "python3" || len(cfg.Args) != 1 || cfg.Args[0] != "main.py" {
		t.Fatalf("stdio 配置错误: %+v", cfg)
	}
	// cwd 必须能解析到代码目录（相对或绝对均可，但要指向 mcp-servers/calculator-mcp）。
	if !strings.HasSuffix(strings.ReplaceAll(cfg.Cwd, "\\", "/"), "mcp-servers/calculator-mcp") {
		t.Fatalf("cwd 未指向代码目录: %q", cfg.Cwd)
	}
	codeDir := filepath.Join(serversDir, "calculator-mcp")
	assertFileOnDisk(t, codeDir, "main.py")
	assertFileOnDisk(t, codeDir, "requirements.txt")
	assertFileOnDisk(t, codeDir, "utils/math_helpers.py") // 嵌套子目录保留
	assertNoFlatFile(t, codeDir, "utils math_helpers.py")
	t.Log("== 上传后本地 MCP 目录树 ==")
	dumpTree(t, codeDir)
}
