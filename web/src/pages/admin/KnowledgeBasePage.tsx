import { useCallback, useEffect, useRef, useState } from 'react'
import {
  adminCreateKb,
  adminDeleteKb,
  adminDeleteKbDoc,
  adminGetKb,
  adminListKbs,
  adminRetryKbDoc,
  adminSearchKb,
  adminUpdateKb,
  adminUploadKbDoc,
} from '@/lib/api'
import type { KbDocument, KbSearchHit, KnowledgeBase } from '@/types/api'
import AgentScopeSelect from '@/components/admin/AgentScopeSelect'
import { useAgentScope } from '@/components/admin/useAgentScope'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import {
  AlertCircle,
  BookOpen,
  CheckCircle2,
  FileText,
  Loader2,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Square,
  Trash2,
  Upload,
} from 'lucide-react'

/** 摄取状态 → 徽标样式 */
const STATUS_STYLE: Record<string, { label: string; cls: string }> = {
  succeeded: { label: '已就绪', cls: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-300' },
  queued: { label: '排队中', cls: 'bg-amber-500/10 text-amber-600 dark:text-amber-300' },
  processing: { label: '处理中', cls: 'bg-sky-500/10 text-sky-600 dark:text-sky-300' },
  failed: { label: '失败', cls: 'bg-destructive/10 text-destructive' },
}
const STATUS_FALLBACK = { label: '未知', cls: 'bg-muted text-muted-foreground' }

/** 刷新按钮转圈的最小可见时长（毫秒），避免请求秒回时动画一闪而过 */
const REFRESH_MIN_MS = 450

function statusMeta(s: string) {
  return STATUS_STYLE[s] ?? STATUS_FALLBACK
}

/** 支持的文件类型说明（与 rag-service 契约一致） */
const SUPPORTED_HINT = '支持 .md/.txt/.html/.xlsx（直接解析）及 .pdf/.docx/.pptx（沙盒解析，含图片/公式/媒体提取）；扫描版 PDF 会被拒绝，.doc 请另存为 .docx'

/** 文档状态筛选选项 */
const STATUS_FILTERS: { value: string; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'succeeded', label: '已就绪' },
  { value: 'queued', label: '排队中' },
  { value: 'processing', label: '处理中' },
  { value: 'failed', label: '失败' },
]

/** 编辑弹窗所需字段 */
type KbEditable = Pick<KnowledgeBase, 'id' | 'name' | 'description'>

/** 简单弹窗（管理端复用，点击遮罩不关闭） */
function Modal({
  title,
  subtitle,
  children,
  footer,
}: {
  title: string
  subtitle?: string
  children: React.ReactNode
  footer: React.ReactNode
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div
        className="flex max-h-[85vh] w-full max-w-md flex-col overflow-hidden rounded-xl border bg-background shadow-2xl"
        role="dialog"
        aria-modal="true"
      >
        <div className="border-b px-5 py-3.5">
          <div className="text-sm font-semibold">{title}</div>
          {subtitle && <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>}
        </div>
        <div className="flex-1 overflow-y-auto p-5">{children}</div>
        <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
          {footer}
        </div>
      </div>
    </div>
  )
}

/** 确认删除弹窗（危险操作） */
function ConfirmModal({
  title,
  message,
  onCancel,
  onConfirm,
  loading,
}: {
  title: string
  message: string
  onCancel: () => void
  onConfirm: () => void
  loading: boolean
}) {
  return (
    <Modal
      title={title}
      footer={
        <>
          <Button variant="outline" onClick={onCancel} disabled={loading}>
            取消
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={loading}>
            {loading ? <Loader2 className="size-4 animate-spin" /> : '确认删除'}
          </Button>
        </>
      }
    >
      <p className="text-sm text-muted-foreground">{message}</p>
    </Modal>
  )
}

