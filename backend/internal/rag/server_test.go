package rag

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// newTestServer 构造 gRPC server（无 DB，仅测参数校验与错误映射路径）。
func newTestServer() *Server {
	return NewServer(newTestService(), zap.NewNop())
}

// TestServer_Search_EmptyQuery 空 query → InvalidArgument。
func TestServer_Search_EmptyQuery(t *testing.T) {
	srv := newTestServer()
	_, err := srv.Search(context.Background(), &ragv1.SearchRequest{Query: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("空 query 应 InvalidArgument，实际 %v", err)
	}
}

// TestServer_CreateKB_EmptyName 空名称 → InvalidArgument。
func TestServer_CreateKB_EmptyName(t *testing.T) {
	srv := newTestServer()
	_, err := srv.CreateKnowledgeBase(context.Background(), &ragv1.CreateKBRequest{Name: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("空名称应 InvalidArgument，实际 %v", err)
	}
}

// TestServer_UpsertDocument_InvalidArgs 缺参 → InvalidArgument。
func TestServer_UpsertDocument_InvalidArgs(t *testing.T) {
	srv := newTestServer()
	cases := []*ragv1.UpsertDocumentRequest{
		{},
		{KbId: "kb1"},
		{KbId: "kb1", FileName: "a.md"},
	}
	for _, req := range cases {
		if _, err := srv.UpsertDocument(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("请求 %+v 应 InvalidArgument，实际 %v", req, err)
		}
	}
}

// TestServer_UpdateKB_EmptyID 缺 id → InvalidArgument。
func TestServer_UpdateKB_EmptyID(t *testing.T) {
	srv := newTestServer()
	if _, err := srv.UpdateKnowledgeBase(context.Background(), &ragv1.UpdateKBRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("空 id 应 InvalidArgument，实际 %v", err)
	}
}

// TestServer_RetryDocument_EmptyID 缺 id → InvalidArgument。
func TestServer_RetryDocument_EmptyID(t *testing.T) {
	srv := newTestServer()
	if _, err := srv.RetryDocument(context.Background(), &ragv1.RetryDocumentRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("空 id 应 InvalidArgument，实际 %v", err)
	}
}

// TestToGRPC_ErrorMapping 领域错误 → gRPC code 映射。
func TestToGRPC_ErrorMapping(t *testing.T) {
	tests := []struct {
		err  error
		want codes.Code
	}{
		{ErrNotFound, codes.NotFound},
		{ErrNameExists, codes.AlreadyExists},
		{ErrInvalidArgument, codes.InvalidArgument},
		{ErrUnsupportedFileType, codes.InvalidArgument},
		{context.DeadlineExceeded, codes.Internal}, // 未识别错误走 Internal
	}
	for _, tc := range tests {
		if got := status.Code(toGRPC(tc.err)); got != tc.want {
			t.Errorf("toGRPC(%v) = %v，want %v", tc.err, got, tc.want)
		}
	}
}

// TestToStatusPB 状态字符串 → proto 枚举。
func TestToStatusPB(t *testing.T) {
	tests := map[string]ragv1.IngestStatus{
		StatusQueued:    ragv1.IngestStatus_QUEUED,
		StatusProcessing: ragv1.IngestStatus_PROCESSING,
		StatusSucceeded: ragv1.IngestStatus_SUCCEEDED,
		StatusFailed:    ragv1.IngestStatus_FAILED,
		"unknown":       ragv1.IngestStatus_INGEST_STATUS_UNSPECIFIED,
	}
	for in, want := range tests {
		if got := toStatusPB(in); got != want {
			t.Errorf("toStatusPB(%q) = %v，want %v", in, got, want)
		}
	}
}

// TestAnyMapToStrings 元数据 map[string]any → map[string]string。
func TestAnyMapToStrings(t *testing.T) {
	in := map[string]any{"source": "a.md", "seq": 3}
	out := anyMapToStrings(in)
	if out["source"] != "a.md" || out["seq"] != "3" {
		t.Errorf("转换结果错误: %+v", out)
	}
	if got := anyMapToStrings(nil); got != nil {
		t.Errorf("nil 输入应返回 nil，实际 %+v", got)
	}
}
