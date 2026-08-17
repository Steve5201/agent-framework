// usage_admin_test.go —— 管理端用量总览端点（数据管理模块）测试。
//
// 覆盖：令牌保护（未配置 503 / 不匹配 401）、参数校验（days 1..90）、
// 正常响应完整性、窗口透传。
package llmsvc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestUsageAdmin_Overview(t *testing.T) {
	store := &fakeUsageStore{overview: &UsageOverview{
		Summary: UsageSummary{Calls: 10, Success: 9, Failed: 1, DAU: 3, TotalTokens: 1000, CostUSD: 1.5},
		Daily:   []DayUsage{{Date: "2026-08-12", Calls: 10, Success: 9, Failed: 1, DAU: 3, TotalTokens: 1000, CostUSD: 1.5}},
		ByModel: []UsageGroup{{Key: "deepseek", Calls: 9, TotalTokens: 900, CostUSD: 1.4}},
		ByAgent: []UsageGroup{{Key: "tutor", Calls: 5, TotalTokens: 500, CostUSD: 0.8}},
		ByUser:  []UserUsage{{UserID: 1, Calls: 5, TotalTokens: 500, CostUSD: 0.8}},
	}}

	// newMux 经 RegisterAdmin 注册（requireToken 中间件生效）。
	newMux := func(token string) *http.ServeMux {
		a := NewUsageAdmin(store, token, zap.NewNop())
		mux := http.NewServeMux()
		a.RegisterAdmin(mux)
		return mux
	}

	t.Run("令牌未配置 → 503", func(t *testing.T) {
		w := httptest.NewRecorder()
		newMux("").ServeHTTP(w, httptest.NewRequest("GET", "/v1/usage/overview?days=7", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("无令牌应 503, got %d", w.Code)
		}
	})

	t.Run("令牌不匹配 → 401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/usage/overview", nil)
		req.Header.Set("X-Admin-Token", "wrong")
		w := httptest.NewRecorder()
		newMux("secret").ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("令牌错误应 401, got %d", w.Code)
		}
	})

	t.Run("参数非法 → 400", func(t *testing.T) {
		for _, days := range []string{"abc", "0", "91"} {
			req := httptest.NewRequest("GET", "/v1/usage/overview?days="+days, nil)
			req.Header.Set("X-Admin-Token", "secret")
			w := httptest.NewRecorder()
			newMux("secret").ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("days=%s 应 400, got %d", days, w.Code)
			}
		}
	})

	t.Run("正常 → 200 + 完整 JSON + 窗口透传", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/usage/overview?days=30", nil)
		req.Header.Set("X-Admin-Token", "secret")
		w := httptest.NewRecorder()
		newMux("secret").ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d: %s", w.Code, w.Body.String())
		}
		var ov UsageOverview
		if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
			t.Fatalf("响应非合法 JSON: %v", err)
		}
		if ov.Summary.Calls != 10 || ov.Summary.DAU != 3 || ov.Summary.CostUSD != 1.5 {
			t.Fatalf("摘要异常: %+v", ov.Summary)
		}
		if len(ov.Daily) != 1 || ov.Daily[0].Date != "2026-08-12" || ov.Daily[0].Failed != 1 {
			t.Fatalf("按日序列异常: %+v", ov.Daily)
		}
		if len(ov.ByModel) != 1 || ov.ByModel[0].Key != "deepseek" || len(ov.ByUser) != 1 || ov.ByUser[0].UserID != 1 {
			t.Fatalf("聚合异常: byModel=%+v byUser=%+v", ov.ByModel, ov.ByUser)
		}
		if store.overviewDays != 30 {
			t.Fatalf("窗口应透传 30, got %d", store.overviewDays)
		}
	})

	t.Run("缺省窗口 → 30", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/usage/overview", nil)
		req.Header.Set("X-Admin-Token", "secret")
		w := httptest.NewRecorder()
		newMux("secret").ServeHTTP(w, req)
		if w.Code != http.StatusOK || store.overviewDays != 30 {
			t.Fatalf("缺省窗口应 30, got code=%d days=%d", w.Code, store.overviewDays)
		}
	})
}
