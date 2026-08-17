package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Steve5201/agent-framework/schema"
)

// 支持的 MCP transport 类型（业界标准 MCP 协议的两类主流传输）。
const (
	// TransportStdio 通过本地子进程 stdio 通信（如 npx -y @modelcontextprotocol/server-xxx）。
	TransportStdio = "stdio"
	// TransportHTTP 通过 Streamable HTTP（MCP 官方推荐的无状态 HTTP 传输）。
	TransportHTTP = "http"
)

// ServerConfig 单个外部 MCP Server 的连接配置。
//
// stdio 示例（本地命令启动的 MCP server）：
//
//	{
//	  "name": "github",
//	  "transport": "stdio",
//	  "command": "npx",
//	  "args": ["-y", "@modelcontextprotocol/server-github"],
//	  "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "xxx"},
//	  "default_permission": "L2"
//	}
//
// http 示例（远程 Streamable HTTP MCP server）：
//
//	{
//	  "name": "weather",
//	  "transport": "http",
//	  "url": "https://mcp.example.com/weather",
//	  "headers": {"Authorization": "Bearer xxx"},
//	  "default_permission": "L1"
//	}
type ServerConfig struct {
	// Name server 标识（必填，用于工具名前缀与日志）。
	Name string `json:"name"`
	// Enabled 是否启用该 server；nil/true = 启用，false = 禁用（工具不注册）。
	// 用指针区分"未配置（默认启用）"与"显式禁用"，避免老配置无该字段时被误判禁用。
	Enabled *bool `json:"enabled,omitempty"`
	// Transport 传输方式：stdio | http（缺省 stdio）。
	Transport string `json:"transport"`
	// Command stdio 模式：可执行命令（必填）。
	Command string `json:"command,omitempty"`
	// Args stdio 模式：命令行参数。
	Args []string `json:"args,omitempty"`
	// Cwd stdio 模式：子进程工作目录（可选；Claude/trae/workbuddy 标准配置字段）。
	Cwd string `json:"cwd,omitempty"`
	// Env stdio 模式：附加环境变量（KEY=VALUE 传给子进程）。
	Env map[string]string `json:"env,omitempty"`
	// URL http 模式：MCP endpoint 地址（必填）。
	URL string `json:"url,omitempty"`
	// Headers http 模式：附加请求头（如认证）。
	Headers map[string]string `json:"headers,omitempty"`
	// TimeoutSeconds 单次工具调用超时（秒）；0 = 使用上游 context 超时。
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// DefaultPermission 该 server 工具的默认权限级别：
	// L0=只读展示 | L1=只读 | L2=需确认 | L3=高危；缺省 L2（MCP 工具有副作用，保守起见需确认）。
	DefaultPermission string `json:"default_permission,omitempty"`
	// DiscoveredTools 最近一次"测试连接/启用"成功发现的工具（名称 + 介绍，管理端展示用，agent 忽略）。
	DiscoveredTools ToolInfoList `json:"discovered_tools,omitempty"`
	// DiscoveryError 最近一次"测试连接/启用"的连接失败原因（管理端展示用，agent 忽略）。
	DiscoveryError string `json:"discovery_error,omitempty"`
}

// ToolInfoList 工具摘要列表，兼容新旧两种配置文件格式：
//   - 新格式：[{name,description}, ...]（含工具介绍，管理端展开面板使用）；
//   - 旧格式：["name1","name2", ...]（早期只存工具名）。
//
// 反序列化时自动兼容旧格式（name 保留、description 留空），
// 序列化统一输出新格式，保证升级后旧 mcp_servers.json 不损坏、可平滑迁移。
type ToolInfoList []ToolInfo

// UnmarshalJSON 同时接受对象数组（新）与字符串数组（旧）。
func (t *ToolInfoList) UnmarshalJSON(data []byte) error {
	var infos []ToolInfo
	if err := json.Unmarshal(data, &infos); err == nil {
		*t = infos
		return nil
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return fmt.Errorf("discovered_tools 既不是工具对象数组也不是字符串数组")
	}
	out := make(ToolInfoList, 0, len(names))
	for _, n := range names {
		out = append(out, ToolInfo{Name: n})
	}
	*t = out
	return nil
}

