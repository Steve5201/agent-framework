// Package kb 提供知识库检索工具（kb_search，L1 只读）。
//
// 数据链路：agent（本包）→ rag-service gRPC（Search RPC，混合检索）→ pgvector。
// 与"文件态"工具（file_ops/skill）不同，知识库是"数据库态"数据（含向量），
// 本工具是纯只读检索，无副作用、无需用户确认（PermissionL1Read）。
//
// 多租户隔离（阶段3）：检索范围强制限定本智能体资源域——每次请求携带
// 本实例的 AgentID，kb_ids 越出本域由 rag 侧返回 404（防跨域检索泄露）。
package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// Provider 知识库检索工具提供者（tools.ToolProvider 实现）。
type Provider struct {
	client  ragv1.RagServiceClient // rag-service gRPC 客户端（必填）
	agentID string                 // 本智能体资源域（检索范围限定，防跨域泄露）
	log     *zap.Logger
}

// NewProvider 创建知识库工具提供者。
// client 为 rag-service gRPC 客户端；agentID 为当前智能体域（如 "tutor"）。
// log 可空（空则静默）。
func NewProvider(client ragv1.RagServiceClient, agentID string, log *zap.Logger) *Provider {
	if log == nil {
		log = zap.NewNop()
	}
	return &Provider{client: client, agentID: agentID, log: log}
}

// Name 提供者唯一标识（日志溯源用）。
func (p *Provider) Name() string { return "kb" }

// Tools 返回本提供者的全部工具（本期仅 kb_search）。
func (p *Provider) Tools() []tool.Tool {
	return []tool.Tool{&searchTool{client: p.client, agentID: p.agentID, log: p.log}}
}

// kbSearchArgs kb_search 参数（与 JSON Schema 对齐）。
type kbSearchArgs struct {
	Query string   `json:"query"`
	KBIDs []string `json:"kb_ids"`
	TopK  int      `json:"top_k"`
}

// kbSearchTimeout 单次检索超时（向量化 + DB 检索 + gRPC 往返）。
const kbSearchTimeout = 30 * time.Second

// ctxKBIDsKey context 键：会话级默认知识库 ID 列表。
type ctxKBIDsKey struct{}

// WithKBIDs 把会话级默认 kb_ids 注入 context。
// agent-service 在对话开始时把会话配置的 KBIDs 注入，
// kb_search 在模型未显式传 kb_ids 时按此限定默认检索范围。
func WithKBIDs(ctx context.Context, ids []string) context.Context {
	return context.WithValue(ctx, ctxKBIDsKey{}, ids)
}

// KBIDsFromContext 读取 context 中的默认 kb_ids（无则返回 nil）。
func KBIDsFromContext(ctx context.Context) []string {
	ids, _ := ctx.Value(ctxKBIDsKey{}).([]string)
	return ids
}

// searchTool kb_search 工具：混合检索知识库并返回带来源引用的片段。
type searchTool struct {
	client  ragv1.RagServiceClient
	agentID string
	log     *zap.Logger
}

