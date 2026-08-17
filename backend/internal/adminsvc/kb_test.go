package adminsvc

// kb 模块测试：mock ragv1.RagServiceClient，验证 REST 层转发与错误映射。
// mock 内嵌 UnimplementedRagServiceServer，只需实现被测方法。

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// kbInDomain 构造归属指定资源域的假数据：listKBResp 含目标知识库
// （kbInScope 按域内列表校验，delete/upload/delete-doc 均需先命中列表）。
func kbInDomain(id, name string) *ragv1.ListKBResponse {
	return &ragv1.ListKBResponse{
		Bases: []*ragv1.KnowledgeBase{
			{Id: id, Name: name, DocCount: 1, CreatedAt: 1_700_000_000, UpdatedAt: 1_700_000_100},
		},
	}
}

// fakeRagClient 测试用 rag client：可编程返回结果或错误。
type fakeRagClient struct {
	ragv1.UnimplementedRagServiceServer

	listKBResp *ragv1.ListKBResponse
	listKBErr  error

	createResp *ragv1.KnowledgeBase
	createErr  error

	delKBErr error

	listDocResp *ragv1.ListDocumentsResponse
	listDocErr  error

	upsertFn   func(req *ragv1.UpsertDocumentRequest) (*ragv1.UpsertDocumentResponse, error)
	upsertResp *ragv1.UpsertDocumentResponse
	upsertErr  error

	delDocErr error

	updateFn func(req *ragv1.UpdateKBRequest) (*ragv1.KnowledgeBase, error)
	retryFn  func(req *ragv1.RetryDocumentRequest) (*ragv1.DocumentStatus, error)
	searchFn func() (*ragv1.SearchResponse, error)
}

func (f *fakeRagClient) ListKnowledgeBases(_ context.Context, _ *ragv1.ListKBRequest, _ ...grpc.CallOption) (*ragv1.ListKBResponse, error) {
	return f.listKBResp, f.listKBErr
}
func (f *fakeRagClient) CreateKnowledgeBase(_ context.Context, _ *ragv1.CreateKBRequest, _ ...grpc.CallOption) (*ragv1.KnowledgeBase, error) {
	return f.createResp, f.createErr
}
func (f *fakeRagClient) DeleteKnowledgeBase(_ context.Context, _ *ragv1.DeleteKBRequest, _ ...grpc.CallOption) (*ragv1.DeleteKBResponse, error) {
	if f.delKBErr != nil {
		return nil, f.delKBErr
	}
	return &ragv1.DeleteKBResponse{}, nil
}
func (f *fakeRagClient) ListDocuments(_ context.Context, _ *ragv1.ListDocumentsRequest, _ ...grpc.CallOption) (*ragv1.ListDocumentsResponse, error) {
	return f.listDocResp, f.listDocErr
}
func (f *fakeRagClient) UpsertDocument(_ context.Context, req *ragv1.UpsertDocumentRequest, _ ...grpc.CallOption) (*ragv1.UpsertDocumentResponse, error) {
	if f.upsertFn != nil {
		return f.upsertFn(req)
	}
	return f.upsertResp, f.upsertErr
}
func (f *fakeRagClient) DeleteDocument(_ context.Context, _ *ragv1.DeleteDocumentRequest, _ ...grpc.CallOption) (*ragv1.DeleteDocumentResponse, error) {
	if f.delDocErr != nil {
		return nil, f.delDocErr
	}
	return &ragv1.DeleteDocumentResponse{}, nil
}
func (f *fakeRagClient) GetDocumentStatus(_ context.Context, _ *ragv1.GetDocumentStatusRequest, _ ...grpc.CallOption) (*ragv1.DocumentStatus, error) {
	return nil, status.Error(codes.Unimplemented, "not used in kb module tests")
}
func (f *fakeRagClient) UpdateKnowledgeBase(_ context.Context, req *ragv1.UpdateKBRequest, _ ...grpc.CallOption) (*ragv1.KnowledgeBase, error) {
	if f.updateFn != nil {
		return f.updateFn(req)
	}
	return &ragv1.KnowledgeBase{Id: req.GetId(), Name: req.GetName(), Description: req.GetDescription()}, nil
}
func (f *fakeRagClient) RetryDocument(_ context.Context, req *ragv1.RetryDocumentRequest, _ ...grpc.CallOption) (*ragv1.DocumentStatus, error) {
	if f.retryFn != nil {
		return f.retryFn(req)
	}
	return &ragv1.DocumentStatus{DocId: req.GetId(), Status: ragv1.IngestStatus_QUEUED}, nil
}
func (f *fakeRagClient) Search(_ context.Context, _ *ragv1.SearchRequest, _ ...grpc.CallOption) (*ragv1.SearchResponse, error) {
	if f.searchFn != nil {
		return f.searchFn()
	}
	return nil, status.Error(codes.Unimplemented, "not used in kb module tests")
}

