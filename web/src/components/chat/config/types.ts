import type { ReactNode } from 'react'
import type { Session, User } from '@/types/api'

/**
 * 配置项可见性判定所需的上下文。
 * 后续扩展（如会话级 MCP/知识库/智能体/大模型）直接在这里加字段，
 * 注册表无需改动——见 config/registry.tsx 的可见属性用法。
 */
export interface ConfigCtx {
  /** 当前登录用户（含 tags，见 User 类型；后端未下发 tags 时为 undefined） */
  user: User | null
  /** 当前活动会话（无会话时多数配置项不可用） */
  activeSession: Session | null
}

/**
 * 标准配置项：输入区一个配置按钮 + 一个独立配置弹窗。
 *
 * 【标准化开发管线】新增一个"对话前配置"选项时：
 *   1. 在 registry.tsx 注册一条 ConfigItem：key 唯一 / 图标 / 可见属性 /
 *      renderDialog 指向独立弹窗组件；
 *   2. 弹窗组件自管内部状态，经 chat store 的 updateConfig 提交（参照
 *      CapabilitiesDialog / ThinkingDialog / SkillsDialog）；
 *   3. 无需改 ConfigButtonArea 骨架。
 *
 * visible 是配置按钮的"可见属性"：后期要面向特定用户群体隐藏某配置
 * （如不允许普通用户切换大模型 / 智能体），改这一条即可；判定依据是
 * ctx.user.tags（见 user_profile 的用户标签方案），或管理员全局开关。
 */
export interface ConfigItem {
  /** 唯一标识（按钮 key / 弹窗 key） */
  key: string
  /** 悬浮提示与无障碍标签 */
  label: string
  icon: ReactNode
  /** 可见属性：false = 不渲染该按钮（可依据用户标签/角色/会话状态判定） */
  visible: (ctx: ConfigCtx) => boolean
  /**
   * 会话依赖：true 表示配置必须落到具体会话上（无活动会话时，
   * ConfigButtonArea 点击会先自动新建会话再弹窗）。false 的项
   * （如切换智能体）不依赖会话，点击不触发建会话。
   */
  requiresSession?: boolean
  /** 渲染独立配置弹窗（宿主负责开关与 key 重挂载重置内部状态） */
  renderDialog: (ctx: ConfigCtx, onClose: () => void) => ReactNode
}
