package llmsvc

// 用户 token 配额逻辑单测（q2 按用户/角色配额管理）。
//
// 优先级契约：user_quota 表显式覆盖 > 角色默认（管理员用
// AdminTokenQuotaMonth，普通用户用 TokenQuotaMonth）；0 = 不限。

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// fakeQuotaStore 内存配额覆盖存储（QuotaStore 测试替身）。
type fakeQuotaStore struct {
	mu    sync.Mutex
	quota int64
	has   bool
	err   error
}

func (f *fakeQuotaStore) Get(_ context.Context, _ int64) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quota, f.has, f.err
}

func (f *fakeQuotaStore) Set(context.Context, int64, int64, int64) error {
	return nil
}

func (f *fakeQuotaStore) Clear(context.Context, int64) error {
	return nil
}

func (f *fakeQuotaStore) List(context.Context) ([]UserQuota, error) {
	return nil, nil
}

// TestEffectiveQuota_Priority 验证有效配额计算优先级与角色默认。
func TestEffectiveQuota_Priority(t *testing.T) {
	cases := []struct {
		name       string
		tokenQuota int64 // 普通用户默认
		adminQuota int64 // 管理员默认
		store      QuotaStore
		role       string
		want       int64
	}{
		{"普通用户无覆盖走普通默认", 1_000_000, 0, nil, "", 1_000_000},
		{"角色头缺失按普通用户", 1_000_000, 0, nil, "", 1_000_000},
		{"super_admin 无覆盖走管理员默认", 1_000_000, 0, nil, "super_admin", 0},
		{"agent_admin 无覆盖走管理员默认", 1_000_000, 0, nil, "agent_admin", 0},
		{"admin 无覆盖走管理员默认", 1_000_000, 5_000_000, nil, "admin", 5_000_000},
		{"覆盖值优先于普通默认", 1_000_000, 0, &fakeQuotaStore{quota: 200_000, has: true}, "", 200_000},
		{"覆盖值优先于管理员默认", 1_000_000, 0, &fakeQuotaStore{quota: 200_000, has: true}, "super_admin", 200_000},
		{"覆盖 0 = 不限", 1_000_000, 0, &fakeQuotaStore{quota: 0, has: true}, "user", 0},
		{"无覆盖记录(ok=false)走角色默认", 1_000_000, 3_000_000, &fakeQuotaStore{has: false}, "admin", 3_000_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Handler{
				log:             zap.NewNop(),
				tokenQuota:      c.tokenQuota,
				adminTokenQuota: c.adminQuota,
				quotaStore:      c.store,
			}
			if got := h.effectiveQuota(context.Background(), 1, c.role); got != c.want {
				t.Errorf("effectiveQuota = %d, want %d", got, c.want)
			}
		})
	}
}

// doChatRole 发送一次 /v1/chat/completions，可携带用户角色头。
func doChatRole(t *testing.T, url, body, userID, role string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	if role != "" {
		req.Header.Set(headerUserRole, role)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestHandler_QuotaByRole 端到端验证：同一用户本月用量超额时，普通用户被
// 429 拦截，管理员（X-User-Role=super_admin，默认不限）放行。
func TestHandler_QuotaByRole(t *testing.T) {
	up := &mockUpstream{}
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","model":"deepseek-v4-flash",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json")
	specs := []ModelSpec{
		{Name: "deepseek-v4-flash", BaseURL: up.serverURL(t), APIKey: "k", Enabled: true, IsDefault: true},
	}
	usage := &fakeUsageStore{monthTotal: 10_000_000} // 本月已用 1000 万
	reg := NewRegistry()
	reg.Reload(specs, zap.NewNop())
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Registry:             reg,
		TokenQuotaMonth:      1_000_000, // 普通用户 100 万/月
		AdminTokenQuotaMonth: 0,         // 管理员不限
	})
	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`

	// 普通用户（无角色头）超额 → 429。
	resp := doChatRole(t, srv.URL, body, "42", "")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("普通用户 status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}

	// 管理员超额也放行（默认不限）。
	resp = doChatRole(t, srv.URL, body, "42", "super_admin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("管理员 status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
}

// TestHandler_QuotaOverrideWins 端到端验证：显式覆盖优先于角色默认——
// 管理员被覆盖 20 万后（本月已用 100 万）即使角色是 super_admin 也被 429。
func TestHandler_QuotaOverrideWins(t *testing.T) {
	up := &mockUpstream{}
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","model":"deepseek-v4-flash",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json")
	specs := []ModelSpec{
		{Name: "deepseek-v4-flash", BaseURL: up.serverURL(t), APIKey: "k", Enabled: true, IsDefault: true},
	}
	usage := &fakeUsageStore{monthTotal: 1_000_000} // 本月已用 100 万
	store := &fakeQuotaStore{quota: 200_000, has: true}
	reg := NewRegistry()
	reg.Reload(specs, zap.NewNop())
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Registry:             reg,
		TokenQuotaMonth:      0, // 普通用户默认不限
		AdminTokenQuotaMonth: 0, // 管理员默认不限
		QuotaStore:           store,
	})
	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`

	resp := doChatRole(t, srv.URL, body, "42", "super_admin")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("覆盖后 super_admin status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
}
