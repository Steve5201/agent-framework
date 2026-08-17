package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/rag/embedding"
	"github.com/Steve5201/agent-backend/internal/rag/ingest"
	"github.com/Steve5201/agent-backend/internal/rag/sandboxclient"
)

// Service RAG 业务编排：知识库/文档管理 + 摄取 worker + 混合检索。
// 依赖注入全部接口（Store/Embedding/Chunker），便于替换实现与单元测试。
type Service struct {
	store        *Store
	emb          embedding.Provider
	parser       ingest.Parser
	chunker      ingest.Chunker
	chunkOpts    ingest.ChunkOptions
	workers      int
	pollInterval time.Duration
	batchSize    int // 向量化分批大小（大文档/批量上传防单次请求超时）
	maxAttempts  int // 摄取失败自动重试上限（指数退避，超限才落 failed）
	backoffs     []time.Duration
	log          *zap.Logger

	// mediaRoot 沙盒共享卷媒体落盘根目录（P3-A6 孤儿媒体清理用）。
	// 媒体公共只读区：<mediaRoot>/rag-media/<docID>/。删除文档/知识库时
	// best-effort 清理该目录（见 cleanupMedia）。
	mediaRoot string
	// mediaCleanupTTL 无主媒体宽限期（模块三）：文档删除后残留的 rag-media
	// 目录保留宽限期再删，防 docID 复用误删；缺省 7 天。
	mediaCleanupTTL time.Duration
}

// retryBackoffs 自动重试退避序列（索引 = 已重试次数 attempt）。
// attempt 0/1/2 分别延迟 10s/1m/5m，其后沿用末位 5m。
var retryBackoffs = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute}

// NewService 构造 RAG 服务。
func NewService(store *Store, emb embedding.Provider, cfg config.RAGConfig, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	batch := cfg.EmbeddingBatchSize
	if batch <= 0 {
		batch = 16
	}
	maxAttempts := cfg.IngestMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	// 文档解析器：md/txt/html/xlsx 原生；pdf/docx/pptx 委托沙盒（RAG_SANDBOX_URL 非空时）。
	parser := ingest.Parser{}
	if cfg.SandboxURL != "" {
		parser = ingest.Parser{
			Sandbox: sandboxclient.New(cfg.SandboxURL, cfg.SandboxWorkRoot, cfg.SandboxUserID, log),
		}
		// 单篇文档字节上限（RAG_MAX_DOC_MB，0 = 客户端默认 20MB）。
		parser.Sandbox.MaxDocBytes = int64(cfg.MaxDocMB) << 20
	}
	mediaRoot := cfg.SandboxWorkRoot
	if mediaRoot == "" {
		mediaRoot = "/work"
	}
	cleanupTTL := cfg.MediaCleanupTTL
	if cleanupTTL <= 0 {
		cleanupTTL = 7 * 24 * time.Hour
	}
	return &Service{
		store:   store,
		emb:     emb,
		parser:  parser,
		chunker: ingest.Chunker{},
		chunkOpts: ingest.ChunkOptions{
			MaxLen:  800,
			Overlap: 100,
		},
		workers:         cfg.IngestWorkers,
		pollInterval:    cfg.IngestPollInterval,
		batchSize:       batch,
		maxAttempts:     maxAttempts,
		backoffs:        retryBackoffs,
		log:             log,
		mediaRoot:       mediaRoot,
		mediaCleanupTTL: cleanupTTL,
	}
}

// ---------------------------------------------------------------------------
// 知识库
// ---------------------------------------------------------------------------

// CreateKB 创建知识库。agentID 为所属智能体域（多租户隔离）。
func (s *Service) CreateKB(ctx context.Context, name, desc, agentID string) (*KnowledgeBase, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 50 {
		return nil, fmt.Errorf("%w: 知识库名 1~50 字符", ErrInvalidArgument)
	}
	if len([]rune(desc)) > 200 {
		return nil, fmt.Errorf("%w: 描述 ≤200 字符", ErrInvalidArgument)
	}
	if agentID == "" {
		agentID = DefaultAgentID
	}
	kb := &KnowledgeBase{Name: name, Description: desc, AgentID: agentID}
	if err := s.store.CreateKB(ctx, kb); err != nil {
		return nil, err
	}
	s.log.Info("知识库创建", zap.String("kb", kb.ID), zap.String("name", name), zap.String("agent", agentID))
	return kb, nil
}

