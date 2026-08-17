// defaults.go —— 智能体域默认会话配置的解析与"创建时快照固化"。
//
// 快照语义：默认配置不参与运行时动态合并。新建会话（CreateSession）时把
// 管理端默认配置一次性合并进会话 config 并落库（快照），之后该会话始终
// 以这份快照为准——管理端再改默认配置只影响后续新建会话，旧会话不受影响。
//
// 普通用户对本会话的实时修改走 UpdateSessionConfig（全量替换用户可配字段），
// 管理员级字段（max_rounds/max_messages/max_thinking_rounds）服务端保留
// 快照原值，不允许用户改动。
//
// 文件布局与 skill/MCP 保持一致的文件态配置平面：
//
//	<mcpDir>/<agent_id>/agent_defaults.json
//
// 缺文件/空内容 = 无默认（零值）；非法 JSON = 返回错误（调用方记录后忽略，
// 不让配置错误阻断会话创建——默认值是可选的，显式配置始终优先）。
package agentsvc

import (
	"bytes"
	"encoding/json"
)

// DefaultsFileName 智能体域默认配置文件（与 mcp_servers.json 同目录）。
const DefaultsFileName = "agent_defaults.json"

// ParseDefaultsJSON 解析智能体默认会话配置。
// 空内容（含纯空白）返回零值，不视为错误；非法 JSON 返回 error。
func ParseDefaultsJSON(data []byte) (AgentDefaults, error) {
	var d AgentDefaults
	if len(bytes.TrimSpace(data)) == 0 {
		return d, nil
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return d, err
	}
	return d, nil
}

// ApplyDefaults 把默认配置合并进会话配置（仅用于会话创建时的快照固化）。
// 规则：
//   - 仅填充 cfg 中未设置的字段（nil / 未置 presence 标记）；
//   - presence 标记（EnabledResourcesSet / KBIDsSet / MCPServersSet）为 true 时，
//     即使数组为空也要显式写入——"默认全不选"是合法默认，不能因空数组被忽略；
//   - 管理员级字段（MaxRounds/MaxMessages/MaxThinkingRounds）0 = 未设置，
//     此时取默认值（仍可能为 0，装配层再回退服务实例默认）。
//
// 返回合并结果（入参值拷贝，不修改调用方对象）。
func ApplyDefaults(cfg SessionConfig, d AgentDefaults) SessionConfig {
	if d.EnabledTools != nil && cfg.EnabledTools == nil {
		cfg.EnabledTools = d.EnabledTools
	}
	if cfg.EnabledResources == nil && !cfg.EnabledResourcesSet {
		if d.EnabledResourcesSet {
			cfg.EnabledResources = d.EnabledResources
			cfg.EnabledResourcesSet = true
		} else if d.EnabledResources != nil {
			cfg.EnabledResources = d.EnabledResources
		}
	}
	if d.Thinking != nil {
		if cfg.Thinking == nil {
			cfg.Thinking = d.Thinking
		} else if cfg.Thinking.ReasoningEffort == "" {
			// reasoning_effort 空串 = "未指定强度，跟随默认"：回填默认强度。
			cfg.Thinking.ReasoningEffort = d.Thinking.ReasoningEffort
		}
	}
	if cfg.KBIDs == nil && !cfg.KBIDsSet {
		if d.KBIDsSet {
			cfg.KBIDs = d.KBIDs
			cfg.KBIDsSet = true
		} else if d.KBIDs != nil {
			cfg.KBIDs = d.KBIDs
		}
	}
	if cfg.MCPServers == nil && !cfg.MCPServersSet {
		if d.MCPServersSet {
			cfg.MCPServers = d.MCPServers
			cfg.MCPServersSet = true
		} else if d.MCPServers != nil {
			cfg.MCPServers = d.MCPServers
		}
	}
	// 管理员级字段：会话侧 0 = 未显式设置 → 采纳默认值（0 则继续为空，
	// 由装配层回退服务实例默认）。
	if cfg.MaxRounds == 0 {
		cfg.MaxRounds = d.MaxRounds
	}
	if cfg.MaxMessages == 0 {
		cfg.MaxMessages = d.MaxMessages
	}
	if cfg.MaxThinkingRounds == 0 {
		cfg.MaxThinkingRounds = d.MaxThinkingRounds
	}
	// 模型名：会话未显式选择时继承默认（空串 = 未设置，回退实例默认）。
	if cfg.Model == "" {
		cfg.Model = d.Model
	}
	// 运行模式：会话未显式选择时继承默认（空串 = single）。
	if cfg.Mode == "" {
		cfg.Mode = d.Mode
	}
	// 编排方案：会话未显式选择时继承默认（空串 = fixed）。
	if cfg.OrchestratePlan == "" {
		cfg.OrchestratePlan = d.OrchestratePlan
	}
	// 能力/技能独立 presence 标记（P3 反馈）：默认全不选随快照固化到新会话。
	// 会话侧未显式设置（标记为 false）时继承默认标记；已显式设置（无论值）
	// 以会话为准（ApplyDefaults 只填充未设置字段）。
	if !cfg.EnabledCapabilitiesSet && d.EnabledCapabilitiesSet {
		cfg.EnabledCapabilitiesSet = true
	}
	if !cfg.EnabledSkillsSet && d.EnabledSkillsSet {
		cfg.EnabledSkillsSet = true
	}
	return cfg
}

// IsEmpty 默认配置是否为空（无任何可应用的默认项）。
func (d AgentDefaults) IsEmpty() bool {
	return d.EnabledTools == nil &&
		d.EnabledResources == nil &&
		!d.EnabledResourcesSet &&
		d.Thinking == nil &&
		d.KBIDs == nil &&
		!d.KBIDsSet &&
		d.MCPServers == nil &&
		!d.MCPServersSet &&
		d.MaxRounds == 0 &&
		d.MaxMessages == 0 &&
		d.MaxThinkingRounds == 0 &&
		d.Model == "" &&
		!d.EnabledCapabilitiesSet &&
		!d.EnabledSkillsSet &&
		d.Mode == "" &&
		d.OrchestratePlan == ""
}
