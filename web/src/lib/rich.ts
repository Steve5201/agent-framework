/** 富媒体渲染协议辅助函数（需求 9）。协议说明见 backend agentsvc/prompt.go 与 docs/api/web.md。 */
import { openExternal } from './external'

/** 视频扩展名：以这些结尾的 Markdown 图片 URL 会被渲染为 <video>。 */
const VIDEO_EXT_RE = /\.(mp4|webm|ogg|mov|m4v)$/i

/** 判断 URL 是否视频资源（去掉查询串/锚点后看扩展名）。 */
export function isVideoUrl(url: string): boolean {
  return VIDEO_EXT_RE.test(url.split(/[?#]/)[0])
}

/** 触发浏览器下载 Blob（内存 URL 用后即回收）。 */
export function downloadBlob(data: Blob, filename: string): void {
  const url = URL.createObjectURL(data)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

/** 下载网络/本地资源到本地：fetch 转 Blob 后下载；
 *  跨域且无 CORS 时 fetch 会失败 → 退化为新窗口/系统浏览器打开，由用户另存。
 *  注意：不能直接 window.open（Tauri 里会劫持当前 webview 导航），统一走 openExternal。 */
export function downloadUrl(url: string, filename: string): void {
  fetch(url)
    .then((r) => r.blob())
    .then((blob) => downloadBlob(blob, filename))
    .catch(() => void openExternal(url))
}

/**
 * 解析 ECharts 代码块内容为 option 对象；非法 JSON 或非对象返回 null。
 *
 * 宽松解析（业界标准容错，应对大模型输出习惯）：
 *   1. 先按严格 JSON.parse 试一次（标准输出零开销）；
 *   2. 失败则"字符串感知"地剥离 // 与 /* *\/ 注释、尾逗号、单引号字符串
 *      后再试——不会误伤字符串内的 http:// 等（状态机跳过字符串字面量）。
 * 仍失败返回 null（前端显示"解析失败"占位，不崩溃）。
 */
export function parseEChartsOption(source: string): Record<string, unknown> | null {
  const text = source.trim()
  if (!text) return null

  const attempts: string[] = [text]
  const cleaned = stripJsonCruft(text)
  if (cleaned !== text) attempts.push(cleaned)

  for (const candidate of attempts) {
    try {
      const obj: unknown = JSON.parse(candidate)
      if (obj !== null && typeof obj === 'object' && !Array.isArray(obj)) {
        return obj as Record<string, unknown>
      }
    } catch {
      /* 尝试下一个候选 */
    }
  }
  return null
}

/** 字符串感知地剥离 JSON 里的注释 / 尾逗号，并把单引号字符串转双引号。
 *  遍历时跟踪 in-string 状态，字符串字面量原样复制（URL 的 // 安全）。 */
function stripJsonCruft(src: string): string {
  let out = ''
  let inStr = false
  let strQuote = ''
  let escaped = false
  let i = 0
  while (i < src.length) {
    const ch = src[i]
    if (inStr) {
      if (escaped) {
        out += ch
        escaped = false
      } else if (ch === '\\') {
        out += ch
        escaped = true
      } else if (ch === strQuote) {
        // 闭合引号：单引号字符串统一转双引号（与开头转换一致）
        out += strQuote === "'" ? '"' : ch
        inStr = false
      } else {
        out += ch
      }
      i++
      continue
    }
    if (ch === '"' || ch === "'") {
      inStr = true
      strQuote = ch
      out += ch === "'" ? '"' : ch // 单引号字符串 → 双引号（宽松 JSON5 风格）
      i++
      continue
    }
    if (ch === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') i++ // 跳过 // 行注释
      continue
    }
    if (ch === '/' && src[i + 1] === '*') {
      i += 2
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++
      i += 2 // 跳过 /* */ 块注释
      continue
    }
    if (ch === ',') {
      // 尾逗号：后随 } 或 ]（允许空白）时删除
      let j = i + 1
      while (j < src.length && /\s/.test(src[j])) j++
      if (src[j] === '}' || src[j] === ']') {
        i++
        continue
      }
    }
    out += ch
    i++
  }
  return out
}

/** 净化 SVG：剔除 script 标签、事件属性（on*）、javascript: 链接，防注入后内联渲染。 */
export function sanitizeSVG(source: string): string {
  return source
    .replace(/<script[\s\S]*?<\/script>/gi, '')
    .replace(/<script[^>]*\/?>/gi, '')
    .replace(/\son\w+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi, '')
    .replace(/\s(href|xlink:href)\s*=\s*"javascript:[^"]*"/gi, '')
    .replace(/\s(href|xlink:href)\s*=\s*'javascript:[^']*'/gi, '')
}