// UpdateKB 更新知识库名称/描述/启用状态。校验规则与 CreateKB 一致；重名返回 ErrNameExists。
// enabled 为 nil = 仅改名称/描述（不触碰启用状态，旧调用方安全）。
func (s *Service) UpdateKB(ctx context.Context, id, name, desc string, enabled *bool) (*KnowledgeBase, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 50 {
		return nil, fmt.Errorf("%w: 知识库名 1~50 字符", ErrInvalidArgument)
	}
	if len([]rune(desc)) > 200 {
		return nil, fmt.Errorf("%w: 描述 ≤200 字符", ErrInvalidArgument)
	}
	kb, err := s.store.UpdateKB(ctx, id, name, desc, enabled)
	if err != nil {
		return nil, err
	}
	s.log.Info("知识库更新", zap.String("kb", id), zap.String("name", name))
	return kb, nil
}

// RetryDocument 手动重试摄取：失败/已就绪文档重置为 queued（attempt 清零、
// 退避清除）重新入队，worker 下次轮询即重新摄取。处理中返回 ErrInvalidArgument。
func (s *Service) RetryDocument(ctx context.Context, id string) (*Document, error) {
	// 先确认文档存在（GetDocument 附带内容，管理端低频操作可接受）。
	if _, err := s.store.GetDocument(ctx, id); err != nil {
		return nil, err
	}
	ok, err := s.store.ResetDocumentForRetry(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: 文档正在处理中，无法重试", ErrInvalidArgument)
	}
	s.log.Info("文档手动重试", zap.String("doc", id))
	return s.store.GetDocument(ctx, id)
}

// ListKBs 列出知识库；agentID 空 = 全部，非空 = 仅该智能体域。
func (s *Service) ListKBs(ctx context.Context, agentID string) ([]KnowledgeBase, error) {
	return s.store.ListKBs(ctx, agentID)
}

