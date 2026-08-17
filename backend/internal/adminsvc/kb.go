package adminsvc

// 知识库管理模块（P3-A）：经 gateway → rag-service gRPC 操作知识库与文档。
//
// 与 skill/mcp 的"文件态"不同，知识库是"数据库态"数据——存于 rag-service
// 的 PostgreSQL（含 pgvector 向量），管理端只做转发，不落本地文件。
//
// 多租户（阶段3）：所有接口先经 agentScopeFor 解析资源域——超管经
// ?agent_id= 显式指定，agent_admin/admin 锁定自身归属；域内校验（kbInScope）
// 保证智能体管理员只能操作自己智能体组的知识库。
//
// REST 契约（全部要求 admin 角色，鉴权由 gateway 中间件保证）：
//
//	GET    /v1/admin/kb                    列表知识库
//	POST   /v1/admin/kb                    创建知识库 {name, description}
//	GET    /v1/admin/kb/{id}               知识库详情（含文档分页）
//	PUT    /v1/admin/kb/{id}               更新知识库 {name, description}
//	DELETE /v1/admin/kb/{id}               删除知识库（级联删文档）
//	POST   /v1/admin/kb/{id}/documents     上传文档（multipart: file）
//	DELETE /v1/admin/kb/{id}/documents/{docId}  删除文档
//	POST   /v1/admin/kb/{id}/documents/{docId}/retry  手动重试摄取失败文档
//	POST   /v1/admin/kb/{id}/search        检索预览 {query, top_k?} → 命中片段
//
// 错误映射：gRPC status → 统一业务错误（apperr.FromGRPCError），
// 与其它管理端模块的 JSON 错误体保持一致。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
)

// kbModule 知识库管理模块。
type kbModule struct{ s *Service }

func newKBModule(s *Service) Module { return kbModule{s: s} }

func (m kbModule) Key() string { return "kb" }
func (m kbModule) Name() string {
	return "知识库管理"
}
func (m kbModule) Description() string {
	return "上传/删除课程文档，自动切分并向量化，供智能体检索引用"
}
func (m kbModule) Implemented() bool { return m.s.rag != nil }

func (m kbModule) Register(mux *http.ServeMux, _ *Service) {
	mux.HandleFunc("GET /v1/admin/kb", m.s.handleListKBs)
	mux.HandleFunc("POST /v1/admin/kb", m.s.handleCreateKB)
	mux.HandleFunc("GET /v1/admin/kb/{id}", m.s.handleGetKB)
	mux.HandleFunc("PUT /v1/admin/kb/{id}", m.s.handleUpdateKB)
	mux.HandleFunc("DELETE /v1/admin/kb/{id}", m.s.handleDeleteKB)
	mux.HandleFunc("POST /v1/admin/kb/{id}/documents", m.s.handleUploadDocument)
	mux.HandleFunc("DELETE /v1/admin/kb/{id}/documents/{docId}", m.s.handleDeleteDocument)
	mux.HandleFunc("POST /v1/admin/kb/{id}/documents/{docId}/retry", m.s.handleRetryDocument)
	mux.HandleFunc("POST /v1/admin/kb/{id}/search", m.s.handleSearchKB)
}

// kbView 知识库对外视图（JSON 契约）。
type kbView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	AgentID     string    `json:"agent_id,omitempty"` // 所属智能体域（多租户隔离）
	Enabled     bool      `json:"enabled"`            // 启用状态（false = 停用，资源启停体系）
	DocCount    int32     `json:"doc_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Documents   []docView `json:"documents,omitempty"` // 详情接口附带（分页）
	Total       int32     `json:"total,omitempty"`     // 文档总数（分页用）
}

// docView 文档视图（摄取状态对前端可见）。
type docView struct {
	DocID      string    `json:"doc_id"`
	KBID       string    `json:"kb_id"`
	FileName   string    `json:"file_name"`
	Status     string    `json:"status"` // queued/processing/succeeded/failed
	ChunkCount int32     `json:"chunk_count"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// kbSearchReq 检索预览请求体。
type kbSearchReq struct {
	Query string `json:"query"`
	TopK  int32  `json:"top_k,omitempty"` // 默认 5，上限 20
}

// kbHitView 检索命中片段视图。
type kbHitView struct {
	ChunkID string  `json:"chunk_id"`
	DocID   string  `json:"doc_id"`
	Content string  `json:"content"`
	Source  string  `json:"source"`
	Score   float64 `json:"score"`
}

func fromKB(pb *ragv1.KnowledgeBase) kbView {
	return kbView{
		ID:          pb.GetId(),
		Name:        pb.GetName(),
		Description: pb.GetDescription(),
		AgentID:     pb.GetAgentId(),
		Enabled:     pb.GetEnabled(),
		DocCount:    pb.GetDocCount(),
		CreatedAt:   time.Unix(pb.GetCreatedAt(), 0),
		UpdatedAt:   time.Unix(pb.GetUpdatedAt(), 0),
	}
}

