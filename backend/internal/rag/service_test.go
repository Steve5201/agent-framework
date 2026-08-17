package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/rag/embedding"
)

// fakeEmbedding 最小可用的 Provider 实现（测试用）：
// 把文本编码为定长向量（字符哈希），Dim 固定。
type fakeEmbedding struct {
	dim int
}

func (f *fakeEmbedding) Name() string { return "fake" }
func (f *fakeEmbedding) Dim() int     { return f.dim }
func (f *fakeEmbedding) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, f.dim)
		for j := 0; j < len(t) && j < f.dim; j++ {
			vec[j] = float32(t[j]) / 256
		}
		out[i] = vec
	}
	return out, nil
}

func newTestService() *Service {
	return NewService(NewStore(nil), &fakeEmbedding{dim: 4}, config.RAGConfig{
		IngestWorkers:      1,
		IngestPollInterval: time.Second,
	}, zap.NewNop())
}

// TestService_CreateKB_Validation 知识库名校验（参数层，不触库）。
func TestService_CreateKB_Validation(t *testing.T) {
	svc := newTestService()
	tests := []struct {
		name string
		desc string
	}{
		{"", "空名"},
		{"   ", "空白名"},
		{strings.Repeat("名", 51), "超长名"},
		{"合法名", strings.Repeat("长", 201)}, // 描述超长
	}
	for _, tc := range tests {
		if _, err := svc.CreateKB(context.Background(), tc.name, tc.desc, ""); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("CreateKB(%q, len(desc)=%d) 应返回 ErrInvalidArgument，实际 %v",
				tc.name, len([]rune(tc.desc)), err)
		}
	}
}

// TestService_UpdateKB_Validation 知识库更新参数校验（参数层，不触库）。
func TestService_UpdateKB_Validation(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	tests := []struct {
		name string
		desc string
	}{
		{"", "空名"},
		{"   ", "空白名"},
		{strings.Repeat("名", 51), "超长名"},
		{"合法名", strings.Repeat("长", 201)}, // 描述超长
	}
	for _, tc := range tests {
		if _, err := svc.UpdateKB(ctx, "kb-1", tc.name, tc.desc, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("UpdateKB(kb-1, %q, len(desc)=%d) 应返回 ErrInvalidArgument，实际 %v",
				tc.name, len([]rune(tc.desc)), err)
		}
	}
}

// TestService_CreateKB_DefaultAgent 未显式指定 agent → 兜底 DefaultAgentID（与迁移 000002 存量兜底一致）。
func TestService_CreateKB_DefaultAgent(t *testing.T) {
	if DefaultAgentID != "tutor" {
		t.Fatalf("DefaultAgentID = %q，期望 tutor", DefaultAgentID)
	}
}

// TestService_UpsertDocument_Validation 文档参数校验（不触库）。
func TestService_UpsertDocument_Validation(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	tests := []struct {
		kbID, fileName string
		content        []byte
	}{
		{"", "a.md", []byte("x")},     // 缺 kb
		{"kb1", "", []byte("x")},      // 缺文件名
		{"kb1", "a.md", nil},          // 空内容
		{"kb1", "a.pdf", []byte("x")}, // 一期不支持的格式
		{"kb1", "a.docx", []byte("x")},
	}
	for _, tc := range tests {
		if _, err := svc.UpsertDocument(ctx, tc.kbID, tc.fileName, tc.content); err == nil {
			t.Errorf("UpsertDocument(%q, %q, %dB) 应报错", tc.kbID, tc.fileName, len(tc.content))
		}
	}
}

// TestService_Search_EmptyQuery 空 query 直接拒绝（不调用 embedding/store）。
func TestService_Search_EmptyQuery(t *testing.T) {
	svc := newTestService()
	if _, err := svc.Search(context.Background(), "  ", nil, 5, 0, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("空 query 应返回 ErrInvalidArgument，实际 %v", err)
	}
}

