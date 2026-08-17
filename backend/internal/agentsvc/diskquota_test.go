// diskquota_test.go —— 保护区磁盘配额（模块三）单测：执行器 + 目录统计 + 管理端点。
package agentsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// fakeDiskQuotaStore 内存实现 DiskQuotaStore（handler/执行器单测用）。
type fakeDiskQuotaStore struct {
	quotas map[int64]int64 // userID → quotaMB（0=不限）
	err    error           // 模拟查询失败（降级路径）
}

func (f *fakeDiskQuotaStore) Get(_ context.Context, userID int64) (int64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	q, ok := f.quotas[userID]
	return q, ok, nil
}
func (f *fakeDiskQuotaStore) Set(_ context.Context, userID, quotaMB, _ int64) error {
	if f.err != nil {
		return f.err
	}
	f.quotas[userID] = quotaMB
	return nil
}
func (f *fakeDiskQuotaStore) Clear(_ context.Context, userID int64) error {
	if f.err != nil {
		return f.err
	}
	delete(f.quotas, userID)
	return nil
}
func (f *fakeDiskQuotaStore) List(_ context.Context) ([]DiskQuota, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]DiskQuota, 0, len(f.quotas))
	for uid, q := range f.quotas {
		out = append(out, DiskQuota{UserID: uid, DiskQuotaMB: q})
	}
	return out, nil
}

// writeProtected 造一个含指定字节内容的保护区文件。
func writeProtected(t *testing.T, protectedDir, name string, size int) {
	t.Helper()
	if err := os.MkdirAll(protectedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protectedDir, name), bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiskQuotaEnforcer(t *testing.T) {
	log := zap.NewNop()

	t.Run("不限放行", func(t *testing.T) {
		e := NewDiskQuotaEnforcer(nil, RoleDiskQuotaDefaults, log)
		if err := e.Check(context.Background(), 1, t.TempDir(), 1<<30, "super_admin"); err != nil {
			t.Fatalf("super_admin 不限配额被拒: %v", err)
		}
	})

	t.Run("角色默认配额拦截", func(t *testing.T) {
		e := NewDiskQuotaEnforcer(nil, RoleDiskQuotaDefaults, log)
		dir := filepath.Join(t.TempDir(), "protected")
		writeProtected(t, dir, "a.txt", 300<<20) // 已用 300MB > user 默认 256MB
		if err := e.Check(context.Background(), 1, dir, 1, "user"); err == nil {
			t.Fatal("普通用户超配额应被拒绝")
		} else if !strings.Contains(err.Error(), "磁盘配额已满") {
			t.Fatalf("错误信息未说明配额已满: %v", err)
		}
	})

	t.Run("显式覆盖优先于角色默认", func(t *testing.T) {
		store := &fakeDiskQuotaStore{quotas: map[int64]int64{1: 10}}
		e := NewDiskQuotaEnforcer(store, RoleDiskQuotaDefaults, log)
		dir := filepath.Join(t.TempDir(), "protected")
		writeProtected(t, dir, "a.txt", 5<<20) // 已用 5MB
		// 超角色默认（256MB）但未超显式覆盖（10MB）→ 放行。
		if err := e.Check(context.Background(), 1, dir, 1<<20, "user"); err != nil {
			t.Fatalf("显式覆盖 10MB 内写入应放行: %v", err)
		}
		// 超显式覆盖 → 拒绝。
		if err := e.Check(context.Background(), 1, dir, 6<<20, "user"); err == nil {
			t.Fatal("超显式覆盖配额应被拒绝")
		}
	})

	t.Run("store 查询失败降级角色默认", func(t *testing.T) {
		store := &fakeDiskQuotaStore{quotas: map[int64]int64{1: 1}, err: os.ErrPermission}
		e := NewDiskQuotaEnforcer(store, RoleDiskQuotaDefaults, log)
		dir := filepath.Join(t.TempDir(), "protected")
		// 降级后按 user 256MB 判定，小写入放行。
		if err := e.Check(context.Background(), 1, dir, 1024, "user"); err != nil {
			t.Fatalf("查询失败应降级角色默认而非拒绝: %v", err)
		}
	})

	t.Run("目录不存在视为零占用", func(t *testing.T) {
		e := NewDiskQuotaEnforcer(nil, RoleDiskQuota{User: 1}, log)
		dir := filepath.Join(t.TempDir(), "protected") // 未创建
		if err := e.Check(context.Background(), 1, dir, 512<<10, "user"); err != nil {
			t.Fatalf("目录不存在时应视为空: %v", err)
		}
		if err := e.Check(context.Background(), 1, dir, 2<<20, "user"); err == nil {
			t.Fatal("超出 1MB 配额应被拒绝")
		}
	})

	t.Run("默认值合理", func(t *testing.T) {
		if RoleDiskQuotaDefaults.User != 256 || RoleDiskQuotaDefaults.Admin != 512 ||
			RoleDiskQuotaDefaults.AgentAdmin != 1024 || RoleDiskQuotaDefaults.SuperAdmin != 0 {
			t.Fatalf("默认配额不符合预期: %+v", RoleDiskQuotaDefaults)
		}
	})
}

func TestDirSizeBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), bytes.Repeat([]byte("x"), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), bytes.Repeat([]byte("y"), 2000), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := dirSizeBytes(dir)
	if err != nil {
		t.Fatalf("dirSizeBytes: %v", err)
	}
	if got != 3000 {
		t.Fatalf("dirSizeBytes = %d, want 3000", got)
	}
	if n, err := dirSizeBytes(filepath.Join(dir, "not-exist")); err != nil || n != 0 {
		t.Fatalf("不存在目录应为 0, got %d err %v", n, err)
	}
}