// ingestStatusLower proto 摄取状态枚举 → 前端契约小写字符串。
// 不能直接用 pb.GetStatus().String()（返回大写枚举名 SUCCEEDED），
// 前端 statusMeta/筛选均按小写 succeeded/queued/processing/failed 匹配。
var ingestStatusLower = map[ragv1.IngestStatus]string{
	ragv1.IngestStatus_QUEUED:     "queued",
	ragv1.IngestStatus_PROCESSING: "processing",
	ragv1.IngestStatus_SUCCEEDED:  "succeeded",
	ragv1.IngestStatus_FAILED:     "failed",
}

func fromDoc(pb *ragv1.DocumentStatus) docView {
	status, ok := ingestStatusLower[pb.GetStatus()]
	if !ok {
		status = "unknown"
	}
	return docView{
		DocID:      pb.GetDocId(),
		KBID:       pb.GetKbId(),
		FileName:   pb.GetFileName(),
		Status:     status,
		ChunkCount: pb.GetChunkCount(),
		Error:      pb.GetError(),
		CreatedAt:  time.Unix(pb.GetCreatedAt(), 0),
		UpdatedAt:  time.Unix(pb.GetUpdatedAt(), 0),
	}
}

// handleListKBs 列出当前资源域内的全部知识库。
func (s *Service) handleListKBs(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.rag.ListKnowledgeBases(r.Context(), &ragv1.ListKBRequest{AgentId: agent})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	bases := make([]kbView, 0, len(resp.GetBases()))
	for _, pb := range resp.GetBases() {
		bases = append(bases, fromKB(pb))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bases": bases, "agent_id": agent})
}

type createKBReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Enabled 新启用状态；nil = 不修改（仅改名称/描述），避免旧客户端误停用。
	Enabled *bool `json:"enabled"`
}

// handleCreateKB 创建知识库（归属当前资源域）。
func (s *Service) handleCreateKB(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	var req createKBReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.rag.CreateKnowledgeBase(r.Context(), &ragv1.CreateKBRequest{
		Name:        req.Name,
		Description: req.Description,
		AgentId:     agent,
	})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	s.logInfo("kb created", zap.String("agent", agent), zap.String("kb", resp.GetId()), zap.String("name", resp.GetName()))
	writeJSON(w, http.StatusOK, map[string]any{"kb": fromKB(resp), "agent_id": agent})
}

// handleUpdateKB 更新知识库名称/描述（body 同 createKBReq）；仅限当前资源域内。
func (s *Service) handleUpdateKB(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少知识库 ID"))
		return
	}
	var req createKBReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.kbInScope(r.Context(), agent, id); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.rag.UpdateKnowledgeBase(r.Context(), &ragv1.UpdateKBRequest{
		Id:          id,
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
	})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	s.logInfo("kb updated", zap.String("agent", agent), zap.String("kb", id), zap.String("name", resp.GetName()))
	writeJSON(w, http.StatusOK, map[string]any{"kb": fromKB(resp), "agent_id": agent})
}

