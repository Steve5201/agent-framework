// gateway_test.go —— gatewaysvc 单测（P2-56）。
//
// 覆盖：JWT 鉴权中间件（白名单/缺失/无效/非法ID/类型不符）、
// bearerToken 提取、Login/CreateSession handler 透传、SSE 流式转发。
// 下游 gRPC 用最小 fake 客户端模拟（不启动真实服务）。
package gatewaysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Steve5201/agent-backend/internal/auth"
	apperr "github.com/Steve5201/agent-backend/internal/errors"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
	"github.com/Steve5201/agent-backend/internal/ratelimit"
	"go.uber.org/zap"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// 测试工具
// ---------------------------------------------------------------------------

// newTestManager 构造 JWT 管理器（测试密钥）。
func newTestManager(t *testing.T) *auth.Manager {
	t.Helper()
	mgr, err := auth.New(auth.Config{
		Secret:     "test-secret-please-change",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	return mgr
}

// signedAccess 签发指定 userID 的 access token。
func signedAccess(t *testing.T, mgr *auth.Manager, userID string) string {
	t.Helper()
	tok, _, err := mgr.SignAccess(userID, "user", "")
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	return tok
}

// minimalClients 构造只含 JWT 与日志的 Clients（鉴权中间件测试用）。
func minimalClients(mgr *auth.Manager) *Clients {
	return &Clients{JWT: mgr, Log: zap.NewNop()}
}

// ---------------------------------------------------------------------------
// 鉴权中间件
// ---------------------------------------------------------------------------

func TestRequireAuth(t *testing.T) {
	mgr := newTestManager(t)
	clients := minimalClients(mgr)

	// 业务 handler：回显 context 中的 user_id。
	echoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := userIDFrom(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"uid": uid})
	})
	handler := clients.RequireAuth("GET /healthz")(echoHandler)

	t.Run("白名单直放", func(t *testing.T) {
		// 白名单路径不经过鉴权，context 无 user_id——
		// 用不依赖 user_id 的 handler 验证放行行为。
		okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		h := clients.RequireAuth("GET /healthz")(okHandler)
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("白名单应放行, got %d", rec.Code)
		}
	})

	t.Run("缺少令牌", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("缺令牌应 401, got %d", rec.Code)
		}
	})

	t.Run("无效令牌", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		req.Header.Set("Authorization", "Bearer not-a-real-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("无效令牌应 401, got %d", rec.Code)
		}
	})

	t.Run("合法令牌", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "7"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("合法令牌应放行, got %d body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]int64
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["uid"] != 7 {
			t.Fatalf("context 中 user_id 应为 7, got %d", body["uid"])
		}
	})

	t.Run("非法user_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "abc"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("user_id 非法应 400, got %d", rec.Code)
		}
	})

	t.Run("refresh令牌冒充access", func(t *testing.T) {
		// 签发 refresh 类型令牌，用 access 场景校验应拒绝。
		refresh, _, err := mgr.SignRefresh("7", "family-1")
		if err != nil {
			t.Fatalf("SignRefresh: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+refresh)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("类型不符应 401, got %d", rec.Code)
		}
	})
}