func newTestServiceWithRag(t *testing.T, f *fakeRagClient) *Service {
	t.Helper()
	s, err := NewService(Config{
		SkillsDir:     t.TempDir(),
		McpConfigFile: t.TempDir() + "/mcp.json",
		McpServersDir: t.TempDir(),
		Rag:           f,
		Log:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s
}

// newKBHandler 组装 kb 模块路由。
func newKBHandler(s *Service) http.Handler {
	mux := http.NewServeMux()
	newKBModule(s).Register(mux, s)
	return mux
}

// TestKB_ListBases 列出知识库并转为 JSON 视图。
func TestKB_ListBases(t *testing.T) {
	f := &fakeRagClient{listKBResp: &ragv1.ListKBResponse{
		Bases: []*ragv1.KnowledgeBase{
			{Id: "kb_1", Name: "示例大学", Description: "课程资料", DocCount: 3,
				CreatedAt: 1_700_000_000, UpdatedAt: 1_700_000_100},
		},
	}}
	s := newTestServiceWithRag(t, f)

	req := withRole(httptest.NewRequest(http.MethodGet, "/v1/admin/kb", nil), "super_admin", "")
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"示例大学"`) || !strings.Contains(body, `"doc_count":3`) {
		t.Errorf("响应缺少知识库字段: %s", body)
	}
	if !strings.Contains(body, `"agent_id":"tutor"`) {
		t.Errorf("超管未指定 agent_id 时应回退默认域 tutor: %s", body)
	}
}

// TestKB_Create 创建成功 + 非法 JSON + gRPC 错误映射。
func TestKB_Create(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeRagClient{createResp: &ragv1.KnowledgeBase{Id: "kb_x", Name: "实验手册"}}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb",
			bytes.NewBufferString(`{"name":"实验手册","description":"课程实验说明"}`)), "super_admin", "")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"实验手册"`) {
			t.Fatalf("创建失败: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		s := newTestServiceWithRag(t, &fakeRagClient{})
		req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb", bytes.NewBufferString("{oops")), "super_admin", "")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("非法 JSON 应 400，实际 %d", rec.Code)
		}
	})
	t.Run("grpc error mapped", func(t *testing.T) {
		f := &fakeRagClient{createErr: status.Error(codes.AlreadyExists, "知识库名已存在")}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb",
			bytes.NewBufferString(`{"name":"重复","description":""}`)), "super_admin", "")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("ALREADY_EXISTS 应 409，实际 %d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"code":40901`) {
			t.Errorf("错误体应含业务码 40901: %s", rec.Body.String())
		}
	})
}

