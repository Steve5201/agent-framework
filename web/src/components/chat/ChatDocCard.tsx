import { FileText } from 'lucide-react'
import { parseDocMarker } from './docMarker'

/**
 * 聊天上传文档（模块二）· 已上传文档引用卡片。
 *
 * 后端上传成功后向会话历史注入一条 user 消息，格式：
 *   [文档] <文件名>（已保存至工作区 <相对路径>）
 * 空文件：
 *   [文档] <文件名>（文件内容为空，无解析内容）
 *
 * 本组件把这类消息渲染为"文档引用卡片"（文件名 + 工作区路径溯源）。
 * 系统不再注入解析正文——模型在会话配置了「文档解析」能力时自行调用
 * read_document 工具解析，因此卡片无正文可展开。
 * 消息解析工具见 ./docMarker（纯函数独立文件，保持组件文件单导出）。
 */

export default function ChatDocCard({ content }: { content: string }) {
  const doc = parseDocMarker(content)
  if (!doc) return null
  const empty = !doc.relPath

  return (
    <div className="w-full min-w-[240px] max-w-sm overflow-hidden rounded-lg border bg-background/70 text-left shadow-sm">
      <div className="flex items-center gap-2 px-3 py-2">
        <FileText className="h-4 w-4 shrink-0 text-primary" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{doc.fileName}</div>
          <div className="truncate font-mono text-xs text-muted-foreground">
            {doc.relPath || '文件内容为空（无工作区文件）'}
          </div>
        </div>
        <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
          {empty ? '空文件' : '已保存至工作区'}
        </span>
      </div>
    </div>
  )
}