export default function KnowledgeBasePage({ fixedAgentId }: { fixedAgentId?: string } = {}) {
  const { agentId, canScope, setAgentId, agents } = useAgentScope(fixedAgentId)
  const [bases, setBases] = useState<KnowledgeBase[]>([])
  const [selected, setSelected] = useState<KnowledgeBase | null>(null)
  const [loading, setLoading] = useState(true)
  // 手动刷新态（与初始 loading 分离）：刷新期间列表保留，按钮转圈最少 450ms。
  const [refreshing, setRefreshing] = useState(false)
  // 右侧详情页刷新态（同 refreshing，仅作用于详情区按钮）。
  const [detailRefreshing, setDetailRefreshing] = useState(false)
  const [error, setError] = useState('')

  // 当前选中 ID 的引用（refresh 闭包内读取，避免依赖 selected 造成回调抖动）。
  const selectedIdRef = useRef<string | null>(null)
  useEffect(() => {
    selectedIdRef.current = selected?.id ?? null
  }, [selected])

  // 新建知识库弹窗
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [createBusy, setCreateBusy] = useState(false)

  // 删除确认
  const [delKB, setDelKB] = useState<KnowledgeBase | null>(null)
  const [delDoc, setDelDoc] = useState<KbDocument | null>(null)
  const [deleting, setDeleting] = useState(false)

  // 知识库启用/停用切换中（禁用按钮防连点）
  const [toggling, setToggling] = useState(false)

  // 文档上传
  const fileRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  // 失败文档的错误详情展开状态（记录 doc_id）
  const [expandErr, setExpandErr] = useState<string | null>(null)

  // 编辑知识库弹窗
  const [editing, setEditing] = useState<KbEditable | null>(null)
  const [editName, setEditName] = useState('')
  const [editDesc, setEditDesc] = useState('')
  const [editBusy, setEditBusy] = useState(false)

  // 文档重试中（记录 doc_id，禁用按钮防连点）
  const [retrying, setRetrying] = useState<string | null>(null)

  // 文档状态筛选（'' = 全部）
  const [statusFilter, setStatusFilter] = useState('')

  // 检索预览弹窗
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchBusy, setSearchBusy] = useState(false)
  const [searchHits, setSearchHits] = useState<KbSearchHit[]>([])
  const [searchError, setSearchError] = useState('')

  /** 选中知识库：拉取详情（含文档分页），并同步列表中的 doc_count */
  const openDetail = useCallback((id: string) => {
    return adminGetKb(id, 1, 20, agentId)
      .then((kb) => {
        setSelected(kb)
        setBases((prev) => prev.map((b) => (b.id === kb.id ? { ...b, doc_count: kb.doc_count } : b)))
        setError('')
      })
      .catch((e) => setError((e as Error).message))
  }, [agentId])

  /** 右侧详情页刷新：保留列表，按钮转圈最少 REFRESH_MIN_MS */
  const refreshDetail = useCallback((id: string) => {
    const started = Date.now()
    setDetailRefreshing(true)
    return openDetail(id).finally(() => {
      const wait = Math.max(0, REFRESH_MIN_MS - (Date.now() - started))
      window.setTimeout(() => setDetailRefreshing(false), wait)
    })
  }, [openDetail])

  const refresh = useCallback(() => {
    // 手动/自动刷新：刷新按钮转圈最少可见 450ms（请求通常秒回，直接绑定
    // loading 会让动画快到看不见）；列表不清空，仅按钮转圈 + 完成后自动
    // 选中第一个知识库（见 openDetail 联动）。
    const started = Date.now()
    // 异步函数体内进入加载态：避免 effect 同步 setState 级联渲染
    setLoading(true)
    setRefreshing(true)
    adminListKbs(agentId)
      .then(async (list) => {
        setBases(list)
        setError('')
        // 默认选中：当前选中仍在列表中则保留，否则选中第一个库（切域/
        // 删除后自动落到新列表首项），除非列表为空。
        const cur = selectedIdRef.current
        const target = list.find((b) => b.id === cur) ?? list[0] ?? null
        if (target) {
          await openDetail(target.id)
        } else {
          setSelected(null)
        }
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => {
        const wait = Math.max(0, REFRESH_MIN_MS - (Date.now() - started))
        window.setTimeout(() => {
          setLoading(false)
          setRefreshing(false)
        }, wait)
      })
  }, [agentId, openDetail])

  useEffect(() => {
    // 首屏/切换资源域：refresh 异步路径内进入 loading（显示左侧占位，避免闪列表）。
    // async IIFE 包装：effect 本身不同步调用 refresh（其同步段含 setState 复位 loading）。
    void (async () => {
      await refresh()
    })()
  }, [refresh])

  const handleCreate = async () => {
    const name = newName.trim()
    if (!name) return
    setCreateBusy(true)
    try {
      await adminCreateKb(name, newDesc.trim())
      setCreating(false)
      setNewName('')
      setNewDesc('')
      await refresh()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setCreateBusy(false)
    }
  }

  const handleUpload = async (file: File | undefined) => {
    if (!file || !selected) return
    setUploading(true)
    try {
      await adminUploadKbDoc(selected.id, file, agentId)
      await openDetail(selected.id)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  const handleDeleteKB = async () => {
    if (!delKB) return
    setDeleting(true)
    try {
      await adminDeleteKb(delKB.id)
      if (selected?.id === delKB.id) setSelected(null)
      setDelKB(null)
      await refresh()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setDeleting(false)
    }
  }

  const handleDeleteDoc = async () => {
    if (!delDoc || !selected) return
    setDeleting(true)
    try {
      await adminDeleteKbDoc(selected.id, delDoc.doc_id, agentId)
      setDelDoc(null)
      await openDetail(selected.id)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setDeleting(false)
    }
  }

  /** 保存知识库编辑（改名/描述） */
  const handleEditSave = async () => {
    if (!editing) return
    const name = editName.trim()
    if (!name) {
      setError('知识库名称不能为空')
      return
    }
    setEditBusy(true)
    try {
      const updated = await adminUpdateKb(editing.id, name, editDesc, agentId)
      setBases((list) => list.map((kb) => (kb.id === updated.id ? { ...kb, name: updated.name, description: updated.description } : kb)))
      if (selected?.id === updated.id) {
        setSelected((prev) => (prev ? { ...prev, name: updated.name, description: updated.description } : prev))
      }
      setEditing(null)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setEditBusy(false)
    }
  }

  /** 切换知识库启用/停用：停用后普通用户与默认配置页均不可见、检索不可命中 */
  const handleToggleEnabled = async () => {
    if (!selected || toggling) return
    setToggling(true)
    try {
      const next = !selected.enabled
      const updated = await adminUpdateKb(selected.id, selected.name, selected.description, agentId, next)
      setSelected((prev) => (prev && prev.id === updated.id ? { ...prev, enabled: updated.enabled } : prev))
      setBases((list) => list.map((kb) => (kb.id === updated.id ? { ...kb, enabled: updated.enabled } : kb)))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setToggling(false)
    }
  }

  /** 手动重试摄取失败文档 */
  const handleRetryDoc = async (docId: string) => {
    if (!selected || retrying) return
    setRetrying(docId)
    try {
      await adminRetryKbDoc(selected.id, docId, agentId)
      await openDetail(selected.id)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRetrying(null)
    }
  }

  /** 检索预览：在选中知识库内检索 */
  const handleSearch = async () => {
    if (!selected || searchBusy) return
    const q = searchQuery.trim()
    if (!q) {
      setSearchError('请输入检索语句')
      return
    }
    setSearchBusy(true)
    setSearchError('')
    try {
      setSearchHits(await adminSearchKb(selected.id, q, 5, agentId))
    } catch (e) {
      setSearchError((e as Error).message)
      setSearchHits([])
    } finally {
      setSearchBusy(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      {/* 顶栏：标题 + 操作 */}
      <div className="flex items-center justify-between border-b px-5 py-3">
        <div>
          <h1 className="text-sm font-semibold">知识库管理</h1>
          <p className="text-xs text-muted-foreground">上传课程文档，自动切分并向量化，供智能体检索引用</p>
        </div>
        <div className="flex items-center gap-2">
          <AgentScopeSelect agentId={agentId} agents={agents} onChange={setAgentId} />
          <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={refreshing}>
            <RefreshCw className={cn('size-3.5', refreshing && 'animate-spin')} /> 刷新
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="size-3.5" /> 新建知识库
          </Button>
        </div>
      </div>

      {/* 非超管管理员固定资源域提示 */}
      {!canScope && (
        <div className="mx-5 mt-3 rounded-md border border-muted bg-muted/30 px-3 py-1.5 text-xs text-muted-foreground">
          当前管理的是智能体「{agentId}」的资源（由账号归属决定）。
        </div>
      )}

      {error && (
        <div className="mx-5 mt-3 flex items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
          <AlertCircle className="size-3.5 shrink-0" /> {error}
        </div>
      )}

      {/* 主体：左列表 + 右详情（各自独立滚动） */}
      <div className="grid min-h-0 flex-1 grid-cols-[320px_1fr]">
        {/* 左侧：知识库列表 */}
        <div className="min-h-0 overflow-y-auto border-r p-3">
          {loading && (
            <div className="flex justify-center py-8">
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
            </div>
          )}
          {!loading && bases.length === 0 && (
            <div className="flex flex-col items-center gap-2 py-10 text-center text-xs text-muted-foreground">
              <BookOpen className="size-6" />
              暂无知识库
              <span className="text-muted-foreground/70">点击右上角「新建知识库」创建</span>
            </div>
          )}
          {bases.map((kb) => (
            <div
              key={kb.id}
              onClick={() => void openDetail(kb.id)}
              className={cn(
                'mb-2 cursor-pointer rounded-lg border p-3 transition-colors',
                selected?.id === kb.id
                  ? 'border-indigo-500/50 bg-indigo-500/5'
                  : 'hover:bg-accent/70',
              )}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="truncate text-sm font-medium">{kb.name}</span>
                <div className="flex shrink-0 items-center gap-1.5">
                  {!kb.enabled && (
                    <Badge variant="outline" className="text-[10px] text-muted-foreground">
                      已停用
                    </Badge>
                  )}
                  <Badge variant="secondary" className="shrink-0 text-[10px]">
                    {kb.doc_count} 文档
                  </Badge>
                </div>
              </div>
              <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
                {kb.description || '暂无描述'}
              </p>
            </div>
          ))}
        </div>

        {/* 右侧：详情 */}
        <div className="min-h-0 overflow-y-auto">
          {!selected ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
              <BookOpen className="size-8" />
              从左侧选择一个知识库查看详情
            </div>
          ) : (
            <div className="p-5">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <h2 className="text-base font-semibold">{selected.name}</h2>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {selected.description || '暂无描述'} · 创建于{' '}
                    {new Date(selected.created_at).toLocaleString()}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="shrink-0"
                    onClick={() => void handleToggleEnabled()}
                    disabled={toggling}
                    title={selected.enabled ? '停用后：普通用户/默认配置页不可见，检索不可命中' : '启用后：该知识库重新对会话开放'}
                  >
                    {toggling ? <Loader2 className="size-3.5 animate-spin" /> : selected.enabled ? <Square className="size-3.5" /> : <Play className="size-3.5" />}
                    {selected.enabled ? '停用' : '启用'}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="shrink-0"
                    onClick={() => {
                      setEditing({ id: selected.id, name: selected.name, description: selected.description })
                      setEditName(selected.name)
                      setEditDesc(selected.description)
                    }}
                  >
                    <Pencil className="size-3.5" /> 编辑
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="shrink-0"
                    onClick={() => {
                      setSearchOpen(true)
                      setSearchQuery('')
                      setSearchHits([])
                      setSearchError('')
                    }}
                  >
                    <Search className="size-3.5" /> 检索预览
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="shrink-0 text-destructive hover:text-destructive"
                    onClick={() => setDelKB(selected)}
                  >
                    <Trash2 className="size-3.5" /> 删除知识库
                  </Button>
                </div>
              </div>

              {/* 上传区 */}
              <Card className="mt-4">
                <CardContent className="flex items-center gap-3 p-4">
                  <input
                    ref={fileRef}
                    type="file"
                    accept=".md,.txt,.html,.markdown,.xlsx,.pdf,.docx,.pptx"
                    className="hidden"
                    onChange={(e) => void handleUpload(e.target.files?.[0])}
                  />
                  <Button
                    size="sm"
                    onClick={() => fileRef.current?.click()}
                    disabled={uploading}
                  >
                    {uploading ? <Loader2 className="size-3.5 animate-spin" /> : <Upload className="size-3.5" />}
                    {uploading ? '上传中…' : '上传文档'}
                  </Button>
                  <span className="text-[11px] text-muted-foreground">
                    {SUPPORTED_HINT}；重传同名文件将更新该文档内容（自动覆盖旧分块）
                  </span>
                </CardContent>
              </Card>

              {/* 文档列表 */}
              <div className="mt-4">
                <div className="flex items-center justify-between gap-2">
                  <h3 className="text-sm font-medium">
                    文档
                    <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                      {selected.total ?? 0} 份
                    </span>
                  </h3>
                  <div className="flex items-center gap-2">
                    <div className="flex items-center rounded-md border p-0.5">
                      {STATUS_FILTERS.map((f) => (
                        <button
                          key={f.value}
                          type="button"
                          onClick={() => setStatusFilter(f.value)}
                          className={cn(
                            'rounded px-2 py-0.5 text-[11px] transition-colors',
                            statusFilter === f.value
                              ? 'bg-primary text-primary-foreground'
                              : 'text-muted-foreground hover:bg-accent',
                          )}
                        >
                          {f.label}
                        </button>
                      ))}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => void refreshDetail(selected.id)}
                      disabled={detailRefreshing}
                    >
                      <RefreshCw className={cn('size-3', detailRefreshing && 'animate-spin')} /> 刷新
                    </Button>
                  </div>
                </div>

                {!selected.documents?.length ? (
                  <div className="mt-2 rounded-lg border border-dashed p-8 text-center text-xs text-muted-foreground">
                    该知识库还没有文档
                  </div>
                ) : (
                  <div className="mt-2 space-y-1.5">
                    {selected.documents
                      .filter((doc) => !statusFilter || doc.status === statusFilter)
                      .map((doc) => {
                        const st = statusMeta(doc.status)
                        return (
                        <div
                          key={doc.doc_id}
                          className="flex items-center gap-3 rounded-lg border p-3"
                        >
                          <div className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted">
                            {doc.status === 'succeeded' ? (
                              <CheckCircle2 className="size-4 text-emerald-500" />
                            ) : (
                              <FileText className="size-4 text-muted-foreground" />
                            )}
                          </div>
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <span className="truncate text-sm font-medium">{doc.file_name}</span>
                              <Badge variant="secondary" className={cn('shrink-0 text-[10px]', st.cls)}>
                                {st.label}
                              </Badge>
                            </div>
                            <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
                              {doc.chunk_count > 0 ? `${doc.chunk_count} 个分块` : '待处理'} ·{' '}
                              {new Date(doc.updated_at).toLocaleString()}
                            </p>
                            {/* 失败原因完整展示：默认一行截断，点击展开全部（复制排查用） */}
                            {doc.error && doc.status === 'failed' && (
                              <button
                                type="button"
                                onClick={() => setExpandErr(expandErr === doc.doc_id ? null : doc.doc_id)}
                                className="mt-0.5 w-full text-left text-[11px] text-destructive/90 hover:text-destructive"
                                title="点击展开/收起完整错误信息"
                              >
                                {expandErr === doc.doc_id ? (
                                  <span className="block whitespace-pre-wrap break-all leading-relaxed">
                                    {doc.error}
                                  </span>
                                ) : (
                                  <span className="block truncate">
                                    <AlertCircle className="mr-1 inline size-3 align-[-1px]" />
                                    {doc.error}
                                  </span>
                                )}
                              </button>
                            )}
                          </div>
                          <div className="flex shrink-0 items-center gap-1">
                            {doc.status === 'failed' && (
                              <>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="text-muted-foreground hover:text-primary"
                                  onClick={() => void handleRetryDoc(doc.doc_id)}
                                  disabled={retrying === doc.doc_id}
                                  title="重新入队摄取（不更换文件，立即重试）"
                                  aria-label={`重试摄取 ${doc.file_name}`}
                                >
                                  {retrying === doc.doc_id ? (
                                    <Loader2 className="size-3.5 animate-spin" />
                                  ) : (
                                    <RotateCcw className="size-3.5" />
                                  )}
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="text-muted-foreground hover:text-primary"
                                  onClick={() => fileRef.current?.click()}
                                  title="重新上传同名文件以更新内容（覆盖旧分块并自动摄取）"
                                  aria-label={`重新上传 ${doc.file_name} 以更新`}
                                >
                                  <RefreshCw className="size-3.5" />
                                </Button>
                              </>
                            )}
                            <Button
                              variant="ghost"
                              size="icon"
                              className="shrink-0 text-muted-foreground hover:text-destructive"
                              onClick={() => setDelDoc(doc)}
                              aria-label={`删除 ${doc.file_name}`}
                            >
                              <Trash2 className="size-3.5" />
                            </Button>
                          </div>
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      </div>

      {/* 新建知识库弹窗 */}
      {creating && (
        <Modal
          title="新建知识库"
          subtitle="名称唯一，用于智能体检索时定位知识范围"
          footer={
            <>
              <Button variant="outline" onClick={() => setCreating(false)} disabled={createBusy}>
                取消
              </Button>
              <Button onClick={() => void handleCreate()} disabled={createBusy || !newName.trim()}>
                {createBusy ? <Loader2 className="size-4 animate-spin" /> : '创建'}
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="kb-name">知识库名称</Label>
              <Input
                id="kb-name"
                value={newName}
                maxLength={50}
                placeholder="如：操作系统课程资料"
                onChange={(e) => setNewName(e.target.value)}
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="kb-desc">描述（可选，≤200 字）</Label>
              <Textarea
                id="kb-desc"
                value={newDesc}
                maxLength={200}
                rows={3}
                placeholder="说明该知识库覆盖的内容范围"
                onChange={(e) => setNewDesc(e.target.value)}
              />
            </div>
          </div>
        </Modal>
      )}

      {/* 删除知识库确认 */}
      {delKB && (
        <ConfirmModal
          title="删除知识库"
          message={`确定删除知识库「${delKB.name}」吗？其下 ${delKB.doc_count} 份文档将一并删除，不可恢复。`}
          onCancel={() => setDelKB(null)}
          onConfirm={() => void handleDeleteKB()}
          loading={deleting}
        />
      )}

      {/* 删除文档确认 */}
      {delDoc && (
        <ConfirmModal
          title="删除文档"
          message={`确定删除文档「${delDoc.file_name}」吗？其分块向量将一并删除，不可恢复。`}
          onCancel={() => setDelDoc(null)}
          onConfirm={() => void handleDeleteDoc()}
          loading={deleting}
        />
      )}

      {/* 编辑知识库弹窗 */}
      {editing && (
        <Modal
          title="编辑知识库"
          subtitle="修改名称或描述，保存后即时生效"
          footer={
            <>
              <Button variant="outline" onClick={() => setEditing(null)} disabled={editBusy}>
                取消
              </Button>
              <Button onClick={() => void handleEditSave()} disabled={editBusy || !editName.trim()}>
                {editBusy ? <Loader2 className="size-4 animate-spin" /> : '保存'}
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="edit-kb-name">知识库名称</Label>
              <Input
                id="edit-kb-name"
                value={editName}
                maxLength={50}
                onChange={(e) => setEditName(e.target.value)}
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="edit-kb-desc">描述（可选，≤200 字）</Label>
              <Textarea
                id="edit-kb-desc"
                value={editDesc}
                maxLength={200}
                rows={3}
                onChange={(e) => setEditDesc(e.target.value)}
              />
            </div>
          </div>
        </Modal>
      )}

      {/* 检索预览弹窗 */}
      {searchOpen && (
        <Modal
          title="检索预览"
          subtitle={`在「${selected?.name ?? ''}」内检索，验证向量化效果（top 5）`}
          footer={
            <>
              <Button variant="outline" onClick={() => setSearchOpen(false)}>
                关闭
              </Button>
              <Button onClick={() => void handleSearch()} disabled={searchBusy || !searchQuery.trim()}>
                {searchBusy ? <Loader2 className="size-4 animate-spin" /> : '检索'}
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            <Input
              value={searchQuery}
              maxLength={200}
              placeholder="输入一个问题或关键词，如：什么是进程调度"
              onChange={(e) => setSearchQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void handleSearch()
              }}
              autoFocus
            />
            {searchError && (
              <p className="text-xs text-destructive">
                <AlertCircle className="mr-1 inline size-3 align-[-1px]" />
                {searchError}
              </p>
            )}
            {searchHits.length === 0 && !searchBusy && !searchError ? (
              <p className="rounded-lg border border-dashed p-6 text-center text-xs text-muted-foreground">
                输入语句后点「检索」，查看命中片段与相似度
              </p>
            ) : (
              <div className="max-h-72 space-y-2 overflow-y-auto">
                {searchHits.map((hit, i) => (
                  <div key={hit.chunk_id} className="rounded-lg border p-3">
                    <div className="flex items-center justify-between gap-2">
                      <span className="truncate text-xs font-medium text-muted-foreground">
                        #{i + 1} · {hit.source || '未知来源'}
                      </span>
                      <Badge variant="secondary" className="shrink-0 text-[10px]">
                        相似度 {(hit.score * 100).toFixed(1)}%
                      </Badge>
                    </div>
                    <p className="mt-1 text-xs leading-relaxed text-foreground/90">{hit.content}</p>
                  </div>
                ))}
              </div>
            )}
          </div>
        </Modal>
      )}
    </div>
  )
}
