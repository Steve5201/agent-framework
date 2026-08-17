// ---------------------------------------------------------------------------
// 外部链接打开统一出口（浏览器 / Tauri 双环境）
//
// 背景：桌面端（Tauri WebView2）里点击 <a href> 默认会在当前 webview 内导航，
// 导致整个聊天界面被目标网页替换（应用"消失"）。本模块统一拦截：
//   - Tauri 环境：invoke 自建命令 open_external，交给系统默认浏览器打开；
//   - 浏览器环境：window.open 开新标签页（noopener，防反向 tabnabbing）。
//
// 用法：RichContent 渲染 Markdown 链接时判断 isExternalLink，命中则阻止默认
// 行为并调用 openExternal；图片/视频下载的跨域兜底同样走这里。
// ---------------------------------------------------------------------------
import { isTauri } from './storage'

/**
 * 打开外部链接（http/https/mailto/tel 等）。
 * 任何情况下都不会劫持当前应用界面。
 */
export async function openExternal(url: string): Promise<void> {
  if (!url) return
  if (isTauri()) {
    try {
      const { invoke } = await import('@tauri-apps/api/core')
      await invoke('open_external', { url })
      return
    } catch (err) {
      // invoke 失败（如命令未注册/被拒）回退到新窗口打开，不抛给调用方
      console.error('[external] Tauri 打开外部链接失败，回退新窗口：', err)
    }
  }
  window.open(url, '_blank', 'noopener,noreferrer')
}

/** 判断 href 是否外部链接（http/https/mailto/tel）。
 *  相对路径 / 站内锚点 / 其它协议返回 false，交给浏览器默认行为。 */
export function isExternalLink(href?: string): boolean {
  if (!href) return false
  return /^(https?:|mailto:|tel:)/i.test(href)
}
