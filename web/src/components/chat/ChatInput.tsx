import { useRef, useState, type ChangeEvent, type DragEvent, type KeyboardEvent } from 'react'
import { AlertCircle, CheckCircle2, FileText, Loader2, Paperclip, Send, Square, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { useChatStore } from '@/stores/chat'
import { cn } from '@/lib/utils'
import ConfigButtonArea from './config/ConfigButtonArea'
import FreeModeToggle from './FreeModeToggle'

// 聊天上传文件（模块二）：类型白名单、大小与份数上限（与后端校验一致）。
// 图片类型为视觉解析预留：落盘原图 + 前端渲染，内容理解暂未接线。
const CHAT_DOC_ACCEPT =
  '.md,.markdown,.txt,.html,.htm,.xlsx,.pdf,.docx,.pptx,.png,.jpg,.jpeg,.gif,.webp,.bmp,.svg'
const CHAT_DOC_MAX_BYTES = 50 * 1024 * 1024
const CHAT_DOC_MAX_FILES = 20
const CHAT_DOC_EXTS = new Set([
  'md', 'markdown', 'txt', 'html', 'htm', 'xlsx', 'pdf', 'docx', 'pptx',
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg',
])

/** 待发文件：解析流程中的状态机（等待 → 解析中 → 完成/失败）。
 *  状态反馈给用户"文件正在服务器上解析"，避免上传大文档时 UI 无响应感。 */
interface PendingFile {
  id: string
  file: File
  status: 'waiting' | 'parsing' | 'done' | 'error'
  error?: string
}

// 前端本地唯一 ID（待发列表 key / 状态定位用）。
let seq = 0
const nid = () => `${Date.now().toString(36)}-${(seq++).toString(36)}`

/**
 * 输入框：Enter 发送、Shift+Enter 换行、自适应高度、发送/停止切换。
 * 文件交互（发送列表模式）：选择/拖拽文件不立即上传，先渲染在输入框上方的
 * 待发文件标志（可移除），用户点发送时与文本一起提交——符合常规智能体体验。
 * 发送后逐个文件进入解析流程：待发列表实时显示"解析中"动画，完成后自动移除
 * （上传成功会注入 [文档]/[图片] 消息，用户气泡即见文件卡片）；失败的文件
 * 留在列表并标记具体原因，可再次发送重试。
 * 布局：上传按钮 + 输入框 + 发送按钮同排（配置区不再参与同排，避免随内容挤压输入区）；
 * 配置按钮区（ConfigButtonArea）在输入框下方一行，左对齐。
 * canConfigure=false 时隐藏配置按钮区（游客模式）。
 */
export default function ChatInput({ canConfigure = true }: { canConfigure?: boolean }) {
  const sending = useChatStore((s) => s.sending)
  const regenerating = useChatStore((s) => Boolean(s.regeneratingId))
  const sendMessage = useChatStore((s) => s.sendMessage)
  const stopStreaming = useChatStore((s) => s.stopStreaming)
  const uploadDocument = useChatStore((s) => s.uploadDocument)

  const [value, setValue] = useState('')
  // 待发文件列表：选文件不立即上传，随文本一起提交；发送后进入解析流程。
  const [pendingFiles, setPendingFiles] = useState<PendingFile[]>([])
  const [uploading, setUploading] = useState(false)
  const [uploadHint, setUploadHint] = useState('')
  const [dragging, setDragging] = useState(false)
  const taRef = useRef<HTMLTextAreaElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const hintTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  function autoResize() {
    const ta = taRef.current
    if (!ta) return
    ta.style.height = 'auto'
    ta.style.height = `${Math.min(ta.scrollHeight, 200)}px`
  }

  /** 提示行：短暂显示后自动消失（成功/失败统一走这里，避免硬弹窗）。 */
  function flashHint(text: string) {
    setUploadHint(text)
    if (hintTimer.current) clearTimeout(hintTimer.current)
    hintTimer.current = setTimeout(() => setUploadHint(''), 6000)
  }

  /** 加入待发列表：前端预检类型/大小/份数（与后端配额一致），不合格的提示并跳过。 */
  function addFiles(files: File[]) {
    const accepted: PendingFile[] = []
    for (const file of files) {
      const ext = file.name.split('.').pop()?.toLowerCase() ?? ''
      if (!CHAT_DOC_EXTS.has(ext)) {
        flashHint(`不支持的文件类型 ${file.name}（支持 md/txt/html/xlsx/pdf/docx/pptx 及常见图片）`)
        continue
      }
      if (file.size > CHAT_DOC_MAX_BYTES) {
        flashHint(`文件过大 ${file.name}（≤20MB）`)
        continue
      }
      if (accepted.length + pendingFiles.length >= CHAT_DOC_MAX_FILES) {
        flashHint(`单会话文件最多 ${CHAT_DOC_MAX_FILES} 份，多余文件已忽略`)
        break
      }
      accepted.push({ id: nid(), file, status: 'waiting' })
    }
    if (accepted.length > 0) {
      setPendingFiles((prev) => [...prev, ...accepted])
      setUploadHint('')
    }
  }

  /** 文件选择框回调：仅加入待发列表，不立即上传。 */
  function handleFile(e: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(e.target.files ?? [])
    e.target.value = '' // 允许重复选择同一文件
    if (files.length === 0 || uploading || sending || regenerating) return
    addFiles(files)
  }

  /** 拖拽投放：同样只加入待发列表。 */
  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault()
    setDragging(false)
    if (uploading || sending || regenerating) return
    addFiles(Array.from(e.dataTransfer.files ?? []))
  }

  /** 发送：文件列表 + 文本一起提交；每个文件进入"解析中→完成/失败"状态流程。 */
  async function handleSubmit() {
    if (sending || uploading || regenerating) return
    const text = value.trim()
    // hasFiles 记录本次提交是否携带文件（上传后 pendingFiles 被清空，
    // 纯文件场景靠它判断是否触发回复）。
    const hasFiles = pendingFiles.length > 0
    if (!text && !hasFiles) return

    if (hasFiles) {
      setUploading(true)
      const batch = [...pendingFiles]
      setPendingFiles([]) // 待发列表交给流程管理：逐个回填解析状态
      const errors: string[] = []
      for (const item of batch) {
        setPendingFiles((prev) => [...prev, { ...item, status: 'parsing' }])
        try {
          await uploadDocument(item.file)
          // 解析完成：短暂显示"已完成"后移除（上传成功已注入消息，用户气泡即见卡片）。
          setPendingFiles((prev) => prev.map((p) => (p.id === item.id ? { ...p, status: 'done' } : p)))
          setTimeout(() => {
            setPendingFiles((prev) => prev.filter((p) => p.id !== item.id))
          }, 1000)
        } catch (e) {
          const msg = (e as Error).message
          if (msg && !errors.includes(msg)) errors.push(msg)
          setPendingFiles((prev) => prev.map((p) => (p.id === item.id ? { ...p, status: 'error', error: msg } : p)))
        }
      }
      setUploading(false)
      if (errors.length > 0) {
        flashHint(`${errors.length} 个文件上传失败，已留在待发列表可重试：${errors.join('；')}`)
        return // 文本暂不发送，避免文件未注入就出回复的时序错乱
      }
    }

    if (text) {
      void sendMessage(text)
      setValue('')
      requestAnimationFrame(autoResize)
    } else if (hasFiles) {
      // 纯文件场景（需求 6）：只上传文件不输入文字也要触发回复——
      // 空消息交给后端，基于注入的 [文档]/[图片] 内容回复。
      void sendMessage('')
      requestAnimationFrame(autoResize)
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    // 中文输入法组合期间不触发发送
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault()
      void handleSubmit()
    }
  }

  const sendable = value.trim().length > 0 || pendingFiles.length > 0
  const busy = sending || uploading || regenerating

  return (
    <div className="border-t px-4 py-3 sm:px-12">
      <div
        className="mx-auto w-full max-w-[800px]"
        onDragOver={(e) => {
          e.preventDefault()
          if (!busy) setDragging(true)
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
      >
        {/* 待发文件列表：每项带解析状态（等待/解析中动画/完成/失败原因），发送时随文本一起提交 */}
        {pendingFiles.length > 0 && (
          <div className="mb-1.5 flex flex-wrap items-center gap-1.5">
            {pendingFiles.map((f) => (
              <span
                key={f.id}
                className={cn(
                  'inline-flex max-w-[260px] items-center gap-1.5 rounded-full border bg-background px-2 py-0.5 text-xs',
                  f.status === 'error' && 'border-destructive/50',
                )}
              >
                {f.status === 'parsing' ? (
                  <Loader2 className="size-3 shrink-0 animate-spin text-primary" aria-hidden />
                ) : f.status === 'done' ? (
                  <CheckCircle2 className="size-3 shrink-0 text-emerald-600" aria-hidden />
                ) : f.status === 'error' ? (
                  <AlertCircle className="size-3 shrink-0 text-destructive" aria-hidden />
                ) : (
                  <FileText className="size-3 shrink-0 text-primary" aria-hidden />
                )}
                <span className="truncate" title={f.file.name}>
                  {f.file.name}
                </span>
                {f.status === 'parsing' && (
                  <span className="shrink-0 text-primary">解析中…</span>
                )}
                {f.status === 'error' && f.error && (
                  <span className="max-w-[140px] truncate shrink-0 text-destructive" title={f.error}>
                    {f.error}
                  </span>
                )}
                {f.status !== 'parsing' && (
                  <button
                    type="button"
                    onClick={() => setPendingFiles((prev) => prev.filter((p) => p.id !== f.id))}
                    className="shrink-0 rounded-full p-0.5 text-muted-foreground transition-colors hover:bg-muted"
                    title="移除"
                    aria-label={`移除 ${f.file.name}`}
                  >
                    <X className="size-3" />
                  </button>
                )}
              </span>
            ))}
            <span className="text-xs text-muted-foreground">
              {pendingFiles.length}/{CHAT_DOC_MAX_FILES} 份待发送
            </span>
          </div>
        )}
        {/* 拖拽高亮：仅视觉提示，投放区域即整个输入区 */}
        <div
          className={cn(
            'flex items-end gap-1.5 rounded-2xl border bg-card px-2.5 py-1.5 shadow-sm',
            dragging && 'ring-2 ring-primary/60',
          )}
        >
          <Textarea
            ref={taRef}
            rows={1}
            value={value}
            placeholder={
              uploading
                ? '正在解析文件…'
                : pendingFiles.length > 0
                  ? '可输入文字，与待发文件一起发送；Enter 发送'
                  : '输入消息，Enter 发送，Shift+Enter 换行（可拖入文档）'
            }
            onChange={(e) => {
              setValue(e.target.value)
              autoResize()
            }}
            onKeyDown={onKeyDown}
            disabled={busy}
            className="min-h-[40px] max-h-[200px] flex-1 resize-none border-0 bg-transparent px-1 py-2 shadow-none focus-visible:ring-0"
          />
          {(sending || regenerating) ? (
            <Button
              type="button"
              variant="secondary"
              size="icon"
              onClick={stopStreaming}
              title={regenerating ? '停止重新生成' : '停止生成'}
              aria-label="停止生成"
            >
              <Square />
            </Button>
          ) : uploading ? (
            <Button type="button" variant="secondary" size="icon" disabled title="正在解析文件…" aria-label="正在解析文件">
              <Loader2 className="animate-spin" />
            </Button>
          ) : (
            <Button
              type="button"
              size="icon"
              onClick={() => void handleSubmit()}
              disabled={!sendable}
              title="发送"
              aria-label="发送"
              className="size-9 shrink-0 rounded-full shadow-sm"
            >
              <Send />
            </Button>
          )}
        </div>
        {/* 上传 + 配置按钮区：输入框下方一行，左对齐。上传按钮始终渲染（游客也可
            上传文件），配置按钮区按 canConfigure 条件隐藏（游客模式）。 */}
        <div className="mt-1.5 flex items-center gap-0.5">
          <input
            ref={fileRef}
            type="file"
            accept={CHAT_DOC_ACCEPT}
            multiple
            className="hidden"
            onChange={handleFile}
            aria-label="选择文件"
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            onClick={() => fileRef.current?.click()}
            disabled={busy || pendingFiles.length >= CHAT_DOC_MAX_FILES}
            title="选择文件（支持 md/txt/html/xlsx/pdf/docx/pptx 及常见图片，≤20MB，随消息一起发送）"
            aria-label="选择文件"
            className="h-8 w-8 text-muted-foreground"
          >
            <Paperclip />
          </Button>
          {canConfigure && <ConfigButtonArea />}
        </div>
        {/* 上传反馈行：成功/失败提示，短暂显示后消失 */}
        {uploadHint && <div className="mt-1.5 text-xs text-muted-foreground">{uploadHint}</div>}
        {/* 输入提示行（参照 ui_chat.html input-hint） */}
        <div className="mt-1.5 text-center text-[11px] text-muted-foreground/70">
          按 Enter 发送 · Shift+Enter 换行 · 支持 Markdown 格式
        </div>
        {/* 自由模式（仅桌面端显示）：本机本地 shell 不询问、不限超时的个人化开关 */}
        <div className="mt-1.5 flex justify-center">
          <FreeModeToggle />
        </div>
      </div>
    </div>
  )
}