func TestRequireAuth_Guest(t *testing.T) {
	mgr := newTestManager(t)
	clients := minimalClients(mgr)

	echoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := userIDFrom(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"uid": uid, "role": roleFrom(r)})
	})
	handler := clients.RequireAuth("GET /healthz")(echoHandler)

	t.Run("合法游客身份", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		req.Header.Set("X-Guest-ID", "550e8400-e29b-41d4-a716-446655440000")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("合法游客应放行, got %d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			UID  int64  `json:"uid"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.UID >= 0 {
			t.Fatalf("游客 user_id 应为负, got %d", body.UID)
		}
		if body.Role != "" {
			t.Fatalf("游客角色应为空, got %q", body.Role)
		}
	})

	t.Run("非法游客头", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		req.Header.Set("X-Guest-ID", "bad id with spaces")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("非法游客头应 401, got %d", rec.Code)
		}
	})

	t.Run("同游客ID身份稳定", func(t *testing.T) {
		first := guestUidOf(t, handler)
		second := guestUidOf(t, handler)
		if first != second || first >= 0 {
			t.Fatalf("同游客 ID 应派生同一负 uid: %d vs %d", first, second)
		}
	})

	t.Run("令牌优先于游客头", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "7"))
		req.Header.Set("X-Guest-ID", "550e8400-e29b-41d4-a716-446655440000")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("应放行, got %d", rec.Code)
		}
		var body struct {
			UID int64 `json:"uid"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.UID != 7 {
			t.Fatalf("令牌身份应优先, uid=%d want 7", body.UID)
		}
	})
}

// TestSessionConfigBody 回归：会话配置序列化必须回传全部分段
// （enabled_resources/kb_ids/mcp_servers 曾漏字段，导致前端重开配置弹窗
// 勾选状态丢失——P2-AH 修复）。
func TestSessionConfigBody(t *testing.T) {
	t.Run("全字段回传", func(t *testing.T) {
		body := sessionConfigBody(&agentv1.SessionConfig{
			EnabledResources: []string{"search", "skill_x"},
			EnabledTools:     []string{"web_search"},
			Thinking:         &agentv1.ThinkingConfig{Enabled: true, ReasoningEffort: "high"},
			KbIds:            []string{"kb_1"},
			McpServers:       []string{"fs", "github"},
		})
		if !reflect.DeepEqual(body["enabled_resources"], []string{"search", "skill_x"}) {
			t.Fatalf("enabled_resources = %v", body["enabled_resources"])
		}
		if !reflect.DeepEqual(body["enabled_tools"], []string{"web_search"}) {
			t.Fatalf("enabled_tools = %v", body["enabled_tools"])
		}
		if !reflect.DeepEqual(body["kb_ids"], []string{"kb_1"}) {
			t.Fatalf("kb_ids = %v", body["kb_ids"])
		}
		if !reflect.DeepEqual(body["mcp_servers"], []string{"fs", "github"}) {
			t.Fatalf("mcp_servers = %v", body["mcp_servers"])
		}
		th, ok := body["thinking"].(map[string]any)
		if !ok || th["enabled"] != true || th["reasoning_effort"] != "high" {
			t.Fatalf("thinking = %v", body["thinking"])
		}
	})

	t.Run("nil 与空配置返回空对象", func(t *testing.T) {
		if len(sessionConfigBody(nil)) != 0 {
			t.Fatalf("nil 应返回空, got %v", sessionConfigBody(nil))
		}
		if len(sessionConfigBody(&agentv1.SessionConfig{})) != 0 {
			t.Fatalf("空配置应返回空, got %v", sessionConfigBody(&agentv1.SessionConfig{}))
		}
	})

	t.Run("思考 effort 为空时不输出", func(t *testing.T) {
		body := sessionConfigBody(&agentv1.SessionConfig{
			Thinking: &agentv1.ThinkingConfig{Enabled: false},
		})
		th := body["thinking"].(map[string]any)
		if _, has := th["reasoning_effort"]; has {
			t.Fatalf("reasoning_effort 不应输出, got %v", th)
		}
	})
}

// guestUidOf 用固定合法游客 ID 发一次游客请求，返回 context 中的 user_id。
func guestUidOf(t *testing.T, handler http.Handler) int64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
	req.Header.Set("X-Guest-ID", "550e8400-e29b-41d4-a716-446655440000")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("游客请求应放行, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		UID int64 `json:"uid"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.UID
}

