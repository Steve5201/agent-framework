package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// Provider MCP 工具提供者：连接一组外部 MCP Server，把各自 tools/list
// 动态发现的工具注册进框架工具集，调用时经 tools/call 转发给远端执行。
//
// 业界对齐：Model Context Protocol（Anthropic 开放标准）。工具名遵循
// Claude 生态惯例加 server 前缀避免与内置工具冲突：
//
//	mcp_<server>_<tool>   （如 mcp_github_search_repositories）
//
// 容错策略（与 Claude Desktop 一致）：单个 server 连接/发现失败仅记警告
// 并跳过该 server 的工具，不影响整体启动；全部失败 = 零工具。
type Provider struct {
	servers []*serverConn
	log     *zap.Logger
}

// NewProvider 创建 MCP 提供者；cfg 为空 = 不加载任何 MCP server。
func NewProvider(cfgs []ServerConfig, log *zap.Logger) *Provider {
	p := &Provider{log: log}
	for i := range cfgs {
		p.servers = append(p.servers, newServerConn(&cfgs[i], log))
	}
	return p
}

// Name 实现 tools.ToolProvider 接口。
func (p *Provider) Name() string { return "mcp" }

// Tools 实现 tools.ToolProvider 接口：连接并发现各 server 的工具。
//
// 首次调用时惰性连接（连接成功的长连接保活，供后续 Execute 复用）；
// 连接失败/工具发现失败 → 记警告跳过该 server，绝不使注册整体失败。
func (p *Provider) Tools() []tool.Tool {
	ctx := context.Background()
	var out []tool.Tool
	for _, s := range p.servers {
		if err := s.ensureDiscovered(ctx); err != nil {
			p.logWarn("MCP server 连接/发现失败，已跳过",
				zap.String("server", s.cfg.Name), zap.Error(err))
			continue
		}
		for mcpName, ts := range s.schemas {
			out = append(out, &mcpTool{conn: s, mcpName: mcpName, ts: ts})
		}
	}
	return out
}

// ToolInfo 管理端展示用的工具摘要（名称 + 介绍）。
// 来源：MCP tools/list 的 name + description，供管理端"展开工具列表"面板使用。
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// DiscoverTools 实际连接 MCP server 并列出其工具（名称 + 介绍）。
// 与 Provider 不同：这里是一次性短连接（连接 → 初始化 → tools/list → 关闭），
// 连接/发现失败返回错误，由调用方决定是否拦截启用。
func DiscoverTools(ctx context.Context, cfg *ServerConfig) ([]ToolInfo, error) {
	conn := newServerConn(cfg, zap.NewNop())
	defer conn.close()
	if err := conn.ensureDiscovered(ctx); err != nil {
		return nil, err
	}
	out := make([]ToolInfo, 0, len(conn.schemas))
	for name, ts := range conn.schemas {
		out = append(out, ToolInfo{Name: name, Description: ts.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Close 关闭全部 MCP 连接（释放 stdio 子进程 / HTTP 会话）。
func (p *Provider) Close() error {
	var errs []error
	for _, s := range p.servers {
		if err := s.close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mcp: 关闭连接失败: %v", errs)
	}
	return nil
}

func (p *Provider) logWarn(msg string, fields ...zap.Field) {
	if p.log != nil {
		p.log.Warn(msg, fields...)
	}
}

// mcpTool 单个远端 MCP 工具：schema 静态映射（发现时确定），执行时转发调用。
type mcpTool struct {
	conn    *serverConn
	mcpName string // MCP 原始工具名（tools/call 用）
	ts      schema.ToolSchema
}

// Schema 实现 tool.Tool 接口。
func (t *mcpTool) Schema() schema.ToolSchema { return t.ts }

// Execute 实现 tool.Tool 接口：把参数原样转发给远端 MCP Server。
func (t *mcpTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.conn.callTool(ctx, t.mcpName, args)
}

// formatCallResult 把 MCP 工具返回（Content 数组 + StructuredContent）转成
// 纯文本，供模型消费；图片/音频等不可文本化的结果仅保留元信息摘要。
func formatCallResult(r *mcp.CallToolResult) string {
	var b strings.Builder
	prefix := ""
	if r.IsError {
		prefix = "（MCP 工具返回错误）"
	}
	for _, c := range r.Content {
		switch v := c.(type) {
		case mcp.TextContent:
			b.WriteString(v.Text)
			b.WriteString("\n")
		case *mcp.TextContent:
			b.WriteString(v.Text)
			b.WriteString("\n")
		case mcp.ImageContent:
			writeImageNote(&b, v.MIMEType, len(v.Data))
		case *mcp.ImageContent:
			writeImageNote(&b, v.MIMEType, len(v.Data))
		case mcp.AudioContent:
			writeAudioNote(&b, v.MIMEType, len(v.Data))
		case *mcp.AudioContent:
			writeAudioNote(&b, v.MIMEType, len(v.Data))
		case mcp.EmbeddedResource:
			writeResourceNote(&b, v.Resource)
		case *mcp.EmbeddedResource:
			writeResourceNote(&b, v.Resource)
		}
	}
	if r.StructuredContent != nil {
		if raw, err := json.Marshal(r.StructuredContent); err == nil {
			b.WriteString(string(raw))
			b.WriteString("\n")
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = "（MCP 工具返回空结果）"
	}
	return prefix + out
}

// writeImageNote 图片结果仅保留元信息摘要（LLM 无法直接消费二进制）。
func writeImageNote(b *strings.Builder, mime string, n int) {
	fmt.Fprintf(b, "（图片结果：MIME %s，%d 字节，data URI 已省略，请让用户在前端查看）\n", mime, n)
}

// writeAudioNote 音频结果仅保留元信息摘要。
func writeAudioNote(b *strings.Builder, mime string, n int) {
	fmt.Fprintf(b, "（音频结果：MIME %s，%d 字节，无法文本化）\n", mime, n)
}

// writeResourceNote 文本型内嵌资源直接输出文本，其余仅标注存在。
func writeResourceNote(b *strings.Builder, res mcp.ResourceContents) {
	if blob, ok := res.(*mcp.TextResourceContents); ok {
		b.WriteString(blob.Text)
		b.WriteString("\n")
		return
	}
	b.WriteString("（资源结果，无法文本化）\n")
}

// 编译期断言：Provider 实现 ToolProvider，mcpTool 实现 tool.Tool。
var (
	_ tools.ToolProvider = (*Provider)(nil)
	_ tool.Tool          = (*mcpTool)(nil)
)
