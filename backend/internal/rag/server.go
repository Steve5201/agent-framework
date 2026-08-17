package rag

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// Server ragv1.RagServiceServer 实现：领域错误 → gRPC status 映射。
type Server struct {
	ragv1.UnimplementedRagServiceServer
	svc *Service
	log *zap.Logger
}

// NewServer 构造 gRPC server。
func NewServer(svc *Service, log *zap.Logger) *Server {
	if log == nil {
		log = zap.NewNop()
	}
	return &Server{svc: svc, log: log}
}

// Search 混合检索（智能体 kb_search 工具入口）。
func (s *Server) Search(ctx context.Context, req *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
	if req == nil || req.Query == "" {
		return nil, status.Error(codes.InvalidArgument, "query 不能为空")
	}
	hits, err := s.svc.Search(ctx, req.Query, req.KbIds, int(req.TopK), req.MinScore, req.GetAgentId())
	if err != nil {
		return nil, toGRPC(err)
	}
	chunks := make([]*ragv1.Chunk, len(hits))
	for i, h := range hits {
		chunks[i] = &ragv1.Chunk{
			ChunkId:  h.ChunkID,
			DocId:    h.DocID,
			KbId:     h.KBID,
			KbName:   h.KBName,
			Content:  h.Content,
			Source:   h.Source,
			Score:    h.Score,
			Metadata: anyMapToStrings(h.Metadata),
		}
	}
	s.log.Info("rag 检索", zap.String("query", req.Query), zap.Int("hits", len(chunks)))
	return &ragv1.SearchResponse{Chunks: chunks}, nil
}

// CreateKnowledgeBase 创建知识库。
func (s *Server) CreateKnowledgeBase(ctx context.Context, req *ragv1.CreateKBRequest) (*ragv1.KnowledgeBase, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "请求为空")
	}
	kb, err := s.svc.CreateKB(ctx, req.Name, req.Description, req.AgentId)
	if err != nil {
		return nil, toGRPC(err)
	}
	return toKBPB(kb), nil
}

// ListKnowledgeBases 列出知识库（agent_id 空 = 全部；非空 = 仅该智能体域）。
func (s *Server) ListKnowledgeBases(ctx context.Context, req *ragv1.ListKBRequest) (*ragv1.ListKBResponse, error) {
	if req == nil {
		req = &ragv1.ListKBRequest{}
	}
	kbs, err := s.svc.ListKBs(ctx, req.AgentId)
	if err != nil {
		return nil, toGRPC(err)
	}
	out := make([]*ragv1.KnowledgeBase, len(kbs))
	for i := range kbs {
		out[i] = toKBPB(&kbs[i])
	}
	return &ragv1.ListKBResponse{Bases: out}, nil
}

// DeleteKnowledgeBase 删除知识库。
func (s *Server) DeleteKnowledgeBase(ctx context.Context, req *ragv1.DeleteKBRequest) (*ragv1.DeleteKBResponse, error) {
	if req == nil || req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id 不能为空")
	}
	if err := s.svc.DeleteKB(ctx, req.Id); err != nil {
		return nil, toGRPC(err)
	}
	return &ragv1.DeleteKBResponse{}, nil
}

// UpsertDocument 提交文档并触发异步摄取。
func (s *Server) UpsertDocument(ctx context.Context, req *ragv1.UpsertDocumentRequest) (*ragv1.UpsertDocumentResponse, error) {
	if req == nil || req.KbId == "" || req.FileName == "" || len(req.Content) == 0 {
		return nil, status.Error(codes.InvalidArgument, "kb_id/file_name/content 不能为空")
	}
	doc, err := s.svc.UpsertDocument(ctx, req.KbId, req.FileName, req.Content)
	if err != nil {
		return nil, toGRPC(err)
	}
	return &ragv1.UpsertDocumentResponse{Status: toDocStatusPB(doc)}, nil
}

// ListDocuments 分页列出知识库文档。
func (s *Server) ListDocuments(ctx context.Context, req *ragv1.ListDocumentsRequest) (*ragv1.ListDocumentsResponse, error) {
	if req == nil || req.KbId == "" {
		return nil, status.Error(codes.InvalidArgument, "kb_id 不能为空")
	}
	docs, total, err := s.svc.ListDocuments(ctx, req.KbId, int(req.Page), int(req.PageSize))
	if err != nil {
		return nil, toGRPC(err)
	}
	out := make([]*ragv1.DocumentStatus, len(docs))
	for i := range docs {
		out[i] = toDocStatusPB(&docs[i])
	}
	return &ragv1.ListDocumentsResponse{Documents: out, Total: int32(total)}, nil
}