// TestRequireAdmin_GuestBlocked 游客身份访问管理端路由应被角色校验拦截。
func TestRequireAdmin_GuestBlocked(t *testing.T) {
	mgr := newTestManager(t)
	clients := minimalClients(mgr)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := clients.RequireAuth("GET /healthz")(clients.RequireAdmin(ok))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)
	req.Header.Set("X-Guest-ID", "550e8400-e29b-41d4-a716-446655440000")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("游客访问管理端应 403, got %d", rec.Code)
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc123", "abc123"},
		{"bearer abc123", "abc123"}, // 大小写不敏感
		{"Basic abc123", ""},
		{"", ""},
		{"Bearer", ""},
	}
	for _, c := range cases {
		if got := bearerToken(c.header); got != c.want {
			t.Errorf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

// TestSkipRoute 白名单匹配：精确 / 目录前缀 / {agent_id} 路径通配。
// 回归保护：/v1/auth/register/{agent_id} 与 /v1/auth/login/{agent_id} 是匿名
// 入口，若白名单不认"带真实 agent_id 的路径"，匿名请求会被 RequireAuth 拦截，
// 表现为"缺少访问令牌"（tutor 登录/注册失败根因）。
func TestSkipRoute(t *testing.T) {
	whitelist := []string{
		"POST /v1/auth/register/{agent_id}",
		"POST /v1/auth/login",
		"POST /v1/auth/login/{agent_id}",
		"GET /swagger/ui",
		"GET /",
	}

	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"精确匹配-login", http.MethodPost, "/v1/auth/login", true},
		{"通配-注册tutor", http.MethodPost, "/v1/auth/register/tutor", true},
		{"通配-注册多段ID", http.MethodPost, "/v1/auth/register/demo-tutor", true},
		{"通配-登录智能体门户", http.MethodPost, "/v1/auth/login/tutor", true},
		{"精确-swagger-ui", http.MethodGet, "/swagger/ui", true},
		{"精确-根路径", http.MethodGet, "/", true},
		{"业务路由不被放行", http.MethodGet, "/v1/agent/sessions", false},
		{"通配段不跨级匹配", http.MethodPost, "/v1/auth/register/tutor/extra", false},
		{"方法不一致不放行", http.MethodGet, "/v1/auth/login", false},
		{"路径不匹配", http.MethodPost, "/v1/auth/refresh", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			if got := skipRoute(req, whitelist); got != c.want {
				t.Fatalf("skipRoute(%s %s) = %v, want %v", c.method, c.path, got, c.want)
			}
		})
	}
}

