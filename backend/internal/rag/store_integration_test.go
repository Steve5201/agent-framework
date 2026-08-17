package rag

// DB 门控集成测试：验证 store 层 SQL 正确性（迁移建表 + pgvector 类型注册 +
// hnsw/trgm 查询）。仅在设置 DB_TEST_DSN 时运行，例如：
//
//	$env:DB_TEST_DSN="postgres://postgres:密码@localhost:5432/rag?sslmode=disable"
//	go test ./internal/rag/ -run TestStore -v
//
// 说明：embedding 向量为手工构造（1024 维，多数为 0），不依赖真实 embedding
// 供应商；测试数据以 kb_integration_test_ 前缀命名，结束自动清理。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/db"
	"github.com/Steve5201/agent-backend/migrations"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DB_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 DB_TEST_DSN，跳过集成测试")
	}
	ctx := context.Background()
	if err := db.MigrateUp(ctx, dsn, "rag", migrations.FS); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	pool, err := db.Connect(ctx, dsn, db.Options{
		AfterConnect: func(ctx context.Context, conn *pgx.Conn) error {
			return pgxvec.RegisterTypes(ctx, conn)
		},
	})
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool)
}

// mkMediaDir 创建媒体目录（含一个文件），并设置文件 mtime。
func mkMediaDir(t *testing.T, dir string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(f, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// assertOrphanGone 验证无主媒体目录被删除（尽力 + 容忍环境删除怪癖）。
func assertOrphanGone(t *testing.T, dir string) {
	t.Helper()
	for i := 0; i < 3; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		_ = os.RemoveAll(dir)
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("目录 %s 删除未生效（环境怪癖，容忍）", dir)
}

// TestStoreIntegration_CleanupOrphanMedia 无主 rag-media 清理（模块三）：
// 有主目录保留（无论 mtime 多旧）；无主目录宽限期内保留、超期删除。
func TestStoreIntegration_CleanupOrphanMedia(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()

	// 建知识库 + 一条"现存文档"（绕过 embedding，直接入库）。
	kb := &KnowledgeBase{
		Name:        fmt.Sprintf("cleanup-test-%d", time.Now().UnixNano()),
		Description: "媒体清理测试",
		AgentID:     "agent-x",
	}
	if err := store.CreateKB(ctx, kb); err != nil {
		t.Fatalf("创建知识库: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteKB(ctx, kb.ID) })

	liveID := fmt.Sprintf("doc_live_%d", time.Now().UnixNano())
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO documents (id, kb_id, file_name, file_type, status) VALUES ($1,$2,'live.md','md','ready')`,
		liveID, kb.ID); err != nil {
		t.Fatalf("插入现存文档: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteDocument(ctx, liveID) })

	old := time.Now().Add(-10 * 24 * time.Hour) // 超过 TTL
	mediaRoot := t.TempDir()
	mediaBase := filepath.Join(mediaRoot, mediaDirName) // 公共只读区（P3-A8）

	// 有主目录（旧 mtime）：必须保留。
	mkMediaDir(t, filepath.Join(mediaBase, liveID), old)
	// 无主目录（doc_ghost_old）：超过 TTL → 删除。
	mkMediaDir(t, filepath.Join(mediaBase, "doc_ghost_old"), old)
	// 无主目录（doc_ghost_fresh）：宽限期内 → 保留。
	mkMediaDir(t, filepath.Join(mediaBase, "doc_ghost_fresh"), time.Now())
	// 历史遗留布局 users/<uid>/rag-media/（P3-A8 迁移前旧路径）：无主过期 → 一并清理。
	mkMediaDir(t, filepath.Join(mediaRoot, "users", "1", mediaDirName, "doc_ghost_legacy"), old)

	svc := &Service{store: store, log: zap.NewNop(), mediaRoot: mediaRoot}
	n, err := svc.CleanupOrphanMedia(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupOrphanMedia: %v", err)
	}
	if n != 2 {
		t.Fatalf("应清理 2 个无主过期目录（公共区 1 + 旧布局 1）, got %d", n)
	}
	// 有主 + 宽限期内无主：保留。
	if _, err := os.Stat(filepath.Join(mediaBase, liveID)); err != nil {
		t.Fatalf("有主目录应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mediaBase, "doc_ghost_fresh")); err != nil {
		t.Fatalf("宽限期内无主目录应保留: %v", err)
	}
	// 无主过期目录：删除（公共区 + 旧布局）。
	assertOrphanGone(t, filepath.Join(mediaBase, "doc_ghost_old"))
	assertOrphanGone(t, filepath.Join(mediaRoot, "users", "1", mediaDirName, "doc_ghost_legacy"))

	// 全库现存文档 ID 集合应包含有主文档。
	ids, err := store.ListAllDocumentIDs(ctx)
	if err != nil {
		t.Fatalf("ListAllDocumentIDs: %v", err)
	}
	if _, ok := ids[liveID]; !ok {
		t.Fatalf("现存文档 ID 应包含 %s", liveID)
	}
}

// mkVec 构造 1024 维测试向量：前 dim 个元素为给定值，其余 0。
func mkVec(dim int, vals ...float32) []float32 {
	vec := make([]float32, 1024)
	for i, v := range vals {
		if i < dim {
			vec[i] = v
		}
	}
	return vec
}

// cos 计算两个 1024 维向量的余弦相似度。
func cos(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// TestStoreIntegration_CrudAndHybrid 全链路：建库→传文档→写分块→双路检索→RRF。
func TestStoreIntegration_CrudAndHybrid(t *testing.T) {
	s := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. 知识库 CRUD。
	kb := &KnowledgeBase{Name: fmt.Sprintf("kb_integration_test_%d", time.Now().UnixNano()), Description: "集成测试库"}
	if err := s.CreateKB(ctx, kb); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteKB(context.Background(), kb.ID) })

	got, err := s.GetKB(ctx, kb.ID)
	if err != nil || got.Name != kb.Name {
		t.Fatalf("GetKB: got=%v err=%v", got, err)
	}

	// 2. 文档摄取（手工标记 succeeded，模拟 worker 完成）。
	doc := &Document{
		KBID: kb.ID, FileName: "go-rag.md", FileType: "md",
		Content: []byte("# Go RAG\nRAG 是检索增强生成。"), ContentHash: "test-hash",
	}
	created, err := s.UpsertDocument(ctx, doc)
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteDocument(context.Background(), doc.ID) })
	if !created {
		t.Fatal("新文档应标记为 created")
	}
	if err := s.MarkDocumentDone(ctx, doc.ID, StatusSucceeded, 1, ""); err != nil {
		t.Fatalf("MarkDocumentDone: %v", err)
	}

	// 3. 写分块：三块语义明确区分（向量前 4 维编码）。
	chunks := []Chunk{
		{ID: "chunk_retrieval", DocID: doc.ID, KBID: kb.ID, Seq: 0,
			Content: "检索增强生成 RAG 把向量检索和语言模型结合", Embedding: mkVec(4, 1, 0, 0, 0),
			Metadata: map[string]any{"source": "go-rag.md"}},
		{ID: "chunk_embedding", DocID: doc.ID, KBID: kb.ID, Seq: 1,
			Content: "Embedding 模型把文本编码为向量表示", Embedding: mkVec(4, 0, 1, 0, 0),
			Metadata: map[string]any{"source": "go-rag.md"}},
		{ID: "chunk_prompt", DocID: doc.ID, KBID: kb.ID, Seq: 2,
			Content: "提示工程是 Prompt Engineering 的中文翻译", Embedding: mkVec(4, 0, 0, 1, 0),
			Metadata: map[string]any{"source": "go-rag.md"}},
	}
	if err := s.ReplaceChunks(ctx, doc.ID, chunks); err != nil {
		t.Fatalf("ReplaceChunks: %v", err)
	}
	// 重复写应替换而非追加（同一 doc 仅 3 块）。
	if err := s.ReplaceChunks(ctx, doc.ID, chunks); err != nil {
		t.Fatalf("ReplaceChunks(2nd): %v", err)
	}
	var cnt int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM chunks WHERE doc_id=$1", doc.ID).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 3 {
		t.Fatalf("ReplaceChunks 应保持 3 块，实际 %d", cnt)
	}

	// 4. 向量检索：query=[1,0,0,0] 应命中 chunk_retrieval。
	vecHits, err := s.VectorSearch(ctx, mkVec(4, 1, 0, 0, 0), []string{kb.ID}, 3, 0.5)
	if err != nil {
		t.Fatalf("VectorSearch: %v", err)
	}
	if len(vecHits) == 0 || vecHits[0].ChunkID != "chunk_retrieval" {
		t.Fatalf("向量检索首命中应为 chunk_retrieval，实际 %+v", vecHits)
	}
	if vecHits[0].KBName != kb.Name || vecHits[0].Source != "go-rag.md" {
		t.Errorf("引用溯源字段缺失: KBName=%q Source=%q", vecHits[0].KBName, vecHits[0].Source)
	}
	// 相似度应与手工 cos 计算一致（误差 1e-6）。
	wantScore := cos(mkVec(4, 1, 0, 0, 0), mkVec(4, 1, 0, 0, 0))
	if math.Abs(vecHits[0].Score-wantScore) > 1e-6 {
		t.Errorf("向量分数 %f 与理论值 %f 偏差过大", vecHits[0].Score, wantScore)
	}

	// 5. 关键词检索：pg_trgm 命中含"提示工程"的块。
	kwHits, err := s.KeywordSearch(ctx, "提示工程", []string{kb.ID}, 3, 0.05)
	if err != nil {
		t.Fatalf("KeywordSearch: %v", err)
	}
	if len(kwHits) == 0 || kwHits[0].ChunkID != "chunk_prompt" {
		t.Fatalf("关键词检索首命中应为 chunk_prompt，实际 %+v", kwHits)
	}

	// 6. 混合检索（RRF）：query=[1,0,0,0] 向量优先，融合分非空且排序稳定。
	hybrid, err := s.Retrieve(ctx, mkVec(4, 1, 0, 0, 0), "检索增强生成", []string{kb.ID}, 3, 0)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(hybrid) != 3 {
		t.Fatalf("混合检索应返回 3 条，实际 %d", len(hybrid))
	}
	if hybrid[0].ChunkID != "chunk_retrieval" {
		t.Errorf("混合检索首命中应为 chunk_retrieval，实际 %s", hybrid[0].ChunkID)
	}
	if hybrid[0].Score <= 0 {
		t.Errorf("融合分应为正，实际 %f", hybrid[0].Score)
	}

	// 7. 知识库过滤：不存在的 kb 返回空。
	none, err := s.VectorSearch(ctx, mkVec(4, 1, 0, 0, 0), []string{"kb_不存在"}, 3, 0)
	if err != nil || len(none) != 0 {
		t.Fatalf("不存在知识库应返回空: len=%d err=%v", len(none), err)
	}

	// 8. 幂等抢单：已 succeeded 的文档不应再被抢占。
	claimed, err := s.ClaimNextQueued(ctx)
	if err != nil {
		t.Fatalf("ClaimNextQueued: %v", err)
	}
	if claimed != nil {
		t.Fatalf("不应有 queued 任务可抢占，实际 %+v", claimed)
	}
}

// failingEmbed 恒失败的 embedding（模拟 embedding 上游瞬时不可用）。
type failingEmbed struct{}

func (failingEmbed) Name() string { return "failing" }
func (failingEmbed) Dim() int     { return 4 }
func (failingEmbed) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embedding: 上游不可用（测试模拟瞬时故障）")
}

// TestStoreIntegration_IngestRetry 摄取失败自动重试全链路（DB 门控）：
// embedding 恒失败 → 重入队（attempt+1 + retry_at 退避）→ 退避期内不可抢占 →
// 到期后再失败 → 达上限（2）落 failed 终态。
func TestStoreIntegration_IngestRetry(t *testing.T) {
	s := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	kb := &KnowledgeBase{Name: fmt.Sprintf("kb_integration_test_%d", time.Now().UnixNano()), Description: "重试集成测试"}
	if err := s.CreateKB(ctx, kb); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteKB(context.Background(), kb.ID) })

	svc := NewService(s, failingEmbed{}, config.RAGConfig{
		EmbeddingBatchSize: 16,
		IngestMaxAttempts:  2, // 最多自动重试 2 次，第 3 次失败落 failed
		IngestWorkers:      1,
		IngestPollInterval: time.Second,
	}, zap.NewNop())

	doc := &Document{
		KBID: kb.ID, FileName: "retry.md", FileType: "md",
		Content: []byte("# 重试测试\n正文内容用于触发摄取管线"),
	}
	if _, err := s.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteDocument(context.Background(), doc.ID) })

	// 读取文档快照（含 attempt 列），遍历 processDocument 到 failed。
	load := func() *Document {
		d, err := s.GetDocument(ctx, doc.ID)
		if err != nil {
			t.Fatalf("GetDocument: %v", err)
		}
		return d
	}

	// 第 1 轮：抢占 → 摄取失败（embedding 恒败）→ 自动重入队。
	claimed, err := s.ClaimNextQueued(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("第 1 轮应抢占到任务: claimed=%v err=%v", claimed, err)
	}
	if claimed.Attempt != 0 {
		t.Fatalf("首次抢占 attempt 应为 0，实际 %d", claimed.Attempt)
	}
	if err := svc.processDocument(ctx, claimed); err == nil {
		t.Fatal("processDocument 应返回错误（embedding 失败）")
	}
	after1 := load()
	if after1.Status != StatusQueued || after1.Attempt != 1 {
		t.Fatalf("第 1 轮后应 queued/attempt=1，实际 status=%s attempt=%d", after1.Status, after1.Attempt)
	}
	if after1.Error == "" {
		t.Fatal("重入队应保留错误信息")
	}

	// 退避期未到：不可抢占（retry_at 在未来）。
	if again, err := s.ClaimNextQueued(ctx); err != nil || again != nil {
		t.Fatalf("退避期内不应抢占到任务: claimed=%v err=%v", again, err)
	}

	// 手动将 retry_at 拨到过去，模拟退避到期。
	if _, err := s.pool.Exec(ctx,
		`UPDATE documents SET retry_at = now() - interval '1 second' WHERE id = $1`, doc.ID); err != nil {
		t.Fatalf("拨快 retry_at: %v", err)
	}

	// 第 2 轮：到期后抢占 → 再失败 → 重入队（attempt=2）。
	claimed2, err := s.ClaimNextQueued(ctx)
	if err != nil || claimed2 == nil {
		t.Fatalf("第 2 轮应抢占到任务: claimed=%v err=%v", claimed2, err)
	}
	if claimed2.Attempt != 1 {
		t.Fatalf("第 2 轮抢占 attempt 应为 1，实际 %d", claimed2.Attempt)
	}
	if err := svc.processDocument(ctx, claimed2); err == nil {
		t.Fatal("第 2 轮 processDocument 应返回错误")
	}
	after2 := load()
	if after2.Status != StatusQueued || after2.Attempt != 2 {
		t.Fatalf("第 2 轮后应 queued/attempt=2，实际 status=%s attempt=%d", after2.Status, after2.Attempt)
	}

	// 拨快 retry_at，第 3 轮：达上限 → 落 failed 终态。
	if _, err := s.pool.Exec(ctx,
		`UPDATE documents SET retry_at = now() - interval '1 second' WHERE id = $1`, doc.ID); err != nil {
		t.Fatalf("拨快 retry_at(3): %v", err)
	}
	claimed3, err := s.ClaimNextQueued(ctx)
	if err != nil || claimed3 == nil {
		t.Fatalf("第 3 轮应抢占到任务: claimed=%v err=%v", claimed3, err)
	}
	if err := svc.processDocument(ctx, claimed3); err == nil {
		t.Fatal("第 3 轮 processDocument 应返回错误")
	}
	final := load()
	if final.Status != StatusFailed {
		t.Fatalf("达上限后应落 failed，实际 status=%s attempt=%d", final.Status, final.Attempt)
	}
	if final.Error == "" || final.ChunkCount != 0 {
		t.Fatalf("failed 终态应保留错误且 chunk_count=0，实际 error=%q chunks=%d", final.Error, final.ChunkCount)
	}

	// 终态后不再被抢占。
	if again, err := s.ClaimNextQueued(ctx); err != nil || again != nil {
		t.Fatalf("failed 终态不应被抢占: claimed=%v err=%v", again, err)
	}
}

// TestStoreIntegration_UpdateKB 知识库更新：改名/描述生效、重名冲突、不存在。
func TestStoreIntegration_UpdateKB(t *testing.T) {
	s := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	kb := &KnowledgeBase{Name: fmt.Sprintf("kb_integration_test_%d", time.Now().UnixNano()), Description: "初始描述"}
	if err := s.CreateKB(ctx, kb); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteKB(context.Background(), kb.ID) })

	updated, err := s.UpdateKB(ctx, kb.ID, "新名称", "新描述", nil)
	if err != nil {
		t.Fatalf("UpdateKB: %v", err)
	}
	if updated.Name != "新名称" || updated.Description != "新描述" {
		t.Fatalf("更新结果不符: %+v", updated)
	}
	if !updated.Enabled {
		t.Fatalf("新建知识库应默认启用: %+v", updated)
	}

	// 停用 → enabled=false 落库回读。
	disabled := false
	if updated, err = s.UpdateKB(ctx, kb.ID, "新名称", "新描述", &disabled); err != nil {
		t.Fatalf("UpdateKB(disabled): %v", err)
	}
	if updated.Enabled {
		t.Fatalf("停用后 enabled 应为 false: %+v", updated)
	}

	// 重名冲突 → ErrNameExists。
	other := &KnowledgeBase{Name: fmt.Sprintf("kb_integration_test_%d", time.Now().UnixNano())}
	if err := s.CreateKB(ctx, other); err != nil {
		t.Fatalf("CreateKB(other): %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteKB(context.Background(), other.ID) })
	if _, err := s.UpdateKB(ctx, other.ID, kb.Name, "", nil); err != ErrNameExists {
		t.Fatalf("重名应 ErrNameExists，实际 %v", err)
	}
	// 不存在 → ErrNotFound。
	if _, err := s.UpdateKB(ctx, "no_such_kb", "x", "", nil); err != ErrNotFound {
		t.Fatalf("不存在应 ErrNotFound，实际 %v", err)
	}
}

// TestStoreIntegration_ResetDocumentForRetry 手动重试重置：failed → queued、
// attempt/error/retry_at 清零且立即可抢占；processing 或不存在不生效。
func TestStoreIntegration_ResetDocumentForRetry(t *testing.T) {
	s := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	kb := &KnowledgeBase{Name: fmt.Sprintf("kb_integration_test_%d", time.Now().UnixNano())}
	if err := s.CreateKB(ctx, kb); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteKB(context.Background(), kb.ID) })

	doc := &Document{KBID: kb.ID, FileName: "broken.pdf", FileType: "pdf", Content: []byte("x")}
	if _, err := s.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteDocument(context.Background(), doc.ID) })
	// 拨到 failed 终态（模拟自动重试耗尽）。
	if _, err := s.pool.Exec(ctx,
		`UPDATE documents SET status='failed', error='模拟失败', attempt=3, retry_at = now() + interval '1 day' WHERE id = $1`, doc.ID); err != nil {
		t.Fatalf("置 failed: %v", err)
	}

	ok, err := s.ResetDocumentForRetry(ctx, doc.ID)
	if err != nil || !ok {
		t.Fatalf("failed 应可重置: ok=%v err=%v", ok, err)
	}
	after, err := s.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if after.Status != StatusQueued || after.Attempt != 0 || after.Error != "" {
		t.Fatalf("重置后应 queued/attempt=0/error 空，实际 status=%s attempt=%d error=%q",
			after.Status, after.Attempt, after.Error)
	}
	// 退避已清空 → 立即可抢占。
	claimed, err := s.ClaimNextQueued(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("重置后应立即可抢占: claimed=%v err=%v", claimed, err)
	}
	// processing 状态不可重置。
	ok2, err := s.ResetDocumentForRetry(ctx, claimed.ID)
	if err != nil || ok2 {
		t.Fatalf("processing 状态不应可重置: ok=%v err=%v", ok2, err)
	}
	// 不存在 → false（Service 层先查存在性再返回 ErrNotFound）。
	ok3, err := s.ResetDocumentForRetry(ctx, "no_such_doc")
	if err != nil || ok3 {
		t.Fatalf("不存在应返回 false: ok=%v err=%v", ok3, err)
	}
}

// TestStoreIntegration_RequeueCondition 重入队条件更新：仅 processing 状态生效。
// 文档已终态（如手动删除或成功）时重入队不复活、不改变状态。
func TestStoreIntegration_RequeueCondition(t *testing.T) {
	s := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	kb := &KnowledgeBase{Name: fmt.Sprintf("kb_integration_test_%d", time.Now().UnixNano()), Description: "重入队条件测试"}
	if err := s.CreateKB(ctx, kb); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteKB(context.Background(), kb.ID) })

	doc := &Document{KBID: kb.ID, FileName: "cond.md", FileType: "md", Content: []byte("x")}
	if _, err := s.UpsertDocument(ctx, doc); err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteDocument(context.Background(), doc.ID) })

	// 文档仍为 queued（未抢占）：重入队不应生效（WHERE status='processing'）。
	requeued, err := s.RequeueDocument(ctx, doc.ID, "测试", time.Second)
	if err != nil {
		t.Fatalf("RequeueDocument: %v", err)
	}
	if requeued {
		t.Fatal("非 processing 状态不应重入队成功")
	}
	got, _ := s.GetDocument(ctx, doc.ID)
	if got.Status != StatusQueued || got.Attempt != 0 {
		t.Fatalf("queued 文档状态不应被重入队改变，实际 status=%s attempt=%d", got.Status, got.Attempt)
	}

	// 抢占后重入队生效（attempt+1）。
	claimed, err := s.ClaimNextQueued(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("应抢占到任务: claimed=%v err=%v", claimed, err)
	}
	requeued, err = s.RequeueDocument(ctx, doc.ID, "测试", 10*time.Second)
	if err != nil || !requeued {
		t.Fatalf("processing 状态应重入队成功: requeued=%v err=%v", requeued, err)
	}
	got, _ = s.GetDocument(ctx, doc.ID)
	if got.Status != StatusQueued || got.Attempt != 1 {
		t.Fatalf("重入队后应 queued/attempt=1，实际 status=%s attempt=%d", got.Status, got.Attempt)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Error("重入队应更新 updated_at")
	}
}
