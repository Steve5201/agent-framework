package kb

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
	"github.com/Steve5201/agent-framework/schema"
)

// mockRag 最小 mock：仅 Search 有意义，其余方法返回"未实现"错误。
type mockRag struct {
	searchFn func(ctx context.Context, in *ragv1.SearchRequest) (*ragv1.SearchResponse, error)
}

func (m *mockRag) Search(ctx context.Context, in *ragv1.SearchRequest, _ ...grpc.CallOption) (*ragv1.SearchResponse, error) {
	if m.searchFn == nil {
		return nil, errors.New("mock: Search 未注入")
	}
	return m.searchFn(ctx, in)
}
func (m *mockRag) CreateKnowledgeBase(context.Context, *ragv1.CreateKBRequest, ...grpc.CallOption) (*ragv1.KnowledgeBase, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) ListKnowledgeBases(context.Context, *ragv1.ListKBRequest, ...grpc.CallOption) (*ragv1.ListKBResponse, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) DeleteKnowledgeBase(context.Context, *ragv1.DeleteKBRequest, ...grpc.CallOption) (*ragv1.DeleteKBResponse, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) UpsertDocument(context.Context, *ragv1.UpsertDocumentRequest, ...grpc.CallOption) (*ragv1.UpsertDocumentResponse, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) ListDocuments(context.Context, *ragv1.ListDocumentsRequest, ...grpc.CallOption) (*ragv1.ListDocumentsResponse, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) DeleteDocument(context.Context, *ragv1.DeleteDocumentRequest, ...grpc.CallOption) (*ragv1.DeleteDocumentResponse, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) GetDocumentStatus(context.Context, *ragv1.GetDocumentStatusRequest, ...grpc.CallOption) (*ragv1.DocumentStatus, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) UpdateKnowledgeBase(context.Context, *ragv1.UpdateKBRequest, ...grpc.CallOption) (*ragv1.KnowledgeBase, error) {
	return nil, errors.New("mock: 未实现")
}
func (m *mockRag) RetryDocument(context.Context, *ragv1.RetryDocumentRequest, ...grpc.CallOption) (*ragv1.DocumentStatus, error) {
	return nil, errors.New("mock: 未实现")
}

func newTestTool(m *mockRag) *searchTool {
	return &searchTool{client: m, agentID: "tutor", log: zap.NewNop()}
}