// TestPatternPathMatch 通配分段比较的边界情况。
func TestPatternPathMatch(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/v1/auth/register/{agent_id}", "/v1/auth/register/tutor", true},
		{"/v1/auth/register/{agent_id}", "/v1/auth/register/x-y", true},
		{"/v1/auth/register/{agent_id}", "/v1/auth/register/", false}, // 通配段不能为空
		{"/v1/auth/register/{agent_id}", "/v1/auth/register", false},  // 段数不一致
		{"/v1/auth/register/{agent_id}", "/v1/auth/login/tutor", false},
		{"/a/{x}/c", "/a/b/c", true},
		{"/a/{x}/c", "/a/b/d", false},
		{"/a/{x}/c", "/a/b/c/d", false}, // 段数不一致
		{"/exact/path", "/exact/path", true},
	}
	for _, c := range cases {
		if got := patternPathMatch(c.pattern, c.path); got != c.want {
			t.Errorf("patternPathMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 下游 fake 客户端
// ---------------------------------------------------------------------------

// fakeAuthClient 最小 auth 客户端：只实现测试需要的 Login / Register。
type fakeAuthClient struct {
	authpb.AuthServiceClient
	loginErr    error
	registerErr error
}

func (f *fakeAuthClient) Register(_ context.Context, _ *authpb.RegisterRequest, _ ...grpc.CallOption) (*authpb.RegisterResponse, error) {
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return &authpb.RegisterResponse{UserId: "1", Username: "alice"}, nil
}

func (f *fakeAuthClient) Login(_ context.Context, _ *authpb.LoginRequest, _ ...grpc.CallOption) (*authpb.LoginResponse, error) {
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	return &authpb.LoginResponse{
		AccessToken:  "fake-access",
		RefreshToken: "fake-refresh",
		ExpiresIn:    900,
		User:         &authpb.User{Id: "1", Username: "alice", Role: "user"},
	}, nil
}

// fakeAgentClient 最小 agent 客户端：实现 CreateSession。
type fakeAgentClient struct {
	agentv1.AgentServiceClient
	createErr    error
	deleteMsgErr error
	msgs         []*agentv1.Message // 非空时 ListMessages 返回该列表
	mergedN      int                // MergeGuestSessions 返回的迁移数
	mergeErr     error
	uploadResp   *agentv1.UploadChatDocumentResponse // UploadChatDocument 返回值
	uploadErr    error                               // UploadChatDocument 错误注入
	uploadReq    *agentv1.UploadChatDocumentRequest  // 最近一次上传请求（断言用）
}

func (f *fakeAgentClient) UploadChatDocument(_ context.Context, req *agentv1.UploadChatDocumentRequest, _ ...grpc.CallOption) (*agentv1.UploadChatDocumentResponse, error) {
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	f.uploadReq = req
	return f.uploadResp, nil
}

func (f *fakeAgentClient) MergeGuestSessions(_ context.Context, _ *agentv1.MergeGuestSessionsRequest, _ ...grpc.CallOption) (*agentv1.MergeGuestSessionsResponse, error) {
	if f.mergeErr != nil {
		return nil, f.mergeErr
	}
	return &agentv1.MergeGuestSessionsResponse{Migrated: int32(f.mergedN)}, nil
}

func (f *fakeAgentClient) CreateSession(_ context.Context, _ *agentv1.CreateSessionRequest, _ ...grpc.CallOption) (*agentv1.CreateSessionResponse, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &agentv1.CreateSessionResponse{Session: &agentv1.Session{Id: "1", UserId: "1", Title: "新对话"}}, nil
}

func (f *fakeAgentClient) DeleteMessage(_ context.Context, req *agentv1.DeleteMessageRequest, _ ...grpc.CallOption) (*agentv1.DeleteMessageResponse, error) {
	if f.deleteMsgErr != nil {
		return nil, f.deleteMsgErr
	}
	if req.SessionId != "1" || req.MessageId != "42" {
		return nil, status.Error(codes.NotFound, "消息不存在")
	}
	return &agentv1.DeleteMessageResponse{}, nil
}

func (f *fakeAgentClient) ListMessages(_ context.Context, _ *agentv1.ListMessagesRequest, _ ...grpc.CallOption) (*agentv1.ListMessagesResponse, error) {
	return &agentv1.ListMessagesResponse{Messages: f.msgs}, nil
}

func TestLoginHandler(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Auth: &fakeAuthClient{},
		JWT:  mgr,
		Log:  zap.NewNop(),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"alice","password":"secret123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	clients.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("登录应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["access_token"] != "fake-access" {
		t.Fatalf("应透传 access_token, got %v", body["access_token"])
	}
}

func TestLoginHandler_ErrorPropagated(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Auth: &fakeAuthClient{loginErr: appErrUnauthenticated()},
		JWT:  mgr,
		Log:  zap.NewNop(),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"a","password":"b"}`))
	rec := httptest.NewRecorder()
	clients.Login(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("登录失败应 401, got %d", rec.Code)
	}
}

// TestRegisterHandler_GRPCErrorMapped 回归测试：下游 auth 返回带业务码的
// gRPC status 错误（真实链路形态：客户端只能收到 status 错误）时，
// writeError 必须经 FromGRPCError 恢复错误码，否则 InvalidArgument 会被
// HTTPBody 兜底成 50001 "internal error"，掩盖真实原因。
func TestRegisterHandler_GRPCErrorMapped(t *testing.T) {
	st := status.New(codes.InvalidArgument, "密码须不少于 8 位，且同时包含字母与数字")
	st, err := st.WithDetails(&errdetails.ErrorInfo{Reason: string(apperr.CodeInvalidArgument)})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	clients := &Clients{
		Auth: &fakeAuthClient{registerErr: st.Err()},
		JWT:  newTestManager(t),
		Log:  zap.NewNop(),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register", strings.NewReader(`{"username":"a","password":"b"}`))
	rec := httptest.NewRecorder()
	clients.Register(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("InvalidArgument 应 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Code != apperr.BizInvalidArgument {
		t.Fatalf("应返回 40001, got %d", body.Code)
	}
	if !strings.Contains(rec.Body.String(), "密码须不少于 8 位") {
		t.Fatalf("应透传真实错误提示, body=%s", rec.Body.String())
	}
}

// TestRootRedirect 根路径直接访问应 302 引导到接口文档（而非 40101 裸 JSON）。
func TestRootRedirect(t *testing.T) {
	clients := minimalClients(newTestManager(t))
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("根路径应 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/swagger/ui" {
		t.Fatalf("应重定向到 /swagger/ui, got %q", loc)
	}
}

// TestDeleteMessageHandler 删除单条消息（DELETE .../messages/{mid}）。
// 验证路由参数解析、下游调用与 204 响应。
func TestDeleteMessageHandler(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Agent: &fakeAgentClient{},
		JWT:   mgr,
		Log:   zap.NewNop(),
	}
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	req := httptest.NewRequest(http.MethodDelete, "/v1/agent/sessions/1/messages/42", nil)
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("删除消息应 204, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 聊天上传文档（模块二）
// ---------------------------------------------------------------------------

// multipartFile 构造 multipart/form-data 请求体（字段 file）。
func multipartFile(t *testing.T, fileName, content string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("写入文件内容: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close multipart: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// TestUploadChatDocumentHandler 上传成功：multipart 解出文件 → 透传下游 → 响应回包。
func TestUploadChatDocumentHandler(t *testing.T) {
	mgr := newTestManager(t)
	fake := &fakeAgentClient{
		uploadResp: &agentv1.UploadChatDocumentResponse{
			FileName:    "intro.md",
			RelPath:     "users/1/chat-files/9/intro.md",
			Segments:    2,
			InjectedLen: 120,
		},
	}
	clients := &Clients{Agent: fake, JWT: mgr, Log: zap.NewNop()}
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	body, ct := multipartFile(t, "intro.md", "# 简介\n内容")
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions/9/documents", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("应 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	// 下游请求断言：会话 ID、文件名、文件内容。
	if fake.uploadReq == nil {
		t.Fatal("应调用下游 UploadChatDocument")
	}
	if fake.uploadReq.SessionId != "9" || fake.uploadReq.FileName != "intro.md" {
		t.Fatalf("下游请求不符: %+v", fake.uploadReq)
	}
	if string(fake.uploadReq.Content) != "# 简介\n内容" {
		t.Fatalf("文件内容不符: %q", string(fake.uploadReq.Content))
	}
	// 响应字段透传。
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["rel_path"] != "users/1/chat-files/9/intro.md" || resp["segments"] != float64(2) {
		t.Fatalf("响应透传不符: %s", rec.Body.String())
	}
}

// TestUploadChatDocumentHandler_MissingFile 非 multipart 请求 → 400。
func TestUploadChatDocumentHandler_MissingFile(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{Agent: &fakeAgentClient{}, JWT: mgr, Log: zap.NewNop()}
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions/9/documents", strings.NewReader("not multipart"))
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少文件字段应 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestUploadChatDocumentHandler_DownstreamError 下游 NOT_FOUND → 404 透传。
func TestUploadChatDocumentHandler_DownstreamError(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Agent: &fakeAgentClient{uploadErr: status.Error(codes.NotFound, "会话不存在")},
		JWT:   mgr,
		Log:   zap.NewNop(),
	}
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	body, ct := multipartFile(t, "a.md", "body")
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions/9/documents", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("下游 NOT_FOUND 应 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateSessionHandler(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Agent: &fakeAgentClient{},
		JWT:   mgr,
		Log:   zap.NewNop(),
	}
	handler := clients.RequireAuth()(http.HandlerFunc(clients.CreateSession))

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions", strings.NewReader(`{"title":"考研"}`))
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("创建会话应 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMergeGuestSessionsHandler(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Agent: &fakeAgentClient{mergedN: 2},
		JWT:   mgr,
		Log:   zap.NewNop(),
	}
	handler := clients.RequireAuth()(http.HandlerFunc(clients.MergeGuestSessions))

	// 1) 正常合并：透传 guest_id，返回迁移数。
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions/merge-guest",
		strings.NewReader(`{"guest_id":"550e8400-e29b-41d4-a716-446655440000"}`))
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("合并应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Migrated int `json:"migrated"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Migrated != 2 {
		t.Fatalf("migrated 应 2, got %d", body.Migrated)
	}

	// 2) 缺 guest_id → 400。
	req2 := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions/merge-guest", strings.NewReader(`{}`))
	req2.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("缺 guest_id 应 400, got %d", rec2.Code)
	}
}

func TestListSessionMessages(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Agent: &fakeAgentClient{msgs: []*agentv1.Message{
			{Id: "11", Role: "user", Content: "你好", RoundNo: 1, Version: 0, TotalVersions: 1},
			{Id: "12", Role: "assistant", Content: "你好！", ToolCalls: `[{"id":"c1","name":"echo","arguments":"{}"}]`, RoundNo: 1, Version: 0, TotalVersions: 2},
			{Id: "13", Role: "tool", Content: `{"echo":"你好"}`, ToolCallId: "c1", RoundNo: 1, Version: 0, TotalVersions: 1},
		}},
		JWT: mgr,
		Log: zap.NewNop(),
	}
	// 走完整路由：让 ServeMux 解析路径参数 {id}。
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	req := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions/1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("历史消息应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("应返回 3 条消息, got %d", len(body.Messages))
	}
	if body.Messages[0]["role"] != "user" || body.Messages[0]["content"] != "你好" {
		t.Errorf("首条消息透传错误: %v", body.Messages[0])
	}
	if body.Messages[1]["tool_calls"] == "" || body.Messages[1]["tool_calls"] == nil {
		t.Errorf("assistant 消息应透传 tool_calls: %v", body.Messages[1])
	}
	if body.Messages[2]["tool_call_id"] != "c1" {
		t.Errorf("tool 消息应透传 tool_call_id: %v", body.Messages[2])
	}
	if body.Messages[0]["id"] != "11" || body.Messages[1]["id"] != "12" || body.Messages[2]["id"] != "13" {
		t.Errorf("应透传数据库主键 id: %v", body.Messages)
	}
	if body.Messages[0]["round_no"] != float64(1) || body.Messages[1]["total_versions"] != float64(2) || body.Messages[2]["version"] != float64(0) {
		t.Errorf("应透传 round_no/version/total_versions: %v", body.Messages)
	}
}