func (s *Service) DeleteKB(ctx context.Context, id string) error {
	// 先取知识库全部文档 ID（供删除后清理媒体文件），再删库（CASCADE 删 chunk/doc）。
	docIDs, err := s.store.ListDocumentIDs(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteKB(ctx, id); err != nil {
		return err
	}
	for _, docID := range docIDs {
		s.cleanupMedia(docID)
	}
	s.log.Info("知识库删除", zap.String("kb", id), zap.Int("docs", len(docIDs)))
	return nil
}

// ---------------------------------------------------------------------------
// 媒体清理（P3-A6）：删除文档时 best-effort 清理沙盒提取的媒体文件。
// 知识库/文档删除只级联 DB 行（documents/chunks），rag-media/ 下的提取文件
// 需显式清理，否则长期累积孤儿文件。文件系统失败仅记日志、不阻断业务。
// ---------------------------------------------------------------------------

// mediaDirFor 文档媒体目录（与 sandboxclient.ParsedDoc 落盘路径一致）：
// <WorkRoot>/rag-media/<docID>/（公共只读区，所有用户可读）。
func (s *Service) mediaDirFor(docID string) string {
	return filepath.Join(s.mediaRoot, sandboxclient.MediaDirName, docID)
}

// cleanupMedia 删除单个文档的媒体目录（目录不存在时 os.RemoveAll 幂等返回 nil）。
func (s *Service) cleanupMedia(docID string) {
	dir := s.mediaDirFor(docID)
	if err := os.RemoveAll(dir); err != nil {
		s.log.Warn("清理文档媒体失败",
			zap.String("doc", docID), zap.String("dir", dir), zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// 文档摄取（异步：落库即返回，worker 后台处理）
// ---------------------------------------------------------------------------

// UpsertDocument 提交文档原始字节并触发摄取（queued）。
// 幂等：同 (kb, file_name) 已成功且内容 hash 一致 → 直接返回现有文档，不重复摄取。
func (s *Service) UpsertDocument(ctx context.Context, kbID, fileName string, content []byte) (*Document, error) {
	if kbID == "" || fileName == "" || len(content) == 0 {
		return nil, fmt.Errorf("%w: kb_id/file_name/content 不能为空", ErrInvalidArgument)
	}
	// 先校验格式、后查询知识库（避免对不支持的格式做无谓查询）。
	fileType := inferFileType(fileName)
	switch fileType {
	case "md", "txt", "html", "xlsx":
	case "pdf", "docx", "pptx":
		if s.parser.Sandbox == nil {
			return nil, fmt.Errorf("%w: %s（需启用解析沙盒 RAG_SANDBOX_URL）", ErrUnsupportedFileType, fileName)
		}
	case "doc":
		return nil, fmt.Errorf("%w: %s（.doc 为老版二进制格式，请另存为 .docx 后上传）", ErrUnsupportedFileType, fileName)
	default:
		return nil, fmt.Errorf("%w: %s（支持 md/txt/html/xlsx/pdf/docx/pptx）", ErrUnsupportedFileType, fileName)
	}
	if _, err := s.store.GetKB(ctx, kbID); err != nil {
		return nil, err
	}

	hash := hashContent(content)
	if existing, err := s.store.FindDocumentByFile(ctx, kbID, fileName); err == nil &&
		existing.Status == StatusSucceeded && existing.ContentHash == hash {
		s.log.Info("文档幂等跳过", zap.String("doc", existing.ID), zap.String("file", fileName))
		return existing, nil
	}

	doc := &Document{
		KBID:        kbID,
		FileName:    fileName,
		FileType:    fileType,
		Content:     content,
		ContentHash: hash,
	}
	if _, err := s.store.UpsertDocument(ctx, doc); err != nil {
		return nil, err
	}
	s.log.Info("文档入队摄取", zap.String("doc", doc.ID), zap.String("kb", kbID),
		zap.String("file", fileName), zap.Int("bytes", len(content)))
	return doc, nil
}

// GetDocument 查询文档（含摄取状态）。
func (s *Service) GetDocument(ctx context.Context, id string) (*Document, error) {
	return s.store.GetDocument(ctx, id)
}

func (s *Service) ListDocuments(ctx context.Context, kbID string, page, pageSize int) ([]Document, int, error) {
	return s.store.ListDocuments(ctx, kbID, page, pageSize)
}

func (s *Service) DeleteDocument(ctx context.Context, id string) error {
	if err := s.store.DeleteDocument(ctx, id); err != nil {
		return err
	}
	s.cleanupMedia(id)
	s.log.Info("文档删除", zap.String("doc", id))
	return nil
}

// ---------------------------------------------------------------------------
// 检索
// ---------------------------------------------------------------------------

// Search 混合检索：query 向量化后走向量+关键词双路召回（RRF 融合）。
//
// 阶段3·多租户隔离：agentID 非空时按资源域强制限定——
//   - kbIDs 为空 → 检索该智能体域内的全部知识库；
//   - kbIDs 非空 → 逐项校验归属，越出本域一律 ErrNotFound（不泄露存在性，
//     与 adminsvc.kbInScope 的 404 语义一致），防跨域检索泄露；
//   - agentID 为空 → 旧行为（检索全部知识库），仅供内部/本地调用，不暴露给业务方。
func (s *Service) Search(ctx context.Context, query string, kbIDs []string, topK int, minScore float64, agentID string) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: query 不能为空", ErrInvalidArgument)
	}
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}
	if agentID != "" {
		allowed, err := s.store.ListKBs(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if kbIDs, err = scopeKBIDs(kbIDs, allowed); err != nil {
			return nil, err
		}
		if len(kbIDs) == 0 {
			return nil, nil // 域内无知识库 → 空结果（非错误）
		}
	}
	vecs, err := s.emb.EmbedBatch(ctx, []string{query})
	if err != nil {
		// embedding 未配置是"功能降级"而非故障：透传领域错误 ErrNotConfigured，
		// 由 gRPC 层映射为 Unavailable，前端给出明确提示而非笼统的 500。
		if errors.Is(err, embedding.ErrNotConfigured) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("rag: 检索向量化失败: %w", err)
	}
	return s.store.Retrieve(ctx, vecs[0], query, kbIDs, topK, minScore)
}

// scopeKBIDs 按资源域裁剪检索范围（纯函数，供 Search 与单测复用）：
//   - allowed 为空 → 返回 nil（域内无知识库，无结果）；
//   - kbIDs 为空 → 返回 allowed 域内全部知识库 id（空 = 检索域内全部）；
//   - kbIDs 非空 → 逐项校验归属，越出本域一律 ErrNotFound（严格拒绝，
//     不静默过滤——便于模型感知后调整参数重试，也不泄露存在性）。
func scopeKBIDs(kbIDs []string, allowed []KnowledgeBase) ([]string, error) {
	if len(allowed) == 0 {
		return nil, nil
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, kb := range allowed {
		allowedSet[kb.ID] = struct{}{}
	}
	if len(kbIDs) > 0 {
		for _, id := range kbIDs {
			if _, ok := allowedSet[id]; !ok {
				return nil, fmt.Errorf("%w: 知识库不存在或无权访问", ErrNotFound)
			}
		}
		return kbIDs, nil
	}
	out := make([]string, 0, len(allowed))
	for _, kb := range allowed {
		out = append(out, kb.ID)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 摄取 worker
// ---------------------------------------------------------------------------

// RunIngestWorkers 启动 N 个摄取 worker 协程，阻塞直到 ctx 取消。
func (s *Service) RunIngestWorkers(ctx context.Context) {
	n := s.workers
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		go s.ingestLoop(ctx)
	}
	<-ctx.Done()
}

func (s *Service) ingestLoop(ctx context.Context) {
	interval := s.pollInterval
	if interval <= 0 {
		interval = time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			doc, err := s.store.ClaimNextQueued(ctx)
			if err != nil {
				s.log.Error("抢占摄取任务失败", zap.Error(err))
				continue
			}
			if doc == nil {
				continue
			}
			if err := s.processDocument(ctx, doc); err != nil {
				s.log.Error("文档摄取失败", zap.String("doc", doc.ID), zap.String("file", doc.FileName), zap.Error(err))
			}
		}
	}
}

// processDocument 摄取单篇文档：解析 → 分块 → 向量化 → 落库（状态机推进）。
// 错误分级：解析/空内容属永久失败（重试无意义）直接落 failed；
// 向量化/落库多为瞬时故障（embedding 上游冷启动、网络抖动、DB 瞬断），
// 自动重入队指数退避重试（上限 maxAttempts），超限才落 failed。
func (s *Service) processDocument(ctx context.Context, doc *Document) error {
	// 传文档 ID 作为沙盒媒体目录名（rag-media/<docID>/），删除文档时可按 ID 清理孤儿媒体。
	parsed, err := s.parser.Parse(doc.Content, doc.FileType, doc.ID)
	if err != nil {
		return s.failDocument(doc, err)
	}
	for _, w := range parsed.Warnings {
		s.log.Warn("文档解析警告", zap.String("doc", doc.ID), zap.String("file", doc.FileName), zap.String("warning", w))
	}
	chunks := s.chunker.Chunk(parsed, s.chunkOpts)
	if len(chunks) == 0 {
		return s.failDocument(doc, errors.New("文档无有效内容，请检查格式"))
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}
	vecs, err := s.embedTexts(ctx, doc, texts)
	if err != nil {
		return err // 分片内部已按批次处理（含自动重试/落终态）
	}

	modelChunks := make([]Chunk, 0, len(chunks))
	for i, c := range chunks {
		modelChunks = append(modelChunks, Chunk{
			KBID:      doc.KBID,
			Seq:       i,
			Content:   c.Content,
			Embedding: vecs[i],
			Metadata:  stringMapToAny(c.Metadata),
		})
	}
	if err := s.store.ReplaceChunks(ctx, doc.ID, modelChunks); err != nil {
		return s.retryOrFail(doc, fmt.Errorf("rag: 写入分块失败: %w", err))
	}
	if err := s.store.MarkDocumentDone(ctx, doc.ID, StatusSucceeded, len(modelChunks), ""); err != nil {
		return s.retryOrFail(doc, fmt.Errorf("rag: 标记完成失败: %w", err))
	}
	s.log.Info("文档摄取完成", zap.String("doc", doc.ID), zap.String("file", doc.FileName),
		zap.Int("chunks", len(modelChunks)))
	return nil
}

// embedTexts 按 batchSize 分批向量化全部文本（大文档/批量上传防单次请求超时）。
// 任一失败：embedding 未配置属永久降级直接 failed；其余瞬时错误走自动重试。
func (s *Service) embedTexts(ctx context.Context, doc *Document, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	vecs := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += s.batchSize {
		end := start + s.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batchVecs, err := s.emb.EmbedBatch(ctx, texts[start:end])
		if err != nil {
			if errors.Is(err, embedding.ErrNotConfigured) {
				return nil, s.failDocument(doc, err)
			}
			s.log.Warn("文档向量化失败，进入自动重试",
				zap.String("doc", doc.ID),
				zap.Int("batch", start/s.batchSize+1),
				zap.Error(err))
			return nil, s.retryOrFail(doc, err)
		}
		vecs = append(vecs, batchVecs...)
	}
	return vecs, nil
}

// retryOrFail 瞬时错误处理：未达重试上限则自动重入队（指数退避，排到队尾），
// 否则落 failed 终态（保留最后一次错误信息供前端完整展示）。
func (s *Service) retryOrFail(doc *Document, err error) error {
	if doc.Attempt < s.maxAttempts {
		delay := s.backoff(doc.Attempt)
		s.log.Warn("文档摄取失败，自动重试",
			zap.String("doc", doc.ID),
			zap.Int("attempt", doc.Attempt+1),
			zap.Int("max", s.maxAttempts),
			zap.Duration("retry_in", delay),
			zap.Error(err))
		if requeued, rerr := s.store.RequeueDocument(context.Background(), doc.ID, err.Error(), delay); rerr != nil {
			s.log.Error("文档重入队失败", zap.String("doc", doc.ID), zap.Error(rerr))
		} else if !requeued {
			// 文档已被其它流程处理（终态/删除），本次失败可忽略。
			s.log.Info("文档状态已变更，跳过重入队", zap.String("doc", doc.ID))
		}
		return err
	}
	s.log.Error("文档摄取失败（已达重试上限，落终态）",
		zap.String("doc", doc.ID),
		zap.String("file", doc.FileName),
		zap.Int("attempt", doc.Attempt),
		zap.Error(err))
	return s.failDocument(doc, err)
}

// backoff 返回第 attempt 次重试前的等待时长（序列越界沿用末位）。
func (s *Service) backoff(attempt int) time.Duration {
	if attempt < 0 {
		return 0
	}
	if attempt >= len(s.backoffs) {
		return s.backoffs[len(s.backoffs)-1]
	}
	return s.backoffs[attempt]
}

func (s *Service) failDocument(doc *Document, err error) error {
	_ = s.store.MarkDocumentDone(context.Background(), doc.ID, StatusFailed, 0, err.Error())
	return err
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func inferFileType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".md", ".markdown":
		return "md"
	case ".txt", ".text":
		return "txt"
	case ".html", ".htm":
		return "html"
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".doc":
		return "doc" // 老版 OLE 二进制，pandoc 不支持，由入队校验明确拒绝
	case ".xlsx":
		return "xlsx"
	case ".pptx":
		return "pptx"
	}
	return strings.TrimPrefix(ext, ".")
}

func hashContent(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func stringMapToAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
