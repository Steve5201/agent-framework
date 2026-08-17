// ---------------------------------------------------------------------------
// 角色与资源域（阶段3·多租户）
// ---------------------------------------------------------------------------
import type { UserTag } from '@/types/api'

/** 管理员角色（超管类 + 普通管理员；均有 /v1/admin/* 访问权） */
export type AdminRole = 'super_admin' | 'agent_admin' | 'admin'

/** 默认智能体域：后端多租户兜底（authsvc 播种 / rag / adminsvc 三处同值） */
export const DEFAULT_AGENT_ID = 'tutor'

/** 超管全门户标识：最高超管的 agent 标签值（含义 = 全部智能体）。
 *  与后端 authsvc allAgentID 保持一致；格式与普通门户标签一致，仅值不同。
 *  识别到该值 → 聊天界面显示智能体选择器（可切换任意门户）；其它门户禁止切换。 */
export const ALL_AGENT_ID = '*'

/** 是否全门户标识（超管专属门户 /agent/*、/login/*） */
export function isAllAgentScope(agentId?: string | null): boolean {
  return agentId === ALL_AGENT_ID
}

/** 角色展示名 */
export const ROLE_LABELS: Record<string, string> = {
  super_admin: '最高超管',
  agent_admin: '智能体超管',
  admin: '普通管理员',
  user: '普通用户',
}

/** 是否管理员角色（决定管理端入口与页面访问权） */
export function isAdminRole(role?: string): role is AdminRole {
  return role === 'super_admin' || role === 'agent_admin' || role === 'admin'
}

/** 是否最高超管（唯一拥有智能体管理模块 + 任意资源域选择权） */
export function isSuperAdmin(role?: string): boolean {
  return role === 'super_admin'
}

/** 是否智能体超管或以上（可管理自己智能体组的用户） */
export function canManageUsers(role?: string): boolean {
  return role === 'super_admin' || role === 'agent_admin'
}

/** 从用户标签解析智能体归属（{key:'agent', value:<id>}）；未绑定回退默认域。
 *  普通管理员（agent_admin/admin）的资源域固定为该值——前端据此提交
 *  agent_id，后端亦强制锁定，双保险防越权。 */
export function getUserAgentId(user?: { tags?: UserTag[] } | null): string {
  const tag = user?.tags?.find((t) => t.key === 'agent')
  return tag?.value || DEFAULT_AGENT_ID
}

/** 角色归属会话域（登录落地 / 管理端对话无记忆时的兜底）：
 *  - 超管 → '*'（全部域，跨域对话主场）；
 *  - agent_admin/admin → 账号绑定智能体域；
 *  - 普通用户/未登录 → ''（不参与管理端路径；调用方自行按门户域处理）。
 *  修复：管理员登录后一律落地 /admin/chat（scope=''），而会话实际归属在
 *  具体智能体域（超管门户 '*' 下新建会话回退默认域），导致首屏列表为空，
 *  必须手动切一次 '*' 才出现（方案 B+C）。 */
export function getHomeScope(user?: { role?: string; tags?: UserTag[] } | null): string {
  if (!user) return ''
  if (isSuperAdmin(user.role)) return ALL_AGENT_ID
  if (isAdminRole(user.role)) return getUserAgentId(user)
  return ''
}

// ---------------------------------------------------------------------------
// 记住的上次选择智能体（超管切换器）
// ---------------------------------------------------------------------------
// 超管切换器选中的智能体持久化到 localStorage，供两类场景恢复：
//   - AgentSwitcher：管理端对话等无 URL 域的场景默认选中它；
//   - ChatPage(mode="admin")：管理端对话的会话域回退到它——保证从管理端
//     返回对话界面时，新建会话与会话列表严格遵循切换器选中的智能体归属。
const LAST_AGENT_KEY = 'agent.last_agent'

/** 记住本次选中的智能体（下次在管理端对话等无域场景打开时默认选中它）。 */
export function rememberAgent(id: string) {
  try {
    localStorage.setItem(LAST_AGENT_KEY, id)
  } catch {
    /* 隐私模式等存储不可用：忽略，仅影响记忆 */
  }
}

/** 读取记住的上次选择智能体 id（无或读取失败返回空串）。 */
export function loadRememberedAgent(): string {
  try {
    return localStorage.getItem(LAST_AGENT_KEY) ?? ''
  } catch {
    return ''
  }
}
