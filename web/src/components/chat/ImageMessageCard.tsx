import { useState } from 'react'
import { FileImage } from 'lucide-react'
import { getServerUrl } from '@/lib/settings'
import { parseImageMarker } from './docMarker'
import { MediaDownloadButton } from './RichContent'

/**
 * 聊天上传图片（模块二）· 图片气泡。
 *
 * 后端上传图片后注入一条 user 消息，格式：
 *   [图片] <文件名>（已保存至工作区 <路径>）
 *
 * 本组件与 AI 气泡的媒体渲染保持同一套逻辑（复用 RichContent 的下载按钮）：
 * 图片经 /files 静态端点直接渲染为用户直接看到的视觉图像，右上角悬浮下载按钮。
 * 系统不再自动注入解析文本——模型在会话配置了「识图」能力时自行调用
 * describe_image 工具解析；因此气泡里不展示任何解析文本。
 * 图片加载失败（工作区被清理等）时降级为文件记录卡片，不裂图。
 * 消息解析工具见 ./docMarker（纯函数独立文件，保持组件文件单导出）。
 */

export default function ImageMessageCard({ content }: { content: string }) {
  const [broken, setBroken] = useState(false)
  const img = parseImageMarker(content)
  if (!img) return null
  const src = `${getServerUrl()}/files/${img.relPath}`

  // 图片缺失（工作区被清理等）：降级为文件记录展示，不裂图。
  if (broken) {
    return (
      <div className="w-full min-w-[240px] max-w-sm overflow-hidden rounded-lg border bg-background/70 text-left shadow-sm">
        <div className="flex items-center gap-2 px-3 py-2">
          <FileImage className="h-4 w-4 shrink-0 text-primary" aria-hidden />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">{img.fileName}</div>
            <div className="truncate font-mono text-xs text-muted-foreground">{img.relPath}</div>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="my-2 max-w-full">
      <div className="relative inline-block">
        <img
          src={src}
          alt={img.fileName}
          loading="lazy"
          onError={() => setBroken(true)}
          className="max-h-80 max-w-full rounded-md border object-contain"
        />
        <MediaDownloadButton src={src} kind="image" />
      </div>
      <div className="mt-1 truncate text-xs text-muted-foreground">{img.fileName}</div>
    </div>
  )
}
