package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveInRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("相对路径-正常解析", func(t *testing.T) {
		full, err := ResolveInRoot(root, "a.txt")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if full != filepath.Join(root, "a.txt") {
			t.Fatalf("full = %q", full)
		}
	})

	t.Run("子目录相对路径", func(t *testing.T) {
		full, err := ResolveInRoot(root, "sub/../a.txt")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if full != filepath.Join(root, "a.txt") {
			t.Fatalf("full = %q", full)
		}
	})

	t.Run("根内绝对路径-允许", func(t *testing.T) {
		full, err := ResolveInRoot(root, filepath.Join(root, "a.txt"))
		if err != nil {
			t.Fatalf("根内绝对路径应允许: %v", err)
		}
		if full != filepath.Join(root, "a.txt") {
			t.Fatalf("full = %q", full)
		}
	})

	t.Run("越界-相对路径拒绝", func(t *testing.T) {
		for _, p := range []string{"../x", "sub/../../x", ".."} {
			if _, err := ResolveInRoot(root, p); err == nil {
				t.Errorf("路径 %q 应被拒绝", p)
			}
		}
	})

	t.Run("越界-绝对路径拒绝", func(t *testing.T) {
		parent := filepath.Join(root, "..")
		if _, err := ResolveInRoot(root, filepath.Join(parent, "x")); err == nil {
			t.Error("根外绝对路径应被拒绝")
		}
	})

	t.Run("空路径拒绝", func(t *testing.T) {
		for _, p := range []string{"", "   "} {
			if _, err := ResolveInRoot(root, p); err == nil {
				t.Errorf("空路径 %q 应被拒绝", p)
			}
		}
	})

	t.Run("软链逃逸-拒绝", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows 无 os.Symlink 权限，跳过")
		}
		outside := filepath.Join(filepath.Dir(root), "outside-secret")
		if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("创建软链失败: %v", err)
		}
		if _, err := ResolveInRoot(root, "link.txt"); err == nil {
			t.Error("指向根外的软链应被拒绝")
		}
	})
}
