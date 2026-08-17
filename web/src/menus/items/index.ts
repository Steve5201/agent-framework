// ---------------------------------------------------------------------------
// 菜单功能注册入口：引入各功能文件触发 registerMenu() 自注册（模块加载时执行）。
// 【新增菜单功能的标准流程】
//   1. 在本目录新建功能文件（如 items/theme.ts），registerMenu(menuItem) 注册；
//   2. 在此 import './theme' 一行即可接入——无需改面板 / 按钮 / 任何消费方。
// 菜单项三要素：roles（角色白名单）、visible（环境谓词）、action / renderPanel（行为）。
// 契约见 ../types.ts。
// ---------------------------------------------------------------------------
import { registerMenu } from '../registry'
import { settingsMenu } from './settings'
import { adminMenu } from './admin'
import { logoutMenu } from './logout'
import { quitAppMenu } from './quit-app'

// 按展示顺序注册：系统（设置）→ 账号（管理端）→ 退出（登出 / 退出应用）
registerMenu(settingsMenu)
registerMenu(adminMenu)
registerMenu(logoutMenu)
registerMenu(quitAppMenu)
