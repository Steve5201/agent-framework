package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// Store RAG 数据仓储（pgx 实现）：知识库 / 文档 / 分块 三层 CRUD + 检索基础查询。
// 所有写操作并发安全由数据库约束保证（唯一索引 + 行级锁 + SKIP LOCKED 抢单）。
type Store struct {
	pool *pgxpool.Pool
}

// NewStore 创建 RAG 仓储。调用方需先 pgvector.RegisterTypes（cmd/rag main 中执行）。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ---------------------------------------------------------------------------
// 知识库
// ---------------------------------------------------------------------------

// CreateKB 创建知识库。同名冲突返回 ErrNameExists。新库默认启用。
func (s *Store) CreateKB(ctx context.Context, kb *KnowledgeBase) error {
	kb.ID = genID("kb")
	kb.Enabled = true
	_, err := s.pool.Exec(ctx,
		`INSERT INTO knowledge_bases (id, name, description, agent_id, enabled) VALUES ($1, $2, $3, $4, true)`,
		kb.ID, kb.Name, kb.Description, kb.AgentID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrNameExists
		}
		return fmt.Errorf("rag: 创建知识库失败: %w", err)
	}
	now := time.Now()
	kb.CreatedAt, kb.UpdatedAt = now, now
	return nil
}

// ListKBs 列出知识库；agentID 空 = 全部，非空 = 仅该智能体域（阶段3·多租户过滤）。
// doc_count 用实时子查询（COUNT documents），不信任存储列——存储列创建后
// 从未随文档增删同步，导致列表数字与详情不符（P3-A8）。
func (s *Store) ListKBs(ctx context.Context, agentID string) ([]KnowledgeBase, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, agent_id, enabled,
		        (SELECT COUNT(*) FROM documents WHERE kb_id = knowledge_bases.id) AS doc_count,
		        created_at, updated_at
		 FROM knowledge_bases
		 WHERE ($1 = '' OR agent_id = $1)
		 ORDER BY created_at DESC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("rag: 查询知识库失败: %w", err)
	}
	defer rows.Close()
	out := make([]KnowledgeBase, 0, 8)
	for rows.Next() {
		var kb KnowledgeBase
		if err := rows.Scan(&kb.ID, &kb.Name, &kb.Description, &kb.AgentID, &kb.Enabled, &kb.DocCount,
			&kb.CreatedAt, &kb.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, rows.Err()
}

// GetKB 获取单个知识库；不存在返回 ErrNotFound。
// doc_count 同样用实时子查询（与 ListKBs 一致，P3-A8）。
func (s *Store) GetKB(ctx context.Context, id string) (*KnowledgeBase, error) {
	var kb KnowledgeBase
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, agent_id, enabled,
		        (SELECT COUNT(*) FROM documents WHERE kb_id = knowledge_bases.id) AS doc_count,
		        created_at, updated_at
		 FROM knowledge_bases WHERE id = $1`, id).
		Scan(&kb.ID, &kb.Name, &kb.Description, &kb.AgentID, &kb.Enabled, &kb.DocCount, &kb.CreatedAt, &kb.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("rag: 查询知识库失败: %w", err)
	}
	return &kb, nil
}

// UpdateKB 更新知识库名称/描述/启用状态；不存在返回 ErrNotFound，重名返回 ErrNameExists。
// enabled 为 nil = 不修改启用状态（旧客户端只改名/描述时不会误停用知识库）。
// 返回更新后的完整知识库（含最新 doc_count）。
func (s *Store) UpdateKB(ctx context.Context, id, name, desc string, enabled *bool) (*KnowledgeBase, error) {
	var tag pgconn.CommandTag
	var err error
	if enabled != nil {
		tag, err = s.pool.Exec(ctx,
			`UPDATE knowledge_bases SET name = $2, description = $3, enabled = $4, updated_at = now() WHERE id = $1`,
			id, name, desc, *enabled)
	} else {
		tag, err = s.pool.Exec(ctx,
			`UPDATE knowledge_bases SET name = $2, description = $3, updated_at = now() WHERE id = $1`,
			id, name, desc)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrNameExists
		}
		return nil, fmt.Errorf("rag: 更新知识库失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetKB(ctx, id)
}

// DeleteKB 删除知识库（ON DELETE CASCADE 连带文档与 chunk）。
func (s *Store) DeleteKB(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("rag: 删除知识库失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 文档
// ---------------------------------------------------------------------------

// UpsertDocument 按 (kb_id, file_name) 幂等写入文档：同文件存在则更新
// content/hash/状态重置为 queued（含 attempt/退避清零，即手动重试通道）；
// 返回是否为新创建。
func (s *Store) UpsertDocument(ctx context.Context, doc *Document) (bool, error) {
	if doc.ID == "" {
		doc.ID = genID("doc")
	}
	doc.Status = StatusQueued
	doc.Error = ""
	doc.Attempt = 0
	now := time.Now()
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO documents (id, kb_id, file_name, file_type, content, status, content_hash, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (kb_id, file_name) DO UPDATE SET
		   content = EXCLUDED.content, content_hash = EXCLUDED.content_hash,
		   status = 'queued', error = '', file_type = EXCLUDED.file_type,
		   chunk_count = 0, attempt = 0, retry_at = NULL, queued_at = now(),
		   updated_at = EXCLUDED.updated_at`,
		doc.ID, doc.KBID, doc.FileName, doc.FileType, doc.Content, doc.Status, doc.ContentHash, now, now)
	if err != nil {
		return false, fmt.Errorf("rag: 写入文档失败: %w", err)
	}
	doc.CreatedAt, doc.UpdatedAt = now, now
	return tag.RowsAffected() == 1, nil
}