// handleGetKB 知识库详情 + 文档分页（page/page_size 查询参数）。
func (s *Service) handleGetKB(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少知识库 ID"))
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := parseIntParam(r, "page", 1)
	pageSize := parseIntParam(r, "page_size", 20)
	found, err := s.kbInScope(r.Context(), agent, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	docResp, err := s.rag.ListDocuments(r.Context(), &ragv1.ListDocumentsRequest{
		KbId:     id,
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	view := fromKB(found)
	docs := make([]docView, 0, len(docResp.GetDocuments()))
	for _, pb := range docResp.GetDocuments() {
		docs = append(docs, fromDoc(pb))
	}
	view.Documents = docs
	view.Total = docResp.GetTotal()
	writeJSON(w, http.StatusOK, map[string]any{"kb": view, "agent_id": agent})
}

// kbInScope 按"域内列表"校验知识库存在且归属当前资源域。
// 归属其它域或不存在一律返回 404（不泄露其它智能体资源的存在性）。
func (s *Service) kbInScope(ctx context.Context, agent, id string) (*ragv1.KnowledgeBase, error) {
	listResp, err := s.rag.ListKnowledgeBases(ctx, &ragv1.ListKBRequest{AgentId: agent})
	if err != nil {
		return nil, apperr.FromGRPCError(err)
	}
	for _, pb := range listResp.GetBases() {
		if pb.GetId() == id {
			return pb, nil
		}
	}
	return nil, apperr.New(apperr.CodeNotFound, "知识库不存在")
}

// handleDeleteKB 删除知识库（级联删除文档）；仅限当前资源域内。
func (s *Service) handleDeleteKB(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少知识库 ID"))
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.kbInScope(r.Context(), agent, id); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.rag.DeleteKnowledgeBase(r.Context(), &ragv1.DeleteKBRequest{Id: id}); err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	s.logInfo("kb deleted", zap.String("agent", agent), zap.String("kb", id))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "agent_id": agent})
}

// handleUploadDocument 上传文档（multipart/form-data 的 file 字段）。
// 文件名与字节原样透传 rag-service；扩展名不支持时由 rag 侧返回错误。
func (s *Service) handleUploadDocument(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少知识库 ID"))
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	// 域内校验：目标知识库必须归属当前资源域。
	if _, err := s.kbInScope(r.Context(), agent, id); err != nil {
		writeError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.kbMaxBytes))
	if err := r.ParseMultipartForm(int64(s.kbMaxBytes)); err != nil {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument,
			fmt.Sprintf("解析上传表单失败（文件需 ≤%dMB）", s.kbMaxBytes>>20)))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少上传文件（字段名 file）"))
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, int64(s.kbMaxBytes)))
	if err != nil {
		writeError(w, r, apperr.Wrap(apperr.CodeInternal, "读取上传文件失败", err))
		return
	}
	if len(content) == 0 {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "上传文件为空"))
		return
	}
	fileName := strings.TrimSpace(header.Filename)
	if fileName == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "上传文件缺少文件名"))
		return
	}

	resp, err := s.rag.UpsertDocument(r.Context(), &ragv1.UpsertDocumentRequest{
		KbId:     id,
		FileName: fileName,
		Content:  content,
	})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	s.log.Info("知识库文档已上传",
		zap.String("agent", agent),
		zap.String("kb_id", id),
		zap.String("file", fileName),
		zap.Int("bytes", len(content)),
	)
	writeJSON(w, http.StatusOK, map[string]any{"doc": fromDoc(resp.GetStatus()), "agent_id": agent})
}

// handleDeleteDocument 删除文档（KB 内的单个文档）；仅限当前资源域内。
func (s *Service) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	kbID := r.PathValue("id")
	docID := r.PathValue("docId")
	if docID == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少文档 ID"))
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	// 域内校验：文档所属知识库必须归属当前资源域。
	if _, err := s.kbInScope(r.Context(), agent, kbID); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.rag.DeleteDocument(r.Context(), &ragv1.DeleteDocumentRequest{Id: docID}); err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	s.logInfo("kb document deleted", zap.String("agent", agent), zap.String("kb", kbID), zap.String("doc", docID))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "agent_id": agent})
}

// handleRetryDocument 手动重试摄取失败文档；仅限当前资源域内。
func (s *Service) handleRetryDocument(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	kbID := r.PathValue("id")
	docID := r.PathValue("docId")
	if kbID == "" || docID == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少知识库/文档 ID"))
		return
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	// 域内校验：文档所属知识库必须归属当前资源域。
	if _, err := s.kbInScope(r.Context(), agent, kbID); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.rag.RetryDocument(r.Context(), &ragv1.RetryDocumentRequest{Id: docID})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	s.logInfo("kb document retried", zap.String("agent", agent), zap.String("kb", kbID), zap.String("doc", docID))
	writeJSON(w, http.StatusOK, map[string]any{"doc": fromDoc(resp), "agent_id": agent})
}

// handleSearchKB 检索预览：在指定知识库内检索，返回命中片段供管理端验证向量化质量。
// 仅限当前资源域内；query 必填，top_k 默认 5 上限 20。
func (s *Service) handleSearchKB(w http.ResponseWriter, r *http.Request) {
	if s.rag == nil {
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "rag-service 未接入，知识库功能不可用"))
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "缺少知识库 ID"))
		return
	}
	var req kbSearchReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "检索语句不能为空"))
		return
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}
	if req.TopK > 20 {
		req.TopK = 20
	}
	agent, err := agentScopeFor(r, r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.kbInScope(r.Context(), agent, id); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.rag.Search(r.Context(), &ragv1.SearchRequest{
		Query:   req.Query,
		KbIds:   []string{id},
		TopK:    req.TopK,
		AgentId: agent,
	})
	if err != nil {
		writeError(w, r, apperr.FromGRPCError(err))
		return
	}
	hits := make([]kbHitView, 0, len(resp.GetChunks()))
	for _, c := range resp.GetChunks() {
		hits = append(hits, kbHitView{
			ChunkID: c.GetChunkId(),
			DocID:   c.GetDocId(),
			Content: c.GetContent(),
			Source:  c.GetSource(),
			Score:   c.GetScore(),
		})
	}
	s.logInfo("kb search preview", zap.String("agent", agent), zap.String("kb", id), zap.String("query", req.Query), zap.Int("hits", len(hits)))
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits, "agent_id": agent})
}

// parseIntParam 解析查询参数为整数；非法/缺省用默认值。
func parseIntParam(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	return n
}