// TestScopeKBIDs 资源域裁剪（阶段3·多租户检索隔离）：
// 域内空库/显式全部/越界拒绝三态。
func TestScopeKBIDs(t *testing.T) {
	allowed := []KnowledgeBase{
		{ID: "kb_a", AgentID: "tutor"},
		{ID: "kb_b", AgentID: "tutor"},
	}
	t.Run("allowed 为空 → nil（域内无知识库）", func(t *testing.T) {
		got, err := scopeKBIDs(nil, nil)
		if err != nil || got != nil {
			t.Fatalf("scopeKBIDs(nil,nil) = %v, %v；want nil, nil", got, err)
		}
	})
	t.Run("kbIDs 为空 → 返回域内全部", func(t *testing.T) {
		got, err := scopeKBIDs(nil, allowed)
		if err != nil {
			t.Fatalf("意外错误: %v", err)
		}
		if len(got) != 2 || !contains(got, "kb_a") || !contains(got, "kb_b") {
			t.Fatalf("kbIDs 空 → 应返回域内全部，实际 %v", got)
		}
	})
	t.Run("kbIDs 全在域内 → 原样返回", func(t *testing.T) {
		got, err := scopeKBIDs([]string{"kb_a"}, allowed)
		if err != nil || len(got) != 1 || got[0] != "kb_a" {
			t.Fatalf("scopeKBIDs([kb_a]) = %v, %v；want [kb_a], nil", got, err)
		}
	})
	t.Run("kbIDs 越出本域 → ErrNotFound", func(t *testing.T) {
		if _, err := scopeKBIDs([]string{"kb_a", "kb_other"}, allowed); !errors.Is(err, ErrNotFound) {
			t.Fatalf("越界 kb_id 应返回 ErrNotFound，实际 %v", err)
		}
	})
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// TestInferFileType 文件类型推断（含不支持类型）。
func TestInferFileType(t *testing.T) {
	tests := map[string]string{
		"a.md": "md", "A.MD": "md", "b.markdown": "md",
		"c.txt": "txt", "d.text": "txt",
		"e.html": "html", "f.htm": "html",
		"g.pdf": "pdf", "h.docx": "docx", "i.DOC": "doc", "j.xlsx": "xlsx", "k.pptx": "pptx",
		"noext": "", "x.xyz": "xyz",
	}
	for in, want := range tests {
		if got := inferFileType(in); got != want {
			t.Errorf("inferFileType(%q) = %q，want %q", in, got, want)
		}
	}
}

// TestHashContent 内容哈希稳定且区分内容。
func TestHashContent(t *testing.T) {
	h1 := hashContent([]byte("hello"))
	h2 := hashContent([]byte("hello"))
	h3 := hashContent([]byte("hellp"))
	if h1 != h2 {
		t.Error("同内容哈希应一致")
	}
	if h1 == h3 {
		t.Error("不同内容哈希应不同")
	}
	if len(h1) != 64 {
		t.Errorf("sha256 hex 应为 64 字符，实际 %d", len(h1))
	}
}

// TestEmbeddingProvider_Interface 编译期断言 fake 实现了 Provider。
var _ embedding.Provider = (*fakeEmbedding)(nil)

// countingEmbedding 记录每次 EmbedBatch 的批大小（验证分片逻辑）。
type countingEmbedding struct {
	calls []int
}

func (c *countingEmbedding) Name() string { return "counting" }
func (c *countingEmbedding) Dim() int     { return 4 }
func (c *countingEmbedding) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	c.calls = append(c.calls, len(texts))
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 4)
	}
	return out, nil
}

// TestService_EmbedTexts_Batching 大文档向量化按 EmbeddingBatchSize 分片：
// 40 段 / 16 批 → 调用 3 次（16/16/8），返回 40 个向量且顺序保持。
func TestService_EmbedTexts_Batching(t *testing.T) {
	emb := &countingEmbedding{}
	svc := NewService(NewStore(nil), emb, config.RAGConfig{
		EmbeddingBatchSize: 16,
		IngestMaxAttempts:  3,
	}, zap.NewNop())
	texts := make([]string, 40)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}
	vecs, err := svc.embedTexts(context.Background(), &Document{}, texts)
	if err != nil {
		t.Fatalf("embedTexts: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("向量数 %d 应等于输入段数 %d", len(vecs), len(texts))
	}
	want := []int{16, 16, 8}
	if len(emb.calls) != len(want) {
		t.Fatalf("应分片调用 %d 次，实际 %d 次（%v）", len(want), len(emb.calls), emb.calls)
	}
	for i := range want {
		if emb.calls[i] != want[i] {
			t.Errorf("第 %d 批应含 %d 段，实际 %d", i+1, want[i], emb.calls[i])
		}
	}
}

// TestService_EmbedTexts_SingleBatch 文本数 ≤ batchSize 时仅一次调用、不分片。
func TestService_EmbedTexts_SingleBatch(t *testing.T) {
	emb := &countingEmbedding{}
	svc := NewService(NewStore(nil), emb, config.RAGConfig{
		EmbeddingBatchSize: 16,
		IngestMaxAttempts:  3,
	}, zap.NewNop())
	texts := make([]string, 8)
	for i := range texts {
		texts[i] = "x"
	}
	if _, err := svc.embedTexts(context.Background(), &Document{}, texts); err != nil {
		t.Fatalf("embedTexts: %v", err)
	}
	if len(emb.calls) != 1 || emb.calls[0] != 8 {
		t.Fatalf("应单批 8 段，实际 %v", emb.calls)
	}
	// 空输入不发请求。
	if _, err := svc.embedTexts(context.Background(), &Document{}, nil); err != nil {
		t.Fatalf("embedTexts(nil): %v", err)
	}
	if len(emb.calls) != 1 {
		t.Fatalf("空输入不应触发请求，实际调用 %d 次", len(emb.calls))
	}
}

// TestService_Backoff 退避序列：10s/1m/5m，越界沿用末位。
func TestService_Backoff(t *testing.T) {
	svc := newTestService()
	want := []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for attempt, w := range want {
		if got := svc.backoff(attempt); got != w {
			t.Errorf("backoff(%d) = %v，want %v", attempt, got, w)
		}
	}
	if got := svc.backoff(-1); got != 0 {
		t.Errorf("backoff(-1) 应为 0，实际 %v", got)
	}
}

// TestService_MaxAttempts_Default 未配置重试上限时兜底 3 次。
func TestService_MaxAttempts_Default(t *testing.T) {
	svc := NewService(NewStore(nil), &fakeEmbedding{dim: 4}, config.RAGConfig{}, zap.NewNop())
	if svc.maxAttempts != 3 {
		t.Errorf("maxAttempts 默认应为 3，实际 %d", svc.maxAttempts)
	}
	if svc.batchSize != 16 {
		t.Errorf("batchSize 默认应为 16，实际 %d", svc.batchSize)
	}
}
