import { useRef } from 'react'
import { Download } from 'lucide-react'
import { cn } from '@/lib/utils'
import { downloadBlob, sanitizeSVG } from '@/lib/rich'

/** 内联渲染 SVG 代码块为图片（净化后注入，防 script / 事件属性注入）。
 *  协议：模型输出 ```svg 代码块，前端将其渲染为可显示的矢量图。
 *  align 来自语言标签（```svg align=center）：居中/右对齐用块级包裹 + text-align。
 *  尺寸由 SVG 根元素 width/height 属性控制（外层 max-w-full 限宽、高度自动）。 */
export default function InlineSVG({ source, align }: { source: string; align?: string }) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const svg = sanitizeSVG(source).trim()

  // 把渲染出的 SVG 序列化回文本，以 .svg 文件下载（保留矢量属性，可编辑）
  const handleDownload = () => {
    const svgEl = wrapRef.current?.querySelector('svg')
    if (!svgEl) return
    const xml = new XMLSerializer().serializeToString(svgEl)
    downloadBlob(new Blob([xml], { type: 'image/svg+xml;charset=utf-8' }), 'image.svg')
  }

  if (!svg) {
    return (
      <div className="my-2 rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
        （SVG 内容为空或已被过滤）
      </div>
    )
  }
  // 净化后的 SVG 注入；svg 宽高由外层约束（max-width 自适应、高度自动）。
  return (
    <div
      className={cn(
        'relative my-2',
        align === 'center' ? 'block text-center' : align === 'right' ? 'block text-right' : 'inline-block',
      )}
    >
      <button
        type="button"
        title="下载 SVG"
        aria-label="下载 SVG"
        onClick={handleDownload}
        className="absolute right-1.5 top-1.5 z-10 rounded-md border bg-background/90 p-1 text-muted-foreground shadow-sm hover:bg-accent hover:text-accent-foreground"
      >
        <Download className="h-3.5 w-3.5" />
      </button>
      <div
        ref={wrapRef}
        className="[&_svg]:h-auto [&_svg]:max-w-full"
        dangerouslySetInnerHTML={{ __html: svg }}
      />
    </div>
  )
}
