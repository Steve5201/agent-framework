// external.ts 单元测试：外部链接识别 + 打开策略（非 Tauri 环境回退 window.open）。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isExternalLink, openExternal } from './external'

describe('isExternalLink', () => {
  it('识别 http/https/mailto/tel 为外部链接', () => {
    expect(isExternalLink('https://www.example.edu.cn')).toBe(true)
    expect(isExternalLink('http://example.com')).toBe(true)
    expect(isExternalLink('mailto:hi@example.edu.cn')).toBe(true)
    expect(isExternalLink('tel:+861234567890')).toBe(true)
  })

  it('相对路径 / 锚点 / 其它协议不算外部链接', () => {
    expect(isExternalLink('/login')).toBe(false)
    expect(isExternalLink('#anchor')).toBe(false)
    expect(isExternalLink('ftp://example.com')).toBe(false)
    expect(isExternalLink('')).toBe(false)
    expect(isExternalLink(undefined)).toBe(false)
  })
})

describe('openExternal（浏览器环境）', () => {
  afterEach(() => vi.restoreAllMocks())

  it('非 Tauri 环境走 window.open 新标签页（noopener）', async () => {
    const spy = vi.spyOn(window, 'open').mockReturnValue(null)
    await openExternal('https://www.example.edu.cn')
    expect(spy).toHaveBeenCalledWith('https://www.example.edu.cn', '_blank', 'noopener,noreferrer')
  })

  it('空 URL 直接返回，不触发任何打开动作', async () => {
    const spy = vi.spyOn(window, 'open')
    await openExternal('')
    expect(spy).not.toHaveBeenCalled()
  })
})
