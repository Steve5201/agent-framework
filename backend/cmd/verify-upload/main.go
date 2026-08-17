// verify-upload 上传验证工具：用真实 HTTP 把 testdata/ 下的演示 zip 上传到
// 本地起的管理端服务，打印每个包的 HTTP 状态、错误信息，以及磁盘上实际创建的
// 目录树（带 [目录]/[文件] 标记，可核对"ref/ 文件夹 + a.md"而非"ref a.md 拍平"）。
//
// 用法（在 backend/ 下）：
//
//	go run ./cmd/verify-upload
//
// 环境：任意机器（本工具完全本地，不依赖 docker / 数据库 / 网络）。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/Steve5201/agent-backend/internal/adminsvc"
)

func main() {
	root, err := os.MkdirTemp("", "verify-upload-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	svc, err := adminsvc.NewService(adminsvc.Config{
		SkillsDir:     filepath.Join(root, "skills"),
		McpConfigFile: filepath.Join(root, "mcp_servers.json"),
		McpServersDir: filepath.Join(root, "mcp-servers"),
	})
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	repo := findRepoRoot() // 向上查找含 testdata/ 的仓库根

	fmt.Println("===== 技能上传（/v1/admin/skills/upload）=====")
	for _, name := range []string{"ref-demo.zip", "ref-demo-flat.zip"} {
		z := filepath.Join(repo, "testdata", "skills", name)
		uploadSkill(srv.URL, z)
	}

	fmt.Println("\n===== 本地 MCP 上传（/v1/admin/mcp-servers/upload）=====")
	z := filepath.Join(repo, "testdata", "mcp", "calculator-mcp.zip")
	uploadMcp(srv.URL, z)

	fmt.Println("\n===== 磁盘目录树（实际落盘结果）=====")
	printTree(filepath.Join(root, "skills"), "skills/")
	printTree(filepath.Join(root, "mcp-servers"), "mcp-servers/")
}

// findRepoRoot 从当前工作目录向上查找含 testdata/ 的仓库根。
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "testdata")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func uploadSkill(base, zipPath string) {
	status, body := post(base+"/v1/admin/skills/upload", zipPath)
	fmt.Printf("\n[上传] %s\n  HTTP %d\n  响应: %s\n", filepath.Base(zipPath), status, pretty(body))
	if sk, ok := body["skill"].(map[string]any); ok {
		if files, ok := sk["files"].([]any); ok {
			fmt.Printf("  系统 files 字段: %s\n", joinAny(files))
		}
	}
}

func uploadMcp(base, zipPath string) {
	status, body := post(base+"/v1/admin/mcp-servers/upload", zipPath)
	fmt.Printf("\n[上传] %s\n  HTTP %d\n  响应: %s\n", filepath.Base(zipPath), status, pretty(body))
	if s, ok := body["server"].(map[string]any); ok {
		fmt.Printf("  command=%v args=%v cwd=%v\n", s["command"], s["args"], s["cwd"])
	}
}

func post(url, zipPath string) (int, map[string]any) {
	data, err := os.ReadFile(zipPath)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", filepath.Base(zipPath))
	_, _ = fw.Write(data)
	_ = w.Close()
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

func pretty(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func joinAny(a []any) string {
	parts := make([]string, 0, len(a))
	for _, v := range a {
		parts = append(parts, fmt.Sprint(v))
	}
	return strings.Join(parts, "  ")
}

func printTree(root, label string) {
	if _, err := os.Stat(root); err != nil {
		fmt.Printf("（%s不存在）\n", label)
		return
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, ".versions") || rel == ".disabled" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		kind := "文件"
		if d.IsDir() {
			kind = "目录"
		}
		fmt.Printf("  %s%s[%s] %s\n", label, strings.Repeat("  ", strings.Count(rel, string(filepath.Separator))), kind, filepath.ToSlash(rel))
		return nil
	})
}
