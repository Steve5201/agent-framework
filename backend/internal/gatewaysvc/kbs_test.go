// kbs_test.go —— 普通用户知识库列表接口（P3-A6）单测。
//
// 覆盖：资源域解析（userAgentScope 的 super_admin/普通用户/非法 ID 分支）、
// ListKBs handler 透传与域锁定、rag 未接入降级 503。
package gatewaysvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-backend/internal/identity"
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// fakeRagClient 只实现 ListKnowledgeBases；其余 RPC 一律 Unimplemented
// （本测试仅验证列表转发与域解析）。
type fakeRagClient struct {
	listResp *ragv1.ListKBResponse
	listErr  error
	gotAgent string // 记录最后一次请求携带的 AgentId
}

func (f *fakeRagClient) ListKnowledgeBases(_ context.Context, in *ragv1.ListKBRequest, _ ...grpc.CallOption) (*ragv1.ListKBResponse, error) {
	f.gotAgent = in.GetAgentId()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func (f *fakeRagClient) Search(context.Context, *ragv1.SearchRequest, ...grpc.CallOption) (*ragv1.SearchResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) CreateKnowledgeBase(context.Context, *ragv1.CreateKBRequest, ...grpc.CallOption) (*ragv1.KnowledgeBase, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) DeleteKnowledgeBase(context.Context, *ragv1.DeleteKBRequest, ...grpc.CallOption) (*ragv1.DeleteKBResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) UpsertDocument(context.Context, *ragv1.UpsertDocumentRequest, ...grpc.CallOption) (*ragv1.UpsertDocumentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) ListDocuments(context.Context, *ragv1.ListDocumentsRequest, ...grpc.CallOption) (*ragv1.ListDocumentsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) DeleteDocument(context.Context, *ragv1.DeleteDocumentRequest, ...grpc.CallOption) (*ragv1.DeleteDocumentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) GetDocumentStatus(context.Context, *ragv1.GetDocumentStatusRequest, ...grpc.CallOption) (*ragv1.DocumentStatus, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) UpdateKnowledgeBase(context.Context, *ragv1.UpdateKBRequest, ...grpc.CallOption) (*ragv1.KnowledgeBase, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
func (f *fakeRagClient) RetryDocument(context.Context, *ragv1.RetryDocumentRequest, ...grpc.CallOption) (*ragv1.DocumentStatus, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}

// newKBClients 构造带 fake rag/ auth 的 Clients（经 RequireAuth 模拟真实链路）。
func newKBClients(mgr *auth.Manager, rag ragv1.RagServiceClient) *Clients {
	return &Clients{JWT: mgr, Log: zap.NewNop(), Rag: rag, Auth: &fakeAuthClient{}}
}

// kbRequest 走 RequireAuth 中间件发起 GET /v1/agent/kbs。
func kbRequest(t *testing.T, c *Clients, token, query string) *httptest.ResponseRecorder {
	t.Helper()
	handler := c.RequireAuth()(http.HandlerFunc(c.ListKBs))
	req := httptest.NewRequest(http.MethodGet, "/v1/agent/kbs"+query, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestUserAgentScope(t *testing.T) {
	mgr := newTestManager(t)
	c := newKBClients(mgr, nil)
	c.Rag = nil

	// 普通用户（JWT agentID=tutor）请求其它域 → 被锁定为 tutor。
	req := httptest.NewRequest(http.MethodGet, "/v1/agent/kbs?agent_id=math", nil)
	req = req.WithContext(authCtxWithRoleAgent(t, mgr, "user", "tutor"))
	if got, err := c.userAgentScope(req, "math"); err != nil || got != "tutor" {
		t.Fatalf("普通用户应锁定自身域 tutor, got %q err=%v", got, err)
	}

	// super_admin 显式指定域 → 跟随。
	req = httptest.NewRequest(http.MethodGet, "/v1/agent/kbs?agent_id=math", nil)
	req = req.WithContext(authCtxWithRoleAgent(t, mgr, "super_admin", ""))
	if got, err := c.userAgentScope(req, "math"); err != nil || got != "math" {
		t.Fatalf("超管应跟随显式域 math, got %q err=%v", got, err)
	}

	// super_admin 传 "*"（全门户标识）→ 回退默认域 tutor。
	req = httptest.NewRequest(http.MethodGet, "/v1/agent/kbs?agent_id=*", nil)
	req = req.WithContext(authCtxWithRoleAgent(t, mgr, "super_admin", ""))
	if got, err := c.userAgentScope(req, "*"); err != nil || got != defaultAgentID {
		t.Fatalf("超管全门户标识应回退默认域, got %q err=%v", got, err)
	}

	// 非法 ID → 拒绝。
	req = httptest.NewRequest(http.MethodGet, "/v1/agent/kbs?agent_id=bad!id", nil)
	req = req.WithContext(authCtxWithRoleAgent(t, mgr, "super_admin", ""))
	if _, err := c.userAgentScope(req, "bad!id"); err == nil {
		t.Fatal("非法智能体 ID 应被拒绝")
	}
}

// TestUserAgentScope_StrictDomain 严格多租户：孤儿域 / 已停用域一律拒绝（含超管）。
func TestUserAgentScope_StrictDomain(t *testing.T) {
	mgr := newTestManager(t)

	// 孤儿域：GetAgentPublic 返回 NotFound → userAgentScope 报错。
	c := newKBClients(mgr, nil)
	c.Auth = &fakeAuthClient{agentErr: status.Error(codes.NotFound, "agent 不存在")}
	req := httptest.NewRequest(http.MethodGet, "/v1/agent/kbs?agent_id=orphan", nil)
	req = req.WithContext(authCtxWithRoleAgent(t, mgr, "super_admin", ""))
	if _, err := c.userAgentScope(req, "orphan"); err == nil {
		t.Fatal("超管访问孤儿域应被拒绝")
	}

	// 已停用域：GetAgentPublic 返回 status=0 → userAgentScope 报错。
	c.Auth = &fakeAuthClient{agentDisabled: true}
	req = httptest.NewRequest(http.MethodGet, "/v1/agent/kbs?agent_id=tutor", nil)
	req = req.WithContext(authCtxWithRoleAgent(t, mgr, "super_admin", ""))
	if _, err := c.userAgentScope(req, "tutor"); err == nil {
		t.Fatal("已停用域应被拒绝")
	}

	// 管理端域（''）与全门户（'*'）豁免：不触发域校验（孤儿 fake 也不报错）。
	c.Auth = &fakeAuthClient{agentErr: status.Error(codes.NotFound, "agent 不存在")}
	mgmt := httptest.NewRequest(http.MethodGet, "/v1/agent/sessions", nil)
	mgmt = mgmt.WithContext(authCtxWithRoleAgent(t, mgr, "super_admin", ""))
	if got, err := c.agentScopeFor(mgmt, ""); err != nil || got != "" {
		t.Fatalf("超管管理端域应豁免返回空, got %q err=%v", got, err)
	}
	if got, err := c.agentScopeFor(mgmt, "*"); err != nil || got != "" {
		t.Fatalf("超管全门户标识应豁免返回空, got %q err=%v", got, err)
	}
}

// authCtxWithRoleAgent 构造携带 role/agentID 身份的请求上下文
// （等价于 RequireAuth 中间件写入的 identity）。
func authCtxWithRoleAgent(t *testing.T, mgr *auth.Manager, role, agentID string) context.Context {
	t.Helper()
	ctx := context.Background()
	ctx = identity.WithRole(ctx, role)
	ctx = identity.WithAgentID(ctx, agentID)
	ctx = identity.WithUserID(ctx, 1)
	return ctx
}

func TestListKBs(t *testing.T) {
	mgr := newTestManager(t)
	rag := &fakeRagClient{listResp: &ragv1.ListKBResponse{
		Bases: []*ragv1.KnowledgeBase{
			{Id: "kb_1", Name: "高数上册", Description: "课程讲义", DocCount: 12, Enabled: true},
			{Id: "kb_2", Name: "物理实验", DocCount: 5, Enabled: true},
			{Id: "kb_3", Name: "已停用库", DocCount: 3, Enabled: false},
		},
	}}

	t.Run("普通用户锁定自身域", func(t *testing.T) {
		f := &fakeRagClient{listResp: rag.listResp}
		c := newKBClients(mgr, f)
		tok, _, _ := mgr.SignAccess("7", "user", "tutor")
		rec := kbRequest(t, c, tok, "?agent_id=math") // 恶意请求其它域
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		if f.gotAgent != "tutor" {
			t.Fatalf("普通用户请求其它域应被锁定 tutor, got %q", f.gotAgent)
		}
		if !strings.Contains(rec.Body.String(), `"kb_1"`) {
			t.Fatalf("响应应含知识库列表, body=%s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"kb_3"`) {
			t.Fatalf("停用的知识库对普通用户不可见, body=%s", rec.Body.String())
		}
	})

	t.Run("超管显式指定域", func(t *testing.T) {
		f := &fakeRagClient{listResp: rag.listResp}
		c := newKBClients(mgr, f)
		tok, _, _ := mgr.SignAccess("1", "super_admin", "")
		rec := kbRequest(t, c, tok, "?agent_id=math")
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", rec.Code)
		}
		if f.gotAgent != "math" {
			t.Fatalf("超管应跟随显式域 math, got %q", f.gotAgent)
		}
	})

	t.Run("rag 未接入降级 503", func(t *testing.T) {
		c := newKBClients(mgr, nil)
		tok, _, _ := mgr.SignAccess("7", "user", "tutor")
		rec := kbRequest(t, c, tok, "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("rag 未接入应 503, got %d", rec.Code)
		}
	})
}
