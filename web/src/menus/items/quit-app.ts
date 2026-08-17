import { Power } from 'lucide-react'
import { isTauri } from '@/lib/storage'
import type { MenuItem } from '../types'

/** 菜单功能 · 退出桌面应用（仅 Tauri 桌面端渲染，浏览器无此概念）。 */
export const quitAppMenu: MenuItem = {
  key: 'quit-app',
  label: '退出应用',
  description: '关闭桌面应用进程',
  icon: Power,
  group: 'danger',
  visible: () => isTauri(),
  action: async () => {
    try {
      const { invoke } = await import('@tauri-apps/api/core')
      await invoke('app_exit')
    } catch {
      /* 非 Tauri 环境或 IPC 失败：静默忽略 */
    }
  },
}
