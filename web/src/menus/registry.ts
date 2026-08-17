import type { MenuItem } from './types'

// ---------------------------------------------------------------------------
// 菜单注册表：全局单例，按注册顺序保序。
// 功能文件在模块加载时 registerMenu() 自注册（见 items/index.ts），
// 消费方（MenuPanel）只读取 filteredMenuItems()，无需感知具体功能。
// ---------------------------------------------------------------------------

const registry = new Map<string, MenuItem>()

/** 注册一个菜单项（重复 key 时后者覆盖，便于测试注入） */
export function registerMenu(item: MenuItem): void {
  registry.set(item.key, item)
}

/** 按注册顺序取出全部菜单项（供面板/测试使用） */
export function allMenuItems(): MenuItem[] {
  return [...registry.values()]
}

/**
 * 过滤出当前用户可见的菜单项：
 *  - roles 白名单：item.roles 缺省 = 全部登录用户；非空 = user.role 需在列。
 *  - visible 环境谓词：桌面端专用等功能在此过滤。
 */
export function filterMenuItems(user: { role?: string } | null): MenuItem[] {
  if (!user) return [] // 游客不渲染菜单面板（注册表层兜底，UI 层同样不渲染入口）
  return [...registry.values()].filter((item) => {
    if (item.roles && (!user.role || !item.roles.includes(user.role))) return false
    if (item.visible && !item.visible()) return false
    return true
  })
}
