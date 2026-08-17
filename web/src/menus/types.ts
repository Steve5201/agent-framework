import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import type { NavigateFunction, Location } from 'react-router-dom'
import type { User } from '@/types/api'

// ---------------------------------------------------------------------------
// 菜单系统契约（web/src/menus，标准化菜单功能开发流程）
// ---------------------------------------------------------------------------
// 菜单 = 主界面统一的功能收纳区（对应桌面/移动端右上角或用户区的"更多"入口）。
// 与 configRegistry（输入区配置项）同源的设计哲学：注册表驱动 + 功能文件解耦。
//
// 【新增一个菜单功能的标准流程】
//   1. 在 src/menus/items/ 下新建独立文件，定义并 `registerMenu(menuItem)`；
//   2. 在 src/menus/items/index.ts 引入该文件（触发注册）；
//   3. 按需配置三个维度，其余缺省：
//        roles   —— 可见角色白名单（缺省 = 全部登录用户；游客不渲染菜单面板）
//        visible —— 环境可见性（如桌面端专用 `isTauri()`）
//        action / renderPanel —— 二选一：
//          · action      = 点击直接执行（导航 / 登出 / 退出程序）
//          · renderPanel = 打开该功能的子界面（如"设置"），界面内提供
//                          ctx.back() 返回菜单列表、ctx.close() 关闭回主界面
//   4. 无需修改 Panel / MenuButton / 任何消费方——过滤与渲染完全由注册表驱动。
// ---------------------------------------------------------------------------

/** 管理员角色白名单（复用 roles.ts 的 isAdminRole 语义，供菜单过滤） */
export const ADMIN_ROLES = ['super_admin', 'agent_admin', 'admin'] as const

/** 菜单功能在面板内的分组（影响展示顺序与间距） */
export type MenuGroup = 'system' | 'account' | 'danger'

/** 菜单项执行上下文：由菜单面板注入，功能文件只依赖此契约（解耦） */
export interface MenuCtx {
  user: User | null
  navigate: NavigateFunction
  location: Location
  logout: () => Promise<void>
  /** 返回菜单列表（子界面专用） */
  back: () => void
  /** 关闭整个菜单面板，回到主界面 */
  close: () => void
}

/** 菜单项定义（功能文件通过 registerMenu 注册） */
export interface MenuItem {
  /** 全局唯一键（面板内 active 子界面定位、测试断言用） */
  key: string
  /** 菜单列表展示名 */
  label: string
  /** 列表副标题（一句话说明） */
  description?: string
  icon?: LucideIcon
  /** 分组：system（设置类）/ account（账号类）/ danger（退出类） */
  group: MenuGroup
  /** 可见角色白名单；缺省 = 全部登录用户可见（游客无菜单面板） */
  roles?: readonly string[]
  /** 环境可见性谓词（如桌面端专用）；缺省 = 全环境可见 */
  visible?: () => boolean
  /** 点击直接执行（导航 / 登出 / 退出程序）。异步可 await，面板在执行完成前保持。 */
  action?: (ctx: MenuCtx) => void | Promise<void>
  /** 打开子界面：渲染该功能界面（设置等需要表单/更多空间的场景）。
   *  界面内调用 ctx.back() 返回菜单列表；ctx.close() 关闭面板回主界面。 */
  renderPanel?: (ctx: MenuCtx) => ReactNode
}
