import { LogOut } from 'lucide-react'
import { DEFAULT_AGENT_ID, getUserAgentId } from '@/lib/roles'
import type { MenuItem } from '../types'

/**
 * 菜单功能 · 退出登录：清令牌后回到游客模式。
 * 按用户归属门户落地——超管 = 自己的 '*' 域（流程统一：超管在本域登录页重新登录）；
 * 普通管理员/用户 = 各自归属的智能体域；无归属 → 默认域。
 * 原在智能体域 → 留在该域（超管 '*' 域游客态禁止对话，仅展示登录入口）；
 * 原在管理端/配置页 → 落地用户归属门户。
 */
export const logoutMenu: MenuItem = {
  key: 'logout',
  label: '退出登录',
  description: '清除登录令牌，回到游客模式',
  icon: LogOut,
  group: 'danger',
  action: async (ctx) => {
    const userAgentId = getUserAgentId(ctx.user) || DEFAULT_AGENT_ID
    const isAgentRoute = ctx.location.pathname.startsWith('/agent/')
    await ctx.logout()
    ctx.navigate(isAgentRoute ? ctx.location.pathname : `/agent/${userAgentId}`, { replace: true })
  },
}