// MarshalJSON 统一输出新格式。
func (t ToolInfoList) MarshalJSON() ([]byte, error) {
	return json.Marshal([]ToolInfo(t))
}

// ParseServersJSON 解析 MCP Server 列表。兼容两种业界格式：
//
//  1. 数组（本项目原生）：`[{ "name": "x", "transport": "stdio", "command": "…", ... }]`
//  2. 标准对象（Claude Desktop / trae / workbuddy）：`{ "mcpServers": { "<name>": { "command": "…", "args": […], "cwd": "…" } } }`
//     也接受裸对象 `{ "<name>": { … } }`（无 mcpServers 包装）。
//
// 对象格式里 server 名取 key，对象内不再需要 name 字段。
func ParseServersJSON(data []byte) ([]ServerConfig, error) {
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		return parseArray(data)
	}
	return parseObject(data)
}

// parseArray 解析数组格式。
func parseArray(data []byte) ([]ServerConfig, error) {
	var cfgs []ServerConfig
	if err := json.Unmarshal(data, &cfgs); err != nil {
		return nil, fmt.Errorf("mcp: MCP Servers 配置不是合法 JSON 数组: %w", err)
	}
	for i := range cfgs {
		if err := cfgs[i].validate(); err != nil {
			return nil, fmt.Errorf("mcp: 第 %d 个 server 配置不合法: %w", i+1, err)
		}
	}
	return cfgs, nil
}

// parseObject 解析标准对象格式（mcpServers 包装或裸对象）。
func parseObject(data []byte) ([]ServerConfig, error) {
	var doc struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("mcp: MCP Servers 配置不是合法 JSON: %w", err)
	}
	var servers map[string]json.RawMessage
	if doc.McpServers != nil {
		servers = doc.McpServers
	} else {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("mcp: MCP Servers 配置不是合法 JSON: %w", err)
		}
		servers = m
	}
	if len(servers) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names) // 稳定顺序（工具注册顺序可预期）
	out := make([]ServerConfig, 0, len(names))
	for _, name := range names {
		var cfg ServerConfig
		if err := json.Unmarshal(servers[name], &cfg); err != nil {
			return nil, fmt.Errorf("mcp: server %q 配置解析失败: %w", name, err)
		}
		cfg.Name = name
		if err := cfg.validate(); err != nil {
			return nil, fmt.Errorf("mcp: server %q 配置不合法: %w", name, err)
		}
		out = append(out, cfg)
	}
	return out, nil
}

// validate 校验单个 server 配置的必填项与枚举值。
func (c *ServerConfig) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name 不能为空")
	}
	switch c.Transport {
	case "", TransportStdio:
		c.Transport = TransportStdio
		if strings.TrimSpace(c.Command) == "" {
			return fmt.Errorf("stdio 模式必须提供 command")
		}
	case TransportHTTP:
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			return fmt.Errorf("http 模式必须提供合法的 url")
		}
	default:
		return fmt.Errorf("transport 仅支持 stdio|http，实际 %q", c.Transport)
	}
	// 权限级别校验（非法值报错，避免静默降级）。
	if _, err := parsePermission(c.DefaultPermission); err != nil {
		return err
	}
	if c.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds 不能为负")
	}
	return nil
}

// Permission 解析出本 server 工具默认权限级别；缺省 L2。
func (c *ServerConfig) Permission() schema.PermissionLevel {
	p, _ := parsePermission(c.DefaultPermission)
	return p
}

// IsEnabled 是否启用：未显式配置（nil）= 启用；显式 false = 禁用。
func (c *ServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// parsePermission 解析 "L0"~"L3"（兼容纯数字写法）；空 = L2。
func parsePermission(s string) (schema.PermissionLevel, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "":
		return schema.PermissionL2Write, nil
	case "L0", "0":
		return schema.PermissionL0Pure, nil
	case "L1", "1":
		return schema.PermissionL1Read, nil
	case "L2", "2":
		return schema.PermissionL2Write, nil
	case "L3", "3":
		return schema.PermissionL3Dangerous, nil
	default:
		return 0, fmt.Errorf("default_permission 仅支持 L0|L1|L2|L3，实际 %q", s)
	}
}
