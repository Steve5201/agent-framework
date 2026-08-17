// domainview.go —— 按智能体域返回资源/工具清单（多智能体配置区跟随）。
//
// agent 实例按 AGENT_ID 固定装配一个域；当管理端创建了其它智能体域时，
// 前端切换智能体后配置区（能力/技能/MCP）需要显示**目标域**的资源，
// 而非本实例域。本文件提供只读的按域扫描实现，注入 agentsvc.DomainViewer：
//
//	技能：扫描 <skillsRoot>/<agent_id>/<name>/SKILL.md（复用 skill.Provider）；
//	MCP ：读取 <mcpDir>/<agent_id>/<文件名>，取已启用 server 的 discovered_tools。
//
// 与 toolset.go/reloader.go 保持同一文件态布局，不建立任何 MCP 连接。
package main

import (
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/agentsvc"
	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/tools/mcp"
	"github.com/Steve5201/agent-backend/internal/tools/skill"
)

// domainViewer 按智能体域扫描 skill/mcp 目录返回资源/工具清单。
type domainViewer struct {
	cfg *config.Config
	log *zap.Logger
}

func newDomainViewer(cfg *config.Config, log *zap.Logger) agentsvc.DomainViewer {
	return &domainViewer{cfg: cfg, log: log}
}

// skillsRoot 技能根目录（<root>/<agent_id>/<name>/SKILL.md 多租户布局的根）。
func (v *domainViewer) skillsRoot() string {
	return filepath.Dir(agentSkillsDir(v.cfg))
}

// Skills 返回该域已启用的技能资源清单（不含全局能力；目录不存在 = 空）。
func (v *domainViewer) Skills(agentID string) []agentsvc.ResourceInfo {
	prov := skill.NewProvider(filepath.Join(v.skillsRoot(), agentID), v.log)
	ts := prov.Tools()
	out := make([]agentsvc.ResourceInfo, 0, len(ts))
	for _, t := range ts {
		s := t.Schema()
		id := strings.TrimPrefix(s.Name, "skill_")
		out = append(out, agentsvc.ResourceInfo{
			ID:          id,
			Name:        skillNameFromSchema(s.Description),
			Description: s.Description,
			Type:        "skill",
		})
	}
	return out
}

// McpTools 返回该域已启用 MCP server 的工具清单（工具名 mcp_<server>_<tool>）。
// 仅读配置文件中的 discovered_tools，不建连接、不触发发现。
func (v *domainViewer) McpTools(agentID string) []agentsvc.ToolInfo {
	data, err := os.ReadFile(v.mcpFileFor(agentID))
	if err != nil {
		return nil // 该域无 MCP 配置（正常）：清单为空
	}
	servers, err := mcp.ParseServersJSON(data)
	if err != nil {
		v.log.Warn("domain mcp config parse failed",
			zap.String("agent_id", agentID),
			zap.String("file", v.mcpFileFor(agentID)),
			zap.Error(err))
		return nil
	}
	var out []agentsvc.ToolInfo
	for i := range servers {
		s := &servers[i]
		if !s.IsEnabled() {
			continue
		}
		srv := mcp.SanitizeName(s.Name)
		for _, t := range s.DiscoveredTools {
			out = append(out, agentsvc.ToolInfo{
				Name:        "mcp_" + srv + "_" + mcp.SanitizeName(t.Name),
				Description: t.Description,
				External:    false,
			})
		}
	}
	return out
}

// Defaults 返回该域默认会话配置（agent_defaults.json；缺文件/空内容 = 零值）。
// 解析失败仅告警并返回零值，不让配置错误阻断会话创建（默认值可选）。
func (v *domainViewer) Defaults(agentID string) agentsvc.AgentDefaults {
	file := v.defaultsFileFor(agentID)
	data, err := os.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			v.log.Warn("read agent defaults failed",
				zap.String("agent_id", agentID),
				zap.String("file", file),
				zap.Error(err))
		}
		return agentsvc.AgentDefaults{}
	}
	d, err := agentsvc.ParseDefaultsJSON(data)
	if err != nil {
		v.log.Warn("agent defaults parse failed",
			zap.String("agent_id", agentID),
			zap.String("file", file),
			zap.Error(err))
		return agentsvc.AgentDefaults{}
	}
	return d
}

// mcpFileFor 返回指定域的 MCP 配置文件（<mcpDir>/<agent_id>/<文件名>），
// 与 adminsvc.McpStore.For 落盘规则一致。
func (v *domainViewer) mcpFileFor(agentID string) string {
	file := v.cfg.Agent.McpConfigFile
	if file == "" {
		file = "mcp_servers.json"
	}
	return filepath.Join(filepath.Dir(file), agentID, filepath.Base(file))
}

// defaultsFileFor 返回指定域的默认会话配置文件（与 mcp_servers.json 同目录）。
func (v *domainViewer) defaultsFileFor(agentID string) string {
	return filepath.Join(filepath.Dir(v.mcpFileFor(agentID)), agentsvc.DefaultsFileName)
}

// skillNameFromSchema 从技能工具描述"技能【<名>】：<说明>…"提取原始技能名。
func skillNameFromSchema(desc string) string {
	const open = "技能【"
	const close = "】"
	i := strings.Index(desc, open)
	if i < 0 {
		return ""
	}
	rest := desc[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