// ---------------------------------------------------------------------------
// SSE 转发
// ---------------------------------------------------------------------------

// fakeAgentStreamClient 提供固定的 StreamChat 流。
type fakeAgentStreamClient struct {
	agentv1.AgentServiceClient
	events []*agentv1.StreamChatEvent
}

func (f *fakeAgentStreamClient) StreamChat(_ context.Context, _ *agentv1.StreamChatRequest, _ ...grpc.CallOption) (agentv1.AgentService_StreamChatClient, error) {
	return &fakeEventStream{events: f.events}, nil
}

func (f *fakeAgentStreamClient) StreamRegenerate(_ context.Context, _ *agentv1.RegenerateRequest, _ ...grpc.CallOption) (agentv1.AgentService_StreamRegenerateClient, error) {
	return &fakeEventStream{events: f.events}, nil
}

// fakeEventStream 实现 Recv 返回预置事件序列。
type fakeEventStream struct {
	grpc.ClientStream
	events []*agentv1.StreamChatEvent
	idx    int
}

func (f *fakeEventStream) Recv() (*agentv1.StreamChatEvent, error) {
	if f.idx >= len(f.events) {
		return nil, io.EOF
	}
	ev := f.events[f.idx]
	f.idx++
	return ev, nil
}

func TestStreamChat_SSEForward(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Agent: &fakeAgentStreamClient{events: []*agentv1.StreamChatEvent{
			{Event: &agentv1.StreamChatEvent_Delta{Delta: &agentv1.Delta{Content: "你"}}},
			{Event: &agentv1.StreamChatEvent_Delta{Delta: &agentv1.Delta{Content: "好"}}},
			{Event: &agentv1.StreamChatEvent_Done{Done: &agentv1.Done{
				Rounds: 1, ToolCalls: 0, TotalTokens: 5,
			}}},
		}},
		JWT: mgr,
		Log: zap.NewNop(),
	}
	// 走完整路由：让 ServeMux 解析路径参数 {id}。
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions/1/chat/stream", strings.NewReader(`{"content":"嗨"}`))
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE Content-Type 错误: %s", rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{`"type":"delta"`, `"content":"你"`, `"content":"好"`, "event: done", `"rounds":1`} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE 输出缺少 %q\n%s", want, body)
		}
	}
}

