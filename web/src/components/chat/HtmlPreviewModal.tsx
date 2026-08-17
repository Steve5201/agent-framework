// HtmlPreviewModal —— 网页（.html）/ PDF 文档预览弹窗（P5-HTML 中间层文档生成）。
//
// 安全（LLM 产出的 HTML 是不可信输入，多层防线之一）：
//   - 后端 sanitizeHTML 已做结构级净化（剥 script/iframe/on*/javascript:）；
//   - 本组件 iframe 使用 sandbox 属性且不含 allow-scripts → 禁脚本执行；
//     不含 allow-same-origin 之外任何能力（禁表单/弹窗/焦点劫持/顶层导航），
//     无与父页面通信能力，只读预览。
//   - PDF 为二进制文档，浏览器原生查看器渲染，同样置于沙箱 iframe 内。
//
// 交互：Esc / 点击遮罩 / 关闭按钮 均可关闭；头部常驻"下载"按钮。
import { useEffect } from 'react'
import { X, Download, FileText, FileType2 } from 'lucide-react'
import { downloadUrl } from '@/lib/rich'

interface Props {
  url: string
  name: string
  ext: 'html' | 'pdf'
  onClose: () => void
}

export default function HtmlPreviewModal({ url, name, ext, onClose }: Props) {
  // Esc 关闭（预览模态需键盘可达）。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const Icon = ext === 'html' ? FileText : FileType2
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={`预览 ${name}`}
      onClick={(e) => {
        // 点遮罩关闭（点击面板内部不关闭）。
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="flex h-[85vh] w-full max-w-5xl flex-col overflow-hidden rounded-lg border bg-background shadow-lg">
        <div className="flex items-center gap-2 border-b px-4 py-2.5">
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
            <Icon className="h-4 w-4" />
          </span>
          <p className="min-w-0 flex-1 truncate text-sm font-medium">{name}</p>
          <button
            type="button"
            title="下载文件"
            aria-label="下载文件"
            onClick={() => downloadUrl(url, name)}
            className="flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium text-muted-foreground shadow-sm hover:bg-accent hover:text-accent-foreground"
          >
            <Download className="h-3.5 w-3.5" />
            下载
          </button>
          <button
            type="button"
            title="关闭预览"
            aria-label="关闭预览"
            onClick={onClose}
            className="flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="min-h-0 flex-1 bg-muted/40">
          {/* sandbox：禁脚本/表单/弹窗等一切交互能力，只保留同源资源读取（样式内联于 HTML，无需外部请求）。 */}
          <iframe
            key={url}
            src={url}
            title={name}
            sandbox="allow-same-origin"
            className="h-full w-full bg-background"
          />
        </div>
      </div>
    </div>
  )
}
