import { Settings2 } from 'lucide-react'
import SettingsPanel from './settings-panel'
import type { MenuItem } from '../types'

/**
 * 菜单功能 · 设置（子界面模式：renderPanel 打开设置页，返回/关闭回主界面）。
 * 面板组件独立于 settings-panel.tsx（fast-refresh 兼容，组件与菜单配置分离）。
 */
export const settingsMenu: MenuItem = {
  key: 'settings',
  label: '设置',
  description: '账号、服务器地址与门户配置',
  icon: Settings2,
  group: 'system',
  renderPanel: (ctx) => <SettingsPanel ctx={ctx} />,
}