// FindDocumentByFile 按 (kb_id, file_name) 查找文档；不存在返回 ErrNotFound。
func (s *Store) FindDocumentByFile(ctx context.Context, kbID, fileName string) (*Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx,
		`SELECT id, kb_id, file_name, file_type, status, content_hash, chunk_count, error, created_at, updated_at
		 FROM documents WHERE kb_id = $1 AND file_name = $2`, kbID, fileName).
		Scan(&d.ID, &d.KBID, &d.FileName, &d.FileType, &d.Status, &d.ContentHash,
			&d.ChunkCount, &d.Error, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("rag: 查询文档失败: %w", err)
	}
	return &d, nil
}

// GetDocument 获取文档（含原始内容）；不存在返回 ErrNotFound。
func (s *Store) GetDocument(ctx context.Context, id string) (*Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx,
		`SELECT id, kb_id, file_name, file_type, content, status, content_hash, chunk_count, error, created_at, updated_at
		 FROM documents WHERE id = $1`, id).
		Scan(&d.ID, &d.KBID, &d.FileName, &d.FileType, &d.Content, &d.Status, &d.ContentHash,
			&d.ChunkCount, &d.Error, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("rag: 查询文档失败: %w", err)
	}
	return &d, nil
}

// ResetDocumentForRetry 手动重试摄取：将非 processing 状态的文档重置为
// queued（attempt 清零、错误清空、退避到期时间清空、排到队尾）。
// 返回是否生效（行数=1）；处理中或不存在返回 false。
func (s *Store) ResetDocumentForRetry(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE documents
		 SET status = 'queued', attempt = 0, error = '', retry_at = NULL, queued_at = now(), updated_at = now()
		 WHERE id = $1 AND status <> 'processing'`, id)
	if err != nil {
		return false, fmt.Errorf("rag: 文档重试失败: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ListDocuments 分页列出知识库文档（创建时间倒序），返回列表与总数。
func (s *Store) ListDocuments(ctx context.Context, kbID string, page, pageSize int) ([]Document, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM documents WHERE kb_id = $1`, kbID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, kb_id, file_name, file_type, status, content_hash, chunk_count, error, created_at, updated_at
		 FROM documents WHERE kb_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		kbID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Document, 0, pageSize)
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.KBID, &d.FileName, &d.FileType, &d.Status, &d.ContentHash,
			&d.ChunkCount, &d.Error, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// ListDocumentIDs 返回知识库全部文档 ID（级联删除时清理媒体文件用）。
