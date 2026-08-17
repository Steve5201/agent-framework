import { ShieldCheck } from 'lucide-react'
import { ADMIN_ROLES, type MenuItem } from '../types'

/** 菜单功能 · 进入管理端（仅管理员角色可见，普通用户列表不渲染此项）。 */
export const adminMenu: MenuItem = {
  key: 'admin',
  label: '管理端',
  description: '后台管理：用户 / 模型 / 智能体 / 数据管理',
  icon: ShieldCheck,
  group: 'account',
  roles: ADMIN_ROLES,
  action: (ctx) => {
    ctx.navigate('/admin')
  },
}