// TestStreamRegenerate_SSEForward 流式重新生成路由：正文增量 + 编排进度 +
// 结束统计逐事件透传（与 StreamChat 同事件映射）。
func TestStreamRegenerate_SSEForward(t *testing.T) {
	mgr := newTestManager(t)
	clients := &Clients{
		Agent: &fakeAgentStreamClient{events: []*agentv1.StreamChatEvent{
			{Event: &agentv1.StreamChatEvent_Delta{Delta: &agentv1.Delta{Content: "新版"}}},
			{Event: &agentv1.StreamChatEvent_TaskStatus{TaskStatus: &agentv1.TaskStatus{
				Type: "task_finished", TaskId: "research", Status: "completed", TotalTokens: 10,
			}}},
			{Event: &agentv1.StreamChatEvent_Done{Done: &agentv1.Done{
				Rounds: 1, ToolCalls: 0, TotalTokens: 20,
			}}},
		}},
		JWT: mgr,
		Log: zap.NewNop(),
	}
	limit := ratelimit.Config{Rate: 1e6, Burst: 1e6}
	handler := clients.Routes(nil, limit, limit)

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/sessions/1/messages/2/regenerate-stream", nil)
	req.Header.Set("Authorization", "Bearer "+signedAccess(t, mgr, "1"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE Content-Type 错误: %s", rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`"type":"delta"`, `"content":"新版"`,
		`"type":"task_status"`, `"task_id":"research"`, `"status":"completed"`,
		"event: done", `"total_tokens":20`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE 输出缺少 %q\n%s", want, body)
		}
	}
}