func TestDiskQuotaAdmin(t *testing.T) {
	store := &fakeDiskQuotaStore{quotas: map[int64]int64{}}
	admin := NewDiskQuotaAdmin(store, "secret-token", zap.NewNop())
	mux := http.NewServeMux()
	admin.RegisterAdmin(mux)

	do := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("X-Admin-Token", "secret-token")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	t.Run("无令牌拒绝", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/disk-quota", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("无令牌 status = %d, want 401", w.Code)
		}
	})

	t.Run("PUT 设置", func(t *testing.T) {
		w := do(http.MethodPut, "/v1/admin/disk-quota/7", []byte(`{"disk_quota_mb": 512}`))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if store.quotas[7] != 512 {
			t.Fatalf("store.quotas[7] = %d, want 512", store.quotas[7])
		}
	})

	t.Run("PUT 负数拒绝", func(t *testing.T) {
		w := do(http.MethodPut, "/v1/admin/disk-quota/7", []byte(`{"disk_quota_mb": -1}`))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("负数 status = %d, want 400", w.Code)
		}
	})

	t.Run("PUT 非法 user_id 拒绝", func(t *testing.T) {
		w := do(http.MethodPut, "/v1/admin/disk-quota/abc", []byte(`{"disk_quota_mb": 10}`))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法 user_id status = %d, want 400", w.Code)
		}
	})

	t.Run("GET 列表", func(t *testing.T) {
		w := do(http.MethodGet, "/v1/admin/disk-quota", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", w.Code)
		}
		var out struct {
			Quotas []DiskQuota `json:"quotas"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Quotas) != 1 || out.Quotas[0].UserID != 7 || out.Quotas[0].DiskQuotaMB != 512 {
			t.Fatalf("列表内容不符: %+v", out.Quotas)
		}
	})

	t.Run("DELETE 恢复角色默认", func(t *testing.T) {
		w := do(http.MethodDelete, "/v1/admin/disk-quota/7", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("DELETE status = %d, want 200", w.Code)
		}
		if _, ok := store.quotas[7]; ok {
			t.Fatal("DELETE 后记录应删除")
		}
	})
}

func TestDiskQuotaAdmin_TokenDisabled(t *testing.T) {
	admin := NewDiskQuotaAdmin(&fakeDiskQuotaStore{quotas: map[int64]int64{}}, "", zap.NewNop())
	mux := http.NewServeMux()
	admin.RegisterAdmin(mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/disk-quota", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("令牌未配置 status = %d, want 503", w.Code)
	}
}