// DeleteDocument 删除文档。
func (s *Server) DeleteDocument(ctx context.Context, req *ragv1.DeleteDocumentRequest) (*ragv1.DeleteDocumentResponse, error) {
	if req == nil || req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id 不能为空")
	}
	if err := s.svc.DeleteDocument(ctx, req.Id); err != nil {
		return nil, toGRPC(err)
	}
	return &ragv1.DeleteDocumentResponse{}, nil
}

// GetDocumentStatus 查询文档摄取状态。
func (s *Server) GetDocumentStatus(ctx context.Context, req *ragv1.GetDocumentStatusRequest) (*ragv1.DocumentStatus, error) {
	if req == nil || req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id 不能为空")
	}
	doc, err := s.svc.GetDocument(ctx, req.Id)
	if err != nil {
		return nil, toGRPC(err)
	}
	return toDocStatusPB(doc), nil
}

// UpdateKnowledgeBase 更新知识库名称/描述。
func (s *Server) UpdateKnowledgeBase(ctx context.Context, req *ragv1.UpdateKBRequest) (*ragv1.KnowledgeBase, error) {
	if req == nil || req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id 不能为空")
	}
	// enabled 为 optional 字段：未传（nil）表示保持原状，避免旧客户端
	// 只改名/描述时误把知识库停用（proto3 非 optional 的 bool 无法区分 false 与未传）。
	var enabled *bool
	if req.Enabled != nil {
		e := req.GetEnabled()
		enabled = &e
	}
	kb, err := s.svc.UpdateKB(ctx, req.Id, req.Name, req.Description, enabled)
	if err != nil {
		return nil, toGRPC(err)
	}
	return toKBPB(kb), nil
}

// RetryDocument 手动重试摄取失败文档。
func (s *Server) RetryDocument(ctx context.Context, req *ragv1.RetryDocumentRequest) (*ragv1.DocumentStatus, error) {
	if req == nil || req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id 不能为空")
	}
	doc, err := s.svc.RetryDocument(ctx, req.Id)
	if err != nil {
		return nil, toGRPC(err)
	}
	return toDocStatusPB(doc), nil
}

// ---------------------------------------------------------------------------
// 转换与错误映射
// ---------------------------------------------------------------------------

func toKBPB(kb *KnowledgeBase) *ragv1.KnowledgeBase {
	return &ragv1.KnowledgeBase{
		Id:          kb.ID,
		Name:        kb.Name,
		Description: kb.Description,
		DocCount:    int32(kb.DocCount),
		CreatedAt:   kb.CreatedAt.Unix(),
		UpdatedAt:   kb.UpdatedAt.Unix(),
		AgentId:     kb.AgentID,
		Enabled:     kb.Enabled,
	}
}

func toDocStatusPB(d *Document) *ragv1.DocumentStatus {
	return &ragv1.DocumentStatus{
		DocId:      d.ID,
		KbId:       d.KBID,
		FileName:   d.FileName,
		Status:     toStatusPB(d.Status),
		ChunkCount: int32(d.ChunkCount),
		Error:      d.Error,
		CreatedAt:  d.CreatedAt.Unix(),
		UpdatedAt:  d.UpdatedAt.Unix(),
	}
}

func toStatusPB(s string) ragv1.IngestStatus {
	switch s {
	case StatusQueued:
		return ragv1.IngestStatus_QUEUED
	case StatusProcessing:
		return ragv1.IngestStatus_PROCESSING
	case StatusSucceeded:
		return ragv1.IngestStatus_SUCCEEDED
	case StatusFailed:
		return ragv1.IngestStatus_FAILED
	}
	return ragv1.IngestStatus_INGEST_STATUS_UNSPECIFIED
}

func anyMapToStrings(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

func toGRPC(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrNameExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, ErrUnsupportedFileType):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrNotConfigured):
		// embedding 未配置：功能降级，明确提示（前端据此展示"知识库不可用"）。
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
