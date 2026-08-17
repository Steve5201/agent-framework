package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Steve5201/agent-framework/schema"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// serverConn 一个 MCP Server 的长连接：连接保活，tools/list 结果缓存，
// 工具调用并发转发（mcp-go client 按 JSON-RPC id 并发安全）。
type serverConn struct {
	cfg *ServerConfig
	log *zap.Logger

	mu  sync.Mutex
	cli client.MCPClient
	// testClient 测试注入：非空时 dial 直接使用（配合 NewInProcessClient），不拉起子进程。
	testClient client.MCPClient
	// schemas MCP 原始工具名 → 映射后的工具 schema（发现后缓存）。
	schemas map[string]schema.ToolSchema
	// dialErr 首次连接失败原因（失败缓存，避免每次 Tools() 重试子进程）。
	dialErr error
}

func newServerConn(cfg *ServerConfig, log *zap.Logger) *serverConn {
	return &serverConn{cfg: cfg, log: log}
}

// setClient 测试注入用：直接替换底层 client（配合 NewInProcessClient）。
func (c *serverConn) setClient(cli client.MCPClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.testClient = cli
}

// ensureDiscovered 保证已连接并完成 tools/list 发现（幂等；失败缓存）。
func (c *serverConn) ensureDiscovered(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.schemas != nil {
		return c.dialErr
	}
	if err := c.dial(ctx); err != nil {
		c.dialErr = err
		return err
	}
	return c.dialErr
}

// dial 建立连接并拉取工具列表；调用方需持有 mu。
func (c *serverConn) dial(ctx context.Context) error {
	cfg := c.cfg
	var cli client.MCPClient
	var err error

	if c.testClient != nil {
		// 测试注入：使用 InProcess 客户端（跳过子进程拉起）。
		cli = c.testClient
	} else {
		switch cfg.Transport {
		case TransportStdio:
			env := make([]string, 0, len(cfg.Env))
			for k, v := range cfg.Env {
				env = append(env, k+"="+v)
			}
			// 标准配置支持 cwd（工作目录）：通过 WithCommandFunc 注入 exec.Cmd。
			if cfg.Cwd != "" {
				opts := []transport.StdioOption{transport.WithCommandFunc(func(ctx context.Context, cmd string, e []string, args []string) (*exec.Cmd, error) {
					c := exec.CommandContext(ctx, cmd, args...)
					c.Env = e
					c.Dir = cfg.Cwd
					return c, nil
				})}
				cli, err = client.NewStdioMCPClientWithOptions(cfg.Command, env, cfg.Args, opts...)
			} else {
				cli, err = client.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
			}
		case TransportHTTP:
			opts := []transport.StreamableHTTPCOption{}
			if len(cfg.Headers) > 0 {
				opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
			}
			cli, err = client.NewStreamableHttpClient(cfg.URL, opts...)
		default:
			return fmt.Errorf("mcp: 不支持的 transport %q", cfg.Transport)
		}
	}
	if err != nil {
		return fmt.Errorf("mcp: 创建 %s 客户端失败: %w", cfg.Transport, err)
	}

	// MCP 握手：协议版本协商 + 客户端信息声明（业界标准初始化流程）。
	initCtx, cancel := c.timeoutCtx(ctx)
	defer cancel()
	if _, err := cli.Initialize(initCtx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo:      mcp.Implementation{Name: "agent-backend", Version: "0.1.0"},
		},
	}); err != nil {
		_ = cli.Close()
		return fmt.Errorf("mcp: %s 初始化失败: %w", cfg.Name, err)
	}

	// 动态发现工具（tools/list）。
	listCtx, cancel2 := c.timeoutCtx(ctx)
	defer cancel2()
	resp, err := cli.ListTools(listCtx, mcp.ListToolsRequest{})
	if err != nil {
		_ = cli.Close()
		return fmt.Errorf("mcp: %s tools/list 失败: %w", cfg.Name, err)
	}

	perm := cfg.Permission()
	c.schemas = make(map[string]schema.ToolSchema, len(resp.Tools))
	for _, t := range resp.Tools {
		ts := schema.ToolSchema{
			Name:        "mcp_" + SanitizeName(cfg.Name) + "_" + SanitizeName(t.Name),
			Description: t.Description,
			Parameters:  marshalInputSchema(t.InputSchema),
			Permission:  perm,
		}
		if ts.Description == "" {
			ts.Description = fmt.Sprintf("MCP 工具 %s（来自 server %s）", t.Name, cfg.Name)
		}
		c.schemas[t.Name] = ts
	}
	c.cli = cli
	return nil
}

// timeoutCtx 为握手/调用叠加 server 级超时（配置 0 = 直接透传上游 ctx）。
func (c *serverConn) timeoutCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.cfg.TimeoutSeconds > 0 {
		return context.WithTimeout(ctx, time.Duration(c.cfg.TimeoutSeconds)*time.Second)
	}
	return context.WithCancel(ctx)
}

// callTool 执行远端工具：参数原样转发 tools/call，结果转文本。
func (c *serverConn) callTool(ctx context.Context, mcpName string, args json.RawMessage) (string, error) {
	c.mu.Lock()
	cli := c.cli
	c.mu.Unlock()
	if cli == nil {
		return "", fmt.Errorf("mcp: server %s 未连接（tools/list 阶段失败）", c.cfg.Name)
	}

	// 参数 JSON → map（MCP 要求对象参数）。
	arguments := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", fmt.Errorf("mcp: %s.%s 参数解析失败: %w", c.cfg.Name, mcpName, err)
		}
	}

	callCtx, cancel := c.timeoutCtx(ctx)
	defer cancel()
	resp, err := cli.CallTool(callCtx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: mcpName, Arguments: arguments},
	})
	if err != nil {
		return "", fmt.Errorf("mcp: %s.%s 调用失败: %w", c.cfg.Name, mcpName, err)
	}
	return formatCallResult(resp), nil
}

// close 释放连接（stdio 子进程 / HTTP 会话）。
func (c *serverConn) close() error {
	c.mu.Lock()
	cli := c.cli
	c.cli = nil
	c.schemas = nil
	c.mu.Unlock()
	if cli != nil {
		return cli.Close()
	}
	return nil
}

// marshalInputSchema 把 MCP 工具的 inputSchema（JSON Schema）序列化为框架
// Parameters。framework 的 ValidateArgs 会从 JSON Schema 内合并 required 与
// 属性类型校验，因此 MCP 原样 schema 可直接透传（业界通用格式兼容）。
func marshalInputSchema(is mcp.ToolInputSchema) json.RawMessage {
	if raw, err := json.Marshal(is); err == nil && len(raw) > 2 {
		return raw
	}
	// 兜底：空 schema 退化为无参对象（避免 nil JSON 破坏 LLM 请求）。
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// SanitizeName 与 skill 包一致的注册名净化（小写、非 [a-z0-9_] 转下划线）。
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "mcp"
	}
	return out
}
