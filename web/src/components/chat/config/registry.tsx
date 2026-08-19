import { Bot, Cpu, Database, Flame, Network, Settings2, Shield, Sparkles, Wand2, Workflow } from 'lucide-react'
import type { ConfigItem } from './types'
import CapabilitiesDialog from './CapabilitiesDialog'
import ThinkingDialog from './ThinkingDialog'
import SkillsDialog from './SkillsDialog'
import KBDialog from './KBDialog'
import MCPDialog from './MCPDialog'
import LLMDialog from './LLMDialog'
import ModeDialog from './ModeDialog'
import SandboxDialog from './SandboxDialog'
import FreeModeDialog from './FreeModeDialog'
import AgentSwitcherDialog from '../AgentSwitcher'
import { getUserAgentId, isAllAgentScope, isAdminRole } from '@/lib/roles'
import { isTauri } from '@/lib/localTools'

/**
 * 配置项注册表：输入区配置按钮的单一事实来源。
 *
 * 【标准化开发管线】新增"对话前配置"选项 = 在数组里加一条：
 *   1. key 唯一；2. 图标 + 文案；3. visible 可见属性（可依据
 *      ctx.user.tags / 会话状态 / 角色判定，false = 按钮不渲染）；
 *   4. renderDialog 指向独立弹窗（宿主负责开关与 key 重挂载）。
 * 无需改 ConfigButtonArea 骨架。
 *
 * 已落地：能力开关 / 思考模式 / 技能 / 切换智能体（combobox 单选，仅具体
 * 智能体）/ 知识库（会话级勾选，经 /v1/agent/kbs 普通接口按会话域拉取，
 * 不勾选 = 本会话不使用知识库检索）/ MCP 连接（管理员会话级勾选，普通用户只读）/
 * 大模型（会话级单选，列表来自公开 /v1/models，选中默认 = 不锁定）。
 */
export const configRegistry: ConfigItem[] = [
  {
    key: 'capabilities',
    label: '能力开关',
    icon: <Cpu className="h-4 w-4" />,
    // 始终可见（游客由 ChatInput canConfigure 整体隐藏）：无活动会话时
    // 点击会自动新建会话，保证"直接输入开新聊"的用户也能配置对话选项。
    visible: () => true,
    requiresSession: true,
    renderDialog: ({ activeSession }, onClose) => (
      <CapabilitiesDialog sessionConfig={activeSession?.config} onClose={onClose} />
    ),
  },
  {
    key: 'thinking',
    label: '思考模式',
    icon: <Sparkles className="h-4 w-4" />,
    visible: () => true,
    requiresSession: true,
    renderDialog: ({ activeSession }, onClose) => (
      <ThinkingDialog sessionConfig={activeSession?.config} onClose={onClose} />
    ),
  },
  {
    key: 'mode',
    label: '运行模式',
    icon: <Workflow className="h-4 w-4" />,
    visible: () => true,
    requiresSession: true,
    renderDialog: ({ activeSession }, onClose) => (
      <ModeDialog sessionConfig={activeSession?.config} onClose={onClose} />
    ),
  },
  {
    key: 'skills',
    label: '技能',
    icon: <Wand2 className="h-4 w-4" />,
    visible: () => true,
    requiresSession: true,
    renderDialog: ({ activeSession }, onClose) => (
      <SkillsDialog agentId={activeSession?.agent_id} sessionConfig={activeSession?.config} onClose={onClose} />
    ),
  },
  // ---- MCP 连接：管理员会话级勾选（mcp_servers）；普通用户只读（全部启用） ----
  {
    key: 'mcp',
    label: 'MCP 连接',
    icon: <Network className="h-4 w-4" />,
    visible: () => true,
    requiresSession: true,
    renderDialog: ({ activeSession }, onClose) => (
      <MCPDialog agentId={activeSession?.agent_id} sessionConfig={activeSession?.config} onClose={onClose} />
    ),
  },
  // ---- 知识库：会话级勾选（普通接口 /v1/agent/kbs，域由后端锁定） ----
  {
    key: 'kb',
    label: '知识库',
    icon: <Database className="h-4 w-4" />,
    visible: () => true,
    requiresSession: true,
    renderDialog: ({ activeSession }, onClose) => (
      <KBDialog sessionConfig={activeSession?.config} agentId={activeSession?.agent_id} onClose={onClose} />
    ),
  },
  {
    key: 'llm',
    label: '大模型',
    icon: <Settings2 className="h-4 w-4" />,
    visible: () => true,
    requiresSession: true,
    renderDialog: ({ activeSession }, onClose) => (
      <LLMDialog sessionConfig={activeSession?.config} agentId={activeSession?.agent_id} onClose={onClose} />
    ),
  },
  // ---- 沙盒配置：仅管理员（agent_admin/super_admin/admin）可见可改。
  // 普通用户配置区不展示该按钮；后端对非管理员角色强制覆盖回快照原值（双保险）。
  {
    key: 'sandbox',
    label: '沙盒配置',
    icon: <Shield className="h-4 w-4" />,
    visible: ({ user }) => isAdminRole(user?.role),
    requiresSession: true,
    renderDialog: ({ activeSession, user }, onClose) => (
      <SandboxDialog sessionConfig={activeSession?.config} role={user?.role} onClose={onClose} />
    ),
  },
  // ---- 自由模式：纯本地个人化开关（仅桌面端可见，不依赖会话）。开启后本地
  // shell 不询问、不限超时；每次开启都在弹窗内风险提示并二次确认。
  {
    key: 'freemode',
    label: '自由模式',
    icon: <Flame className="h-4 w-4" />,
    visible: () => isTauri(),
    renderDialog: (_ctx, onClose) => <FreeModeDialog onClose={onClose} />,
  },
  {
    key: 'agent',
    label: '切换智能体',
    icon: <Bot className="h-4 w-4" />,
    // 仅最高超管（全门户标识 '*'）可见：切换任意门户聊天；其它角色聊天界面
    // 仍禁止智能体选择（只能用自己绑定的门户）。无需活动会话即可切换。
    // 排列在配置项最右：入口性质与其它"会话配置"不同，独立分隔（见上方图标分隔线）。
    visible: ({ user }) => isAllAgentScope(getUserAgentId(user)),
    renderDialog: (_ctx, onClose) => <AgentSwitcherDialog onClose={onClose} />,
  },
]