// Schema 实现 tool.Tool 接口（Tool Schema 遵循主流大模型 Function Calling 规范）。
//
// 可用性契约（P3-A6 反转语义）：本工具只在"会话勾选了知识库"时装配——
// 未配置知识库的会话，工具名不在注册表内，模型不可调用；因此描述里不再
// 声称"未配置 = 检索全部"。
func (t *searchTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name: "kb_search",
		Description: "知识库检索（只读）：在已勾选的知识库中检索文档片段，返回带来源引用（文件名/页码）的内容。" +
			"当用户的问题涉及课件、讲义、教材、课程资料等已上传到知识库的内容时，先调用本工具检索相关片段，再基于片段回答。" +
			"参数 query 必填——用问题中的核心名词/概念作为检索词（如\"极限的定义\"）；" +
			"kb_ids 可选（限定检索的知识库 ID 列表；缺省按会话勾选的知识库检索）；" +
			"top_k 可选（返回片段数 1-20，默认 5）。" +
			"命中片段若带「附带媒体」行（rag-media/… 图片等公共路径），可遵循渲染协议拼 <files 基址>/files/<路径> 渲染到对话，或作为文档生成素材引用。" +
			"检索片段仅作参考，若不足以回答问题，应如实告知用户并建议补充资料。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","description":"核心名词/概念检索词，如 极限的定义"},
				"kb_ids":{"type":"array","items":{"type":"string"},"description":"限定检索的知识库 ID 列表；空 = 按会话勾选的知识库检索"},
				"top_k":{"type":"integer","minimum":1,"maximum":20,"description":"返回片段数，默认 5"}
			}
		}`),
		Required:   []string{"query"},
		Permission: schema.PermissionL1Read,
	}
}

// Execute 实现 tool.Tool 接口：调 rag-service Search 并格式化为带来源的列表。
func (t *searchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p kbSearchArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("kb_search: 参数解析失败: %w", err)
	}
	p.Query = strings.TrimSpace(p.Query)
	if p.Query == "" {
		return "", fmt.Errorf("kb_search: query 不能为空")
	}
	topK := p.TopK
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}
	// 模型未显式限定 kb_ids 时，按会话勾选的知识库默认检索（agent-service 注入）。
	// 防御：会话未勾选知识库时本工具不应被装配调用；万一走到这里直接给出
	// 明确提示，而不是"检索全部"（P3-A6 反转语义，不勾选 = 不使用知识库）。
	if len(p.KBIDs) == 0 {
		p.KBIDs = KBIDsFromContext(ctx)
		if len(p.KBIDs) == 0 {
			return "", fmt.Errorf("kb_search: 当前会话未启用知识库检索（请在对话配置中勾选知识库后再试）")
		}
	}

	// 超时保护：向量化 + DB 混合检索 + gRPC 往返，单次调用不无限阻塞对话。
	ctx, cancel := context.WithTimeout(ctx, kbSearchTimeout)
	defer cancel()

	resp, err := t.client.Search(ctx, &ragv1.SearchRequest{
		Query:   p.Query,
		KbIds:   p.KBIDs,
		TopK:    int32(topK),
		AgentId: t.agentID, // 资源域限定：越界 kb_ids 由 rag 侧返回 404
	})
	if err != nil {
		// rag 不可达 / 越权 kb_ids / embedding 未配置 → 明确错误回填给模型重试。
		return "", fmt.Errorf("kb_search: 知识库检索失败: %w", err)
	}
	chunks := resp.GetChunks()
	if len(chunks) == 0 {
		return "知识库中未检索到与“" + p.Query + "”相关的内容，可更换关键词后重试。", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "知识库检索“%s”的结果（共 %d 条）：\n", p.Query, len(chunks))
	for i, c := range chunks {
		src := strings.TrimSpace(c.GetSource())
		if src == "" {
			src = "未知来源"
		}
		kbName := strings.TrimSpace(c.GetKbName())
		fmt.Fprintf(&b, "%d. 【%s】来源：%s\n    %s\n",
			i+1, orDefault(kbName, "知识库"), src, strings.TrimSpace(c.GetContent()))
		// 媒体透传（知识库媒体检索）：命中片段可能附带文档解析出的媒体文件
		// （图片/视频等，path 为公共引用路径 rag-media/<docID>/<file>，逗号分隔）。
		// 模型可据此用渲染协议拼 <files 基址>/files/<path> 直接渲染到对话，
		// 或在生成文档时引用为图片素材——不列出，模型将无从知晓媒体存在。
		if media := strings.TrimSpace(c.GetMetadata()["media"]); media != "" {
			fmt.Fprintf(&b, "    附带媒体：%s\n", media)
		}
	}
	return b.String(), nil
}

// orDefault 空串回退默认值。
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// 编译期断言：Provider / searchTool 实现各自接口。
var (
	_ tools.ToolProvider = (*Provider)(nil)
	_ tool.Tool          = (*searchTool)(nil)
)