func (s *Store) ListDocumentIDs(ctx context.Context, kbID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM documents WHERE kb_id = $1`, kbID)
	if err != nil {
		return nil, fmt.Errorf("rag: 查询知识库文档失败: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListAllDocumentIDs 返回全库现存文档 ID 集合（跨知识库、含各状态）。
// 无主 rag-media 目录清理（模块三）用：媒体目录名 = docID，不在集合中即无主。
func (s *Store) ListAllDocumentIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM documents`)
	if err != nil {
		return nil, fmt.Errorf("rag: 查询全部文档失败: %w", err)
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

// DeleteDocument 删除文档（CASCADE 连带 chunk）。
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("rag: 删除文档失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimNextQueued 原子抢占一条待处理文档（FOR UPDATE SKIP LOCKED）：
// queued → processing，避免多 worker 重复处理。
// 仅领取"已到重试时间"的任务（retry_at IS NULL OR <= now()），并按 queued_at
// 公平排队（失败重入队的任务排到队尾，不插队饿死新上传文档）。无任务返回 nil。
func (s *Store) ClaimNextQueued(ctx context.Context) (*Document, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var d Document
	err = tx.QueryRow(ctx,
		`SELECT id, kb_id, file_name, file_type, content, content_hash, attempt
		 FROM documents
		 WHERE status = 'queued' AND (retry_at IS NULL OR retry_at <= now())
		 ORDER BY queued_at
		 LIMIT 1 FOR UPDATE SKIP LOCKED`).
		Scan(&d.ID, &d.KBID, &d.FileName, &d.FileType, &d.Content, &d.ContentHash, &d.Attempt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rag: 抢占任务失败: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE documents SET status = 'processing', updated_at = now() WHERE id = $1`, d.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	d.Status = StatusProcessing
	return &d, nil
}

// MarkDocumentDone 更新文档摄取终态（succeeded/failed）与 chunk 数。
func (s *Store) MarkDocumentDone(ctx context.Context, id, status string, chunkCount int, errMsg string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE documents SET status = $2, chunk_count = $3, error = $4, updated_at = now() WHERE id = $1`,
		id, status, chunkCount, errMsg)
	return err
}

// RequeueDocument 摄取失败自动重入队：processing → queued，attempt+1，
// 记录错误与退避到期时间（retry_at），并更新 queued_at 排到队尾。
// 仅当文档仍处于 processing 时生效（条件更新），避免与其它 worker 的
// 终态写入/删除竞争产生脏状态；受影响行数为 0 表示文档已非 processing。
func (s *Store) RequeueDocument(ctx context.Context, id, errMsg string, delay time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE documents
		 SET status = 'queued', attempt = attempt + 1, error = $2,
		     retry_at = now() + ($3 * interval '1 second'),
		     queued_at = now(), updated_at = now()
		 WHERE id = $1 AND status = 'processing'`,
		id, errMsg, int(delay.Seconds()))
	if err != nil {
		return false, fmt.Errorf("rag: 文档重入队失败: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ---------------------------------------------------------------------------
// 分块
// ---------------------------------------------------------------------------

// ReplaceChunks 删除文档旧 chunk 并批量插入新 chunk（同一事务）。
func (s *Store) ReplaceChunks(ctx context.Context, docID string, chunks []Chunk) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE doc_id = $1`, docID); err != nil {
		return err
	}
	if len(chunks) > 0 {
		b := &pgx.Batch{}
		for _, c := range chunks {
			if c.ID == "" {
				c.ID = genID("chunk")
			}
			meta, _ := json.Marshal(c.Metadata)
			b.Queue(`INSERT INTO chunks (id, doc_id, kb_id, seq, content, embedding, metadata)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				c.ID, docID, c.KBID, c.Seq, c.Content, pgvector.NewVector(c.Embedding), meta)
		}
		br := tx.SendBatch(ctx, b)
		for range chunks {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// 检索
// ---------------------------------------------------------------------------

// VectorSearch 向量召回（余弦相似度，hnsw 索引），返回 topK 命中。
// kbIDs 为 nil/空 = 全部知识库；minScore 为相似度下限（0~1）。
func (s *Store) VectorSearch(ctx context.Context, queryVec []float32, kbIDs []string, topK int, minScore float64) ([]SearchHit, error) {
	if len(kbIDs) == 0 {
		kbIDs = nil // 传 NULL = 不过滤
	}
	if minScore < 0 {
		minScore = 0
	}
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.doc_id, c.kb_id, k.name, c.content, c.metadata,
		        1 - (c.embedding <=> $1) AS score
		 FROM chunks c JOIN knowledge_bases k ON k.id = c.kb_id
		 WHERE ($2::text[] IS NULL OR c.kb_id = ANY($2))
		   AND k.enabled = true
		   AND 1 - (c.embedding <=> $1) >= $3
		 ORDER BY c.embedding <=> $1
		 LIMIT $4`,
		pgvector.NewVector(queryVec), kbIDs, minScore, topK)
	if err != nil {
		return nil, fmt.Errorf("rag: 向量检索失败: %w", err)
	}
	defer rows.Close()
	return scanHits(rows)
}

// KeywordSearch 关键词召回（pg_trgm 相似度，GIN 索引），返回 topK 命中。
func (s *Store) KeywordSearch(ctx context.Context, query string, kbIDs []string, topK int, minScore float64) ([]SearchHit, error) {
	if len(kbIDs) == 0 {
		kbIDs = nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.doc_id, c.kb_id, k.name, c.content, c.metadata,
		        similarity(c.content, $1) AS score
		 FROM chunks c JOIN knowledge_bases k ON k.id = c.kb_id
		 WHERE ($2::text[] IS NULL OR c.kb_id = ANY($2))
		   AND k.enabled = true
		   AND similarity(c.content, $1) >= $3
		 ORDER BY similarity(c.content, $1) DESC
		 LIMIT $4`,
		query, kbIDs, minScore, topK)
	if err != nil {
		return nil, fmt.Errorf("rag: 关键词检索失败: %w", err)
	}
	defer rows.Close()
	return scanHits(rows)
}

func scanHits(rows pgx.Rows) ([]SearchHit, error) {
	out := make([]SearchHit, 0, 16)
	for rows.Next() {
		var h SearchHit
		var meta []byte
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.KBID, &h.KBName, &h.Content, &meta, &h.Score); err != nil {
			return nil, err
		}
		h.Metadata = map[string]any{}
		_ = json.Unmarshal(meta, &h.Metadata)
		if src, ok := h.Metadata["source"].(string); ok {
			h.Source = src
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505") // PG unique_violation
}

// Source 从文档构造引用溯源字符串（文件名，可含页码/章节，由分块元数据补充）。
func Source(fileName string, extra ...string) string {
	if len(extra) == 0 {
		return fileName
	}
	return fileName + " · " + strings.Join(extra, " · ")
}