// TestKB_GetDetail 详情含文档分页。
func TestKB_GetDetail(t *testing.T) {
	f := &fakeRagClient{
		listKBResp: &ragv1.ListKBResponse{
			Bases: []*ragv1.KnowledgeBase{{Id: "kb_1", Name: "教材", DocCount: 1,
				CreatedAt: 1_700_000_000, UpdatedAt: 1_700_000_000}},
		},
		listDocResp: &ragv1.ListDocumentsResponse{
			Documents: []*ragv1.DocumentStatus{
				{DocId: "doc_1", KbId: "kb_1", FileName: "第一章.md",
					Status: ragv1.IngestStatus_SUCCEEDED, ChunkCount: 8,
					CreatedAt: 1_700_000_000, UpdatedAt: 1_700_000_100},
			},
			Total: 1,
		},
	}
	s := newTestServiceWithRag(t, f)
	req := withRole(httptest.NewRequest(http.MethodGet, "/v1/admin/kb/kb_1?page=1&page_size=20", nil), "super_admin", "")
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"file_name":"第一章.md"`) || !strings.Contains(body, `"status":"succeeded"`) {
		t.Errorf("详情响应缺少文档信息: %s", body)
	}
}

// TestKB_GetDetail_NotFound 未知 kb → 404。
func TestKB_GetDetail_NotFound(t *testing.T) {
	f := &fakeRagClient{listKBResp: &ragv1.ListKBResponse{}}
	s := newTestServiceWithRag(t, f)
	req := withRole(httptest.NewRequest(http.MethodGet, "/v1/admin/kb/kb_missing", nil), "super_admin", "")
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知知识库应 404，实际 %d", rec.Code)
	}
}

// TestKB_DeleteKB 删除知识库。
func TestKB_DeleteKB(t *testing.T) {
	s := newTestServiceWithRag(t, &fakeRagClient{listKBResp: kbInDomain("kb_1", "课程资料")})
	req := withRole(httptest.NewRequest(http.MethodDelete, "/v1/admin/kb/kb_1", nil), "super_admin", "")
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("删除失败: %d %s", rec.Code, rec.Body.String())
	}
}

// TestKB_DeleteKB_OutOfScope 知识库不属于当前资源域 → 404（不泄露存在性）。
func TestKB_DeleteKB_OutOfScope(t *testing.T) {
	s := newTestServiceWithRag(t, &fakeRagClient{listKBResp: kbInDomain("kb_other", "其它智能体的库")})
	req := withRole(httptest.NewRequest(http.MethodDelete, "/v1/admin/kb/kb_1", nil), "agent_admin", "math")
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("域外知识库应 404，实际 %d", rec.Code)
	}
}

// TestKB_UploadDocument 上传文档（multipart），校验透传内容。
func TestKB_UploadDocument(t *testing.T) {
	var gotFileName string
	var gotContent []byte
	f := &fakeRagClient{
		listKBResp: kbInDomain("kb_1", "课程资料"),
		upsertFn: func(req *ragv1.UpsertDocumentRequest) (*ragv1.UpsertDocumentResponse, error) {
			gotFileName = req.GetFileName()
			gotContent = req.GetContent()
			return &ragv1.UpsertDocumentResponse{
				Status: &ragv1.DocumentStatus{DocId: "doc_1", KbId: "kb_1", FileName: req.GetFileName(),
					Status: ragv1.IngestStatus_QUEUED, CreatedAt: 1_700_000_000},
			}, nil
		},
	}
	s := newTestServiceWithRag(t, f)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "课程大纲.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("# 课程大纲\n第一章 绪论")); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb/kb_1/documents", &buf), "agent_admin", "tutor")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败: %d %s", rec.Code, rec.Body.String())
	}
	if gotFileName != "课程大纲.md" {
		t.Errorf("文件名透传错误: %q", gotFileName)
	}
	if !strings.Contains(string(gotContent), "第一章") {
		t.Errorf("内容透传错误: %q", string(gotContent))
	}
}

// TestKB_UploadDocument_EmptyFile 空文件 → 400。
func TestKB_UploadDocument_EmptyFile(t *testing.T) {
	s := newTestServiceWithRag(t, &fakeRagClient{listKBResp: kbInDomain("kb_1", "课程资料")})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if _, err := mw.CreateFormFile("file", "empty.md"); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb/kb_1/documents", &buf), "super_admin", "")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空文件应 400，实际 %d", rec.Code)
	}
}

// TestKB_DeleteDocument 删除文档。
func TestKB_DeleteDocument(t *testing.T) {
	s := newTestServiceWithRag(t, &fakeRagClient{listKBResp: kbInDomain("kb_1", "课程资料")})
	req := withRole(httptest.NewRequest(http.MethodDelete, "/v1/admin/kb/kb_1/documents/doc_1", nil), "super_admin", "")
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除文档失败: %d %s", rec.Code, rec.Body.String())
	}
}

// TestKB_UpdateKB 更新知识库：成功转发 + 域外知识库 404。
func TestKB_UpdateKB(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotName, gotDesc string
		f := &fakeRagClient{
			listKBResp: kbInDomain("kb_1", "旧名称"),
			updateFn: func(req *ragv1.UpdateKBRequest) (*ragv1.KnowledgeBase, error) {
				gotName, gotDesc = req.GetName(), req.GetDescription()
				return &ragv1.KnowledgeBase{Id: req.GetId(), Name: req.GetName(), Description: req.GetDescription()}, nil
			},
		}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPut, "/v1/admin/kb/kb_1",
			bytes.NewBufferString(`{"name":"新名称","description":"新描述"}`)), "super_admin", "")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"新名称"`) {
			t.Fatalf("更新失败: %d %s", rec.Code, rec.Body.String())
		}
		if gotName != "新名称" || gotDesc != "新描述" {
			t.Errorf("字段透传错误: name=%q desc=%q", gotName, gotDesc)
		}
	})
	t.Run("out of scope", func(t *testing.T) {
		f := &fakeRagClient{listKBResp: kbInDomain("kb_other", "其它域的库")}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPut, "/v1/admin/kb/kb_1",
			bytes.NewBufferString(`{"name":"x"}`)), "agent_admin", "math")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("域外知识库应 404，实际 %d", rec.Code)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		s := newTestServiceWithRag(t, &fakeRagClient{listKBResp: kbInDomain("kb_1", "课程资料")})
		req := withRole(httptest.NewRequest(http.MethodPut, "/v1/admin/kb/kb_1",
			bytes.NewBufferString("{oops")), "super_admin", "")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("非法 JSON 应 400，实际 %d", rec.Code)
		}
	})
}

