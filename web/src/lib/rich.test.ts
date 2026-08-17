import { beforeEach, describe, expect, it, vi } from 'vitest'

// downloadUrl 桌面端分支（Tauri）：mock openExternal，断言直接跳系统浏览器、不走 fetch。
const mocks = vi.hoisted(() => ({ openExternal: vi.fn() }))
vi.mock('./external', () => ({ openExternal: mocks.openExternal }))

import { downloadUrl, isVideoUrl, parseEChartsOption, sanitizeSVG } from './rich'

describe('downloadUrl', () => {
  beforeEach(() => {
    mocks.openExternal.mockClear()
    // 默认清理 Tauri 环境标记，避免影响其它用例
    delete (window as unknown as Record<string, unknown>).__TAURI_INTERNALS__
  })

  it('Tauri 环境直接跳系统默认浏览器（openExternal），不 fetch Blob', () => {
    ;(window as unknown as Record<string, unknown>).__TAURI_INTERNALS__ = {}
    const fetchSpy =
      typeof globalThis.fetch === 'function' ? vi.spyOn(globalThis, 'fetch') : null
    downloadUrl('http://127.0.0.1:8080/files/a.png', 'a.png')
    expect(mocks.openExternal).toHaveBeenCalledWith('http://127.0.0.1:8080/files/a.png')
    expect(fetchSpy ? fetchSpy.mock.calls.length : 0).toBe(0)
    fetchSpy?.mockRestore()
  })

  it('浏览器环境保持 fetch Blob 下载，跨域失败回退 openExternal', async () => {
    const fetchSpy =
      typeof globalThis.fetch === 'function'
        ? vi.spyOn(globalThis, 'fetch').mockRejectedValue(new TypeError('Failed to fetch'))
        : null
    downloadUrl('https://x/a.png', 'a.png')
    await vi.waitFor(() => expect(mocks.openExternal).toHaveBeenCalledWith('https://x/a.png'))
    fetchSpy?.mockRestore()
  })
})

describe('isVideoUrl', () => {
  it('识别视频扩展名（忽略查询串/锚点）', () => {
    expect(isVideoUrl('https://x.com/a.mp4')).toBe(true)
    expect(isVideoUrl('a.webm?t=1')).toBe(true)
    expect(isVideoUrl('file:///d:/videos/课.mov')).toBe(true)
    expect(isVideoUrl('https://x.com/b.png')).toBe(false)
    expect(isVideoUrl('https://x.com/c.txt')).toBe(false)
  })
})

describe('parseEChartsOption', () => {
  it('解析合法 option 对象', () => {
    const o = parseEChartsOption('{"series":[{"type":"bar","data":[1,2,3]}]}')
    expect(o).not.toBeNull()
    expect((o as Record<string, unknown>).series).toHaveLength(1)
  })
  it('非法输入返回 null', () => {
    expect(parseEChartsOption('{bad json')).toBeNull()
    expect(parseEChartsOption('42')).toBeNull()
    expect(parseEChartsOption('"str"')).toBeNull()
    expect(parseEChartsOption('')).toBeNull()
    expect(parseEChartsOption('{"a": 1, 中文引号}')).toBeNull()
  })
  it('宽松解析：剥离 // 与 /* */ 注释', () => {
    const o = parseEChartsOption('{\n  // 销量\n  "series": [1, 2] /* 注释 */\n}')
    expect(o).not.toBeNull()
    expect((o as Record<string, unknown>).series).toEqual([1, 2])
  })
  it('宽松解析：不误伤字符串内的 http://', () => {
    const o = parseEChartsOption('{"url": "http://x.com/a?t=1"}')
    expect(o).not.toBeNull()
    expect((o as Record<string, unknown>).url).toBe('http://x.com/a?t=1')
  })
  it('宽松解析：容忍尾逗号', () => {
    const o = parseEChartsOption('{"data": [1, 2,],}')
    expect(o).not.toBeNull()
    expect((o as Record<string, unknown>).data).toEqual([1, 2])
  })
  it('宽松解析：单引号字符串转双引号', () => {
    const o = parseEChartsOption("{'name': '柱状图'}")
    expect(o).not.toBeNull()
    expect((o as Record<string, unknown>).name).toBe('柱状图')
  })
})

describe('sanitizeSVG', () => {
  it('剔除 script 标签与事件属性', () => {
    const out = sanitizeSVG('<svg><script>alert(1)</script><circle onclick="x()" /></svg>')
    expect(out).not.toContain('script')
    expect(out).not.toContain('onclick')
    expect(out).toContain('<circle')
  })
  it('剔除 javascript: 链接', () => {
    expect(sanitizeSVG('<a href="javascript:alert(1)">x</a>')).not.toContain('javascript:')
  })
  it('保留正常 SVG 结构', () => {
    const src = '<svg width="10"><rect width="10" height="10" fill="#f00" /></svg>'
    expect(sanitizeSVG(src)).toContain('<rect')
  })
})
