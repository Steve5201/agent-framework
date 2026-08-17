// DocDownloadCard —— 文档生成下载卡片（P4-D render_document + P5-HTML render_html）。
//
// 渲染协议：模型在最终回答中输出 Markdown 代码块
//   ```doc
//   users/<uid>/chat-docs/<fileID>/<文件名>.html
//   ```
// RichContent 识别 lang === 'doc' 后渲染本卡片：文件图标 + 文件名 + 类型标签 +
// 下载按钮（fetch 经 gateway /files 拉取，与图片/视频下载同一链路）。
// .html/.pdf 额外提供"预览"按钮，弹 HtmlPreviewModal（沙箱 iframe，禁脚本）。
import { useState } from 'react'
import { FileText, Presentation, Eye, Download, FileType2, FileType } from 'lucide-react'
import { getServerUrl } from '@/lib/settings'
import { downloadUrl } from '@/lib/rich'
import HtmlPreviewModal from './HtmlPreviewModal'

/** 由代码块内容解析下载地址：支持工作区相对路径（users/...）与完整 URL。 */
function resolveDocUrl(raw: string): string {
  const path = raw.trim()
  if (/^https?:\/\//i.test(path)) return path
  return `${getServerUrl()}/files/${path}`
}

/** 由路径取文件名（末级路径段，去 query/hash）。 */
function docFileName(raw: string): string {
  const path = raw.trim().split(/[?#]/)[0]
  const segs = path.split('/')
  return segs[segs.length - 1] || '文档'
}

/** 文件类型标签（按扩展名）。 */
function docMeta(name: string): { label: string; Icon: typeof FileText } {
  const ext = name.split('.').pop()?.toLowerCase()
  if (ext === 'docx' || ext === 'doc') return { label: 'Word 文档', Icon: FileText }
  if (ext === 'pptx' || ext === 'ppt') return { label: 'PPT 演示文稿', Icon: Presentation }
  if (ext === 'html' || ext === 'htm') return { label: '网页文档', Icon: FileType }
  if (ext === 'pdf') return { label: 'PDF 文档', Icon: FileType2 }
  return { label: '文档文件', Icon: FileText }
}

export default function DocDownloadCard({ path }: { path: string }) {
  const [previewing, setPreviewing] = useState(false)
  const url = resolveDocUrl(path)
  const name = docFileName(path)
  const { label, Icon } = docMeta(name)
  const ext = name.split('.').pop()?.toLowerCase()
  // 仅网页/PDF 可安全预览（沙箱 iframe 禁脚本；docx/pptx 无浏览器预览能力）。
  const previewable = ext === 'html' || ext === 'htm' || ext === 'pdf'
  return (
    <>
      <div className="my-2 flex max-w-md items-center gap-3 rounded-lg border bg-background/80 px-3 py-2.5 shadow-sm">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
          <Icon className="h-5 w-5" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium">{name}</p>
          <p className="text-xs text-muted-foreground">{label}</p>
        </div>
        {previewable && (
          <button
            type="button"
            title="预览文档"
            aria-label="预览文档"
            onClick={() => setPreviewing(true)}
            className="flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium text-muted-foreground shadow-sm hover:bg-accent hover:text-accent-foreground"
          >
            <Eye className="h-3.5 w-3.5" />
            预览
          </button>
        )}
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
      </div>
      {previewing && (
        <HtmlPreviewModal
          url={url}
          name={name}
          ext={ext === 'pdf' ? 'pdf' : 'html'}
          onClose={() => setPreviewing(false)}
        />
      )}
    </>
  )
}