// TestKB_RetryDocument 手动重试文档：成功 + 错误映射。
func TestKB_RetryDocument(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotDocID string
		f := &fakeRagClient{
			listKBResp: kbInDomain("kb_1", "课程资料"),
			retryFn: func(req *ragv1.RetryDocumentRequest) (*ragv1.DocumentStatus, error) {
				gotDocID = req.GetId()
				return &ragv1.DocumentStatus{DocId: req.GetId(), KbId: "kb_1",
					FileName: "broken.pdf", Status: ragv1.IngestStatus_QUEUED}, nil
			},
		}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb/kb_1/documents/doc_1/retry", nil), "super_admin", "")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
			t.Fatalf("重试失败: %d %s", rec.Code, rec.Body.String())
		}
		if gotDocID != "doc_1" {
			t.Errorf("文档 ID 透传错误: %q", gotDocID)
		}
	})
	t.Run("rag error mapped", func(t *testing.T) {
		f := &fakeRagClient{
			listKBResp: kbInDomain("kb_1", "课程资料"),
			retryFn: func(_ *ragv1.RetryDocumentRequest) (*ragv1.DocumentStatus, error) {
				return nil, status.Error(codes.InvalidArgument, "文档正在处理中，无法重试")
			},
		}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb/kb_1/documents/doc_1/retry", nil), "super_admin", "")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("INVALID_ARGUMENT 应 400，实际 %d %s", rec.Code, rec.Body.String())
		}
	})
}

// TestKB_Search 检索预览：命中片段透传 + 空 query 400。
func TestKB_Search(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeRagClient{
			listKBResp: kbInDomain("kb_1", "课程资料"),
			searchFn: func() (*ragv1.SearchResponse, error) {
				return &ragv1.SearchResponse{Chunks: []*ragv1.Chunk{
					{ChunkId: "ch_1", DocId: "doc_1", KbId: "kb_1", KbName: "课程资料",
						Content: "向量检索命中片段", Source: "第一章.md", Score: 0.87},
				}}, nil
			},
		}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb/kb_1/search",
			bytes.NewBufferString(`{"query":"示例大学","top_k":3}`)), "super_admin", "")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"向量检索命中片段"`) ||
			!strings.Contains(rec.Body.String(), `"score":0.87`) {
			t.Fatalf("检索预览失败: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("empty query", func(t *testing.T) {
		f := &fakeRagClient{listKBResp: kbInDomain("kb_1", "课程资料")}
		s := newTestServiceWithRag(t, f)
		req := withRole(httptest.NewRequest(http.MethodPost, "/v1/admin/kb/kb_1/search",
			bytes.NewBufferString(`{"query":"  "}`)), "super_admin", "")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newKBHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("空检索语句应 400，实际 %d", rec.Code)
		}
	})
}

// TestKB_RagNil 未接入 rag-service 时模块标记未实现且接口返回 503。
func TestKB_RagNil(t *testing.T) {
	s, err := NewService(Config{
		SkillsDir:     t.TempDir(),
		McpConfigFile: t.TempDir() + "/mcp.json",
		McpServersDir: t.TempDir(),
		Log:           zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if kb := findModule(s, "kb"); kb.Implemented() {
		t.Error("未注入 Rag 客户端时 kb 模块应标记未实现")
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/kb", nil)
	rec := httptest.NewRecorder()
	newKBHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未接入 rag 应 503，实际 %d", rec.Code)
	}
}

func findModule(s *Service, key string) Module {
	for _, m := range s.Modules() {
		if m.Key() == key {
			return m
		}
	}
	return nil
}