// TestSearchTool_Schema 说明书契约：名称/必填/权限/参数描述齐备。
func TestSearchTool_Schema(t *testing.T) {
	ts := newTestTool(&mockRag{}).Schema()
	if ts.Name != "kb_search" {
		t.Fatalf("工具名应为 kb_search，实际 %s", ts.Name)
	}
	if len(ts.Required) != 1 || ts.Required[0] != "query" {
		t.Fatalf("必填应为 [query]，实际 %v", ts.Required)
	}
	if ts.Permission != schema.PermissionL1Read {
		t.Fatalf("权限应为 L1 只读，实际 %v", ts.Permission)
	}
	// 参数 JSON Schema 必须可解析且含类型定义。
	var params map[string]any
	if err := json.Unmarshal(ts.Parameters, &params); err != nil {
		t.Fatalf("参数 JSON Schema 非法: %v", err)
	}
	if params["type"] != "object" {
		t.Fatalf("参数应为 object，实际 %v", params["type"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 properties: %v", params)
	}
	for _, k := range []string{"query", "kb_ids", "top_k"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("参数 %s 未声明", k)
		}
	}
}

// TestSearchTool_Execute 正常检索：请求参数透传 + 结果带来源引用格式化。
// 会话级知识库由 agent-service 注入 ctx（WithKBIDs），缺省按此检索。
func TestSearchTool_Execute(t *testing.T) {
	var got *ragv1.SearchRequest
	m := &mockRag{
		searchFn: func(_ context.Context, in *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
			got = in
			return &ragv1.SearchResponse{Chunks: []*ragv1.Chunk{
				{
					ChunkId: "ck1", KbId: "kb_a", KbName: "高数课件",
					Source:  "高等数学.pdf·第3页",
					Content: "极限的定义：设函数 f(x)...",
					Score:   0.86,
				},
			}}, nil
		},
	}
	ctx := WithKBIDs(context.Background(), []string{"kb_a"})
	out, err := newTestTool(m).Execute(ctx, json.RawMessage(`{"query":"极限的定义","top_k":3}`))
	if err != nil {
		t.Fatalf("Execute 意外错误: %v", err)
	}
	// 请求契约：query 透传、top_k 生效、agent_id 注入资源域（防跨域泄露）。
	if got.GetQuery() != "极限的定义" {
		t.Fatalf("query 透传错误: %v", got.GetQuery())
	}
	if got.GetTopK() != 3 {
		t.Fatalf("top_k 应为 3，实际 %d", got.GetTopK())
	}
	if got.GetAgentId() != "tutor" {
		t.Fatalf("agent_id 应为 tutor，实际 %q", got.GetAgentId())
	}
	// 模型未显式传 kb_ids 时，默认检索范围 = 会话勾选的知识库（ctx 注入）。
	if len(got.GetKbIds()) != 1 || got.GetKbIds()[0] != "kb_a" {
		t.Fatalf("默认检索范围应为会话知识库 [kb_a]，实际 %v", got.GetKbIds())
	}
	// 输出契约：含来源引用与片段内容。
	if !strings.Contains(out, "高等数学.pdf·第3页") || !strings.Contains(out, "极限的定义") {
		t.Fatalf("输出缺少来源引用/内容: %s", out)
	}
}

// TestSearchTool_Execute_NoKB 未勾选知识库（ctx 无 kb_ids）→ 明确报错而非检索全部。
// 对应 P3-A6 反转语义：不勾选 = 本会话不使用知识库检索。
func TestSearchTool_Execute_NoKB(t *testing.T) {
	m := &mockRag{searchFn: func(_ context.Context, _ *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
		return &ragv1.SearchResponse{}, nil
	}}
	_, err := newTestTool(m).Execute(context.Background(), json.RawMessage(`{"query":"极限的定义"}`))
	if err == nil {
		t.Fatal("会话未勾选知识库时应报错，实际 nil")
	}
	if !strings.Contains(err.Error(), "未启用知识库检索") {
		t.Fatalf("错误信息应明确提示未启用知识库: %v", err)
	}
}

// TestSearchTool_Execute 参数防御：空 query / 非法 JSON / top_k 越界钳制。
func TestSearchTool_Execute_Args(t *testing.T) {
	m := &mockRag{searchFn: func(_ context.Context, _ *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
		return &ragv1.SearchResponse{}, nil
	}}
	tool := newTestTool(m)
	ctx := WithKBIDs(context.Background(), []string{"kb_a"})

	if _, err := tool.Execute(ctx, json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Fatal("空 query 应报错")
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{bad json`)); err == nil {
		t.Fatal("非法 JSON 应报错")
	}

	// top_k 越界 → 钳制到上限（20），不崩溃。
	var got int32
	m.searchFn = func(_ context.Context, in *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
		got = in.GetTopK()
		return &ragv1.SearchResponse{}, nil
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"query":"a","top_k":999}`)); err != nil {
		t.Fatalf("top_k 越界不应报错: %v", err)
	}
	if got != 20 {
		t.Fatalf("top_k 应钳制为 20，实际 %d", got)
	}
}

// TestSearchTool_Execute 空结果：友好提示而非报错（模型据此调整检索词）。
func TestSearchTool_Execute_Empty(t *testing.T) {
	m := &mockRag{searchFn: func(_ context.Context, _ *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
		return &ragv1.SearchResponse{}, nil
	}}
	ctx := WithKBIDs(context.Background(), []string{"kb_a"})
	out, err := newTestTool(m).Execute(ctx, json.RawMessage(`{"query":"量子纠缠"}`))
	if err != nil {
		t.Fatalf("空结果不应报错: %v", err)
	}
	if !strings.Contains(out, "未检索到") {
		t.Fatalf("空结果提示不符: %s", out)
	}
}

// TestSearchTool_Execute_Media 媒体透传（知识库媒体检索）：命中片段带
// metadata["media"]（rag-media/<docID>/<file> 公共路径，逗号分隔）时，
// 结果里必须列出「附带媒体」，模型才能据渲染协议拼 URL 渲染。
func TestSearchTool_Execute_Media(t *testing.T) {
	m := &mockRag{searchFn: func(_ context.Context, _ *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
		return &ragv1.SearchResponse{Chunks: []*ragv1.Chunk{
			{
				ChunkId: "ck1", KbId: "kb_a", KbName: "数据结构课件",
				Source:  "栈与队列.pdf·第5页",
				Content: "栈的示意图如下：",
				Metadata: map[string]string{
					"source": "栈与队列.pdf·第5页",
					"media":  "rag-media/doc1/stack.png,rag-media/doc1/push.mp4",
				},
			},
			{
				ChunkId: "ck2", KbId: "kb_a", KbName: "数据结构课件",
				Source:  "栈与队列.pdf·第6页",
				Content: "队列的实现",
			},
		}}, nil
	}}
	ctx := WithKBIDs(context.Background(), []string{"kb_a"})
	out, err := newTestTool(m).Execute(ctx, json.RawMessage(`{"query":"栈","top_k":5}`))
	if err != nil {
		t.Fatalf("Execute 意外错误: %v", err)
	}
	// 带 media 的片段：必须列出完整路径清单（逗号分隔原样保留）。
	if !strings.Contains(out, "附带媒体：rag-media/doc1/stack.png,rag-media/doc1/push.mp4") {
		t.Fatalf("输出应列出附带媒体路径清单，实际：%s", out)
	}
	// 无 media 的片段：不输出「附带媒体」行（避免误导模型）。
	if strings.Count(out, "附带媒体") != 1 {
		t.Fatalf("仅带媒体的片段应列出一次「附带媒体」，实际 %d 次：%s", strings.Count(out, "附带媒体"), out)
	}
}

// TestSearchTool_Execute 下游错误（如越权 kb_ids 返回 404）：透传为错误回填给模型。
func TestSearchTool_Execute_RagError(t *testing.T) {
	m := &mockRag{searchFn: func(_ context.Context, _ *ragv1.SearchRequest) (*ragv1.SearchResponse, error) {
		return nil, errors.New("rpc error: code = NotFound desc = 知识库不存在或无权访问")
	}}
	if _, err := newTestTool(m).Execute(context.Background(), json.RawMessage(`{"query":"a","kb_ids":["kb_x"]}`)); err == nil {
		t.Fatal("下游错误应透传")
	}
}

// TestProvider 提供者契约：Name 稳定、Tools 产出注册用工具。
func TestProvider(t *testing.T) {
	p := NewProvider(&mockRag{}, "tutor", nil)
	if p.Name() != "kb" {
		t.Fatalf("provider 名应为 kb，实际 %s", p.Name())
	}
	ts := p.Tools()
	if len(ts) != 1 {
		t.Fatalf("应产出 1 个工具，实际 %d", len(ts))
	}
	if ts[0].Schema().Name != "kb_search" {
		t.Fatalf("工具名应为 kb_search，实际 %s", ts[0].Schema().Name)
	}
}
