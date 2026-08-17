// privatemedia_test.go —— 聊天媒体迁入用户私有工作区的单测。
package agentsvc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Steve5201/agent-backend/internal/rag/ingest"
	"github.com/Steve5201/agent-framework/llm"
	"go.uber.org/zap"
)

// newTestServiceWithWork 构造带指定工作区根的 Service（媒体迁移测试用）。
func newTestServiceWithWork(workRoot string) (*Service, error) {
	reg, err := DefaultToolSet()
	if err != nil {
		return nil, err
	}
	return NewService(Config{
		Repo:        newFakeRepo(),
		Provider:    &llm.MockProvider{},
		Registry:    reg,
		Log:         zap.NewNop(),
		Model:       "test-model",
		MaxRounds:   8,
		MaxMessages: 20,
		WorkRoot:    workRoot,
	})
}

// fsHonorsChmod 探测当前文件系统是否真正生效 chmod（Windows 挂载盘/WSL
// 挂载盘下 POSIX 权限位是 no-op，无法断言权限收紧；Linux 容器内正常）。
func fsHonorsChmod(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, ".perm_probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer os.Remove(probe)
	if err := os.Chmod(probe, 0o640); err != nil {
		return false
	}
	info, err := os.Stat(probe)
	if err != nil {
		return false
	}
	return info.Mode().Perm() == 0o640
}

// TestMigrateChatMedia 媒体迁入私有目录：文件移动 + 引用前缀改写 + 权限收紧 +
// 公共区空目录清理。
func TestMigrateChatMedia(t *testing.T) {
	workRoot := t.TempDir()
	// 模拟沙盒公共区产物：rag-media/<docID>/fig.png（0644）。
	srcDir := filepath.Join(workRoot, "rag-media", "x_123", "pdf")
	if err := os.MkdirAll(srcDir, 0o777); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	srcFile := filepath.Join(srcDir, "fig.png")
	if err := os.WriteFile(srcFile, []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc, err := newTestServiceWithWork(workRoot)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	doc := &ingest.ParsedDoc{Media: []ingest.MediaItem{
		{Path: "rag-media/x_123/pdf/fig.png"},
	}}
	oldPrefix, newPrefix, err := svc.migrateChatMedia(1, 9, doc)
	if err != nil {
		t.Fatalf("migrateChatMedia: %v", err)
	}
	if oldPrefix != "rag-media/x_123/pdf/" {
		t.Fatalf("oldPrefix = %q", oldPrefix)
	}
	if newPrefix != "users/1/chat-files/9/media/" {
		t.Fatalf("newPrefix = %q", newPrefix)
	}

	// 文件已移动到私有目录。
	dstFile := filepath.Join(workRoot, "users", "1", "chat-files", "9", "media", "fig.png")
	if _, err := os.Stat(dstFile); err != nil {
		t.Fatalf("私有目录应存在媒体文件: %v", err)
	}
	// 引用路径已改写。
	if doc.Media[0].Path != "users/1/chat-files/9/media/fig.png" {
		t.Fatalf("media path = %q", doc.Media[0].Path)
	}
	// 公共区空目录已清理。
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Fatalf("公共区目录应已清理（残留: %v）", err)
	}
	// 权限收紧：文件系统支持 chmod 时断言（挂载盘 no-op 则跳过）。
	if fsHonorsChmod(t, workRoot) {
		info, err := os.Stat(dstFile)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm&0o004 != 0 {
			t.Fatalf("媒体文件不应世界可读，当前 %v", perm)
		}
		dirInfo, _ := os.Stat(filepath.Dir(dstFile))
		if perm := dirInfo.Mode().Perm(); perm&0o007 != 0 {
			t.Fatalf("媒体目录不应其他用户可访问，当前 %v", perm)
		}
	}
}