// ---------------------------------------------------------------------------
// /files 媒体代理（files_proxy.go）
// ---------------------------------------------------------------------------

// TestFilesProxy /files/… 反向代理：匿名可访问（<img> 不带 token）、路径原样
// 透传、Content-Type 透传；未配置 AgentHTTPAddr 时不挂代理（走业务链鉴权）。
func TestFilesProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/users/1/chat-files/9/photo.png" {
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("fake-image-bytes"))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	clients := minimalClients(newTestManager(t))
	clients.AgentHTTPAddr = upstream.URL

	limit := ratelimit.Config{Rate: 100, Burst: 100}
	handler := clients.Routes(nil, limit, limit)

	// 匿名（无 Authorization）即可加载媒体，与 agent 端 /files 无鉴权语义一致。
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/files/users/1/chat-files/9/photo.png", nil)
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /files/… = %d, want 200（匿名媒体加载）", rr.Code)
	}
	if body := rr.Body.String(); body != "fake-image-bytes" {
		t.Fatalf("body = %q, want fake-image-bytes（透传上游）", body)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png（透传上游）", ct)
	}

	// 代理路径原样转发：上游 404 透传。
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/files/users/1/nope.png", nil)
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET 不存在媒体 = %d, want 404（透传上游）", rr.Code)
	}

	// 未配置 AgentHTTPAddr：不挂代理，/files 走业务链（鉴权拦截 → 401）。
	plain := minimalClients(newTestManager(t))
	plainHandler := plain.Routes(nil, limit, limit)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/files/users/1/chat-files/9/photo.png", nil)
	plainHandler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("未配置代理时 /files = %d, want 401（业务链鉴权拦截）", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// appErrUnauthenticated 构造统一未认证错误（供 fake 客户端返回）。
func appErrUnauthenticated() error {
	return apperr.New(apperr.CodeUnauthenticated, "用户名或密码错误")
}