// TestMigrateChatMedia_NoMedia 无媒体时零操作。
func TestMigrateChatMedia_NoMedia(t *testing.T) {
	svc, err := newTestServiceWithWork(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	doc := &ingest.ParsedDoc{}
	oldP, newP, err := svc.migrateChatMedia(1, 9, doc)
	if err != nil || oldP != "" || newP != "" {
		t.Fatalf("无媒体应零操作: (%q, %q, %v)", oldP, newP, err)
	}
}

// TestMigrateChatMedia_DuplicateNames 同名媒体去重，不互相覆盖。
func TestMigrateChatMedia_DuplicateNames(t *testing.T) {
	workRoot := t.TempDir()
	// 两个同名但不同子目录的源文件（解析产物可能嵌套同名图片）。
	for _, sub := range []string{"a", "b"} {
		dir := filepath.Join(workRoot, "rag-media", "x_2", sub)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pic.png"), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	svc, err := newTestServiceWithWork(workRoot)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	doc := &ingest.ParsedDoc{Media: []ingest.MediaItem{
		{Path: "rag-media/x_2/a/pic.png"},
		{Path: "rag-media/x_2/b/pic.png"},
	}}
	_, _, err = svc.migrateChatMedia(7, 3, doc)
	if err != nil {
		t.Fatalf("migrateChatMedia: %v", err)
	}
	if doc.Media[0].Path == doc.Media[1].Path {
		t.Fatalf("同名媒体应去重: %v", doc.Media)
	}
	base := filepath.Join(workRoot, "users", "7", "chat-files", "3", "media")
	entries, _ := os.ReadDir(base)
	if len(entries) != 2 {
		t.Fatalf("私有目录应有 2 个文件，实际 %d", len(entries))
	}
}

// TestMigrateChatMedia_NoWorkRoot 未配置 WorkRoot 时回退进程工作目录（容器内
// /app = 沙盒 /work 同一宿主目录），媒体仍能迁入私有目录——防止再次静默跳过。
func TestMigrateChatMedia_NoWorkRoot(t *testing.T) {
	// 切换到临时目录，os.Getwd() 即为其路径（NewService 不设 WorkRoot）。
	t.Chdir(t.TempDir())
	workRoot, _ := os.Getwd()
	srcDir := filepath.Join(workRoot, "rag-media", "x_5", "pdf")
	if err := os.MkdirAll(srcDir, 0o777); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "fig.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc, err := newTestServiceWithWork("") // 不配置 WorkRoot
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	doc := &ingest.ParsedDoc{Media: []ingest.MediaItem{
		{Path: "rag-media/x_5/pdf/fig.png"},
	}}
	oldP, newP, err := svc.migrateChatMedia(1, 9, doc)
	if err != nil {
		t.Fatalf("migrateChatMedia: %v", err)
	}
	if oldP == "" || newP == "" {
		t.Fatalf("无 WorkRoot 配置时仍应迁移: (%q, %q)", oldP, newP)
	}
	if doc.Media[0].Path != "users/1/chat-files/9/media/fig.png" {
		t.Fatalf("media path = %q", doc.Media[0].Path)
	}
	if _, err := os.Stat(filepath.Join(workRoot, "users", "1", "chat-files", "9", "media", "fig.png")); err != nil {
		t.Fatalf("私有目录应存在媒体文件: %v", err)
	}
}

// TestMigrateChatMedia_RenameFail 迁移失败时保留公共区路径，不阻断上传。
func TestMigrateChatMedia_RenameFail(t *testing.T) {
	workRoot := t.TempDir()
	svc, err := newTestServiceWithWork(workRoot)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 源文件不存在 → rename 失败。
	doc := &ingest.ParsedDoc{Media: []ingest.MediaItem{
		{Path: "rag-media/missing/doc.pdf/page.png"},
	}}
	oldP, newP, err := svc.migrateChatMedia(1, 9, doc)
	if err != nil {
		t.Fatalf("迁移失败不应返回错误（降级）: %v", err)
	}
	if oldP != "" || newP != "" {
		t.Fatalf("全部失败应返回空前缀: (%q, %q)", oldP, newP)
	}
	if doc.Media[0].Path != "rag-media/missing/doc.pdf/page.png" {
		t.Fatalf("失败媒体应保留原路径: %q", doc.Media[0].Path)
	}
}
