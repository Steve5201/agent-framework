import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { adminListAgents } from '@/lib/api'
import { ALL_AGENT_ID, DEFAULT_AGENT_ID, loadRememberedAgent, rememberAgent } from '@/lib/roles'
import { ArrowLeftRight, Ban, Check, ChevronDown, Loader2, RefreshCw, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/**
 * 超管智能体切换弹窗（combobox 单选下拉，阶段3·统一标签模型）。
 * 作为输入区配置项（configRegistry key='agent'）渲染，仅最高超管可见。
 *
 * 交互（按产品要求）：
 *   - 智能体选择为单选，用下拉框（combobox）呈现；选项 = 具体智能体，
 *     不再提供"全部智能体（'*'）"聚合项（其与配置区按域更新的设计冲突，
 *     且导致跨域会话展示混乱）；
 *   - 选择某个智能体 → 跳转对应门户（/agent/:id）并刷新对话列表，
 *     保留登录态（超管 token 全局有效，不绑定域）；
 *   - 当前若在管理端对话（/admin，无智能体域）或不在任何智能体门户，
 *     下拉显示占位提示，选择具体智能体后跳转其门户。
 */
interface Props {
  onClose: () => void
}

export default function AgentSwitcherDialog({ onClose }: Props) {
  const navigate = useNavigate()
  const location = useLocation()

  const [agents, setAgents] = useState<{ id: string; name: string; status: number }[] | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const listRef = useRef<HTMLDivElement>(null)

  // 记住上次选择的智能体：切换器始终有一个默认选中项，避免"没有选中"。
  // 优先级：当前 URL 域 > 记住的上次选择 > 管理端占位/默认域。
  const remembered = loadRememberedAgent()
  const m = location.pathname.match(/^\/agent\/([^/]+)/)
  const current = m
    ? m[1]
    : remembered || (location.pathname.startsWith('/admin') ? 'admin' : DEFAULT_AGENT_ID)

  // 加载智能体列表（combobox 数据源）；'*' 聚合项不作为选项。
  // 异步路径内进入加载态：避免 effect 同步 setState 级联渲染。
  const load = useCallback(() => {
    setLoading(true)
    setError('')
    adminListAgents()
      .then((list) => {
        setAgents(
          list
            .map((a) => ({ id: a.id, name: a.name || a.id, status: a.status ?? 1 }))
            .filter((a) => a.id !== ALL_AGENT_ID),
        )
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  // 打开弹窗即加载；async IIFE 包装：effect 不同步调用 load（其同步段含 setState 复位）。
  useEffect(() => {
    void (async () => {
      await load()
    })()
  }, [load])

  // Esc 关闭弹窗
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  // 点击外部关闭下拉
  useEffect(() => {
    if (!open) return
    const onDocClick = (e: MouseEvent) => {
      if (listRef.current && !listRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [open])

  function go(id: string) {
    setOpen(false)
    onClose()
    rememberAgent(id)
    // 是否已在目标门户取决于 URL 域（而非 current——管理端下 current 可能是
    // 记住的上次选择，点击它仍需跳转到对应门户）。
    const m2 = location.pathname.match(/^\/agent\/([^/]+)/)
    if (m2?.[1] === id) return
    // 切换当前对话的智能体：跳转对应门户聊天域，保留登录态（不登出）。
    navigate(`/agent/${id}`)
  }

  // 默认选中项：当前域优先；不在列表（如管理端对话/域被删）时兜底列表第一个，
  // 保证切换器始终有选中项，不会出现"没有智能体选中"。
  const selected = agents?.find((a) => a.id === current) ?? agents?.[0] ?? null
  const enabledCount = useMemo(() => (agents ?? []).filter((a) => a.status !== 0).length, [agents])
  const triggerLabel =
    selected != null
      ? `${selected.name}（${selected.id}）`
      : current === 'admin'
        ? '当前为管理端对话'
        : '选择智能体门户'

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-sm rounded-xl border bg-background p-5 shadow-xl">
        {/* 头部：图标 + 标题 + 关闭 */}
        <div className="mb-3 flex items-start justify-between gap-2">
          <div className="flex items-center gap-2.5">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-blue-500/15 text-blue-600 dark:text-blue-300">
              <ArrowLeftRight className="size-4" />
            </span>
            <div>
              <h2 className="text-sm font-semibold">切换智能体</h2>
              <p className="text-[11px] text-muted-foreground">
                {agents != null ? `共 ${agents.length} 个智能体（${enabledCount} 个可用）` : '加载智能体列表…'}
              </p>
            </div>
          </div>
          <Button type="button" variant="ghost" size="icon" className="-mr-1 -mt-1 size-7" onClick={onClose} aria-label="关闭">
            <X className="size-4" />
          </Button>
        </div>

        {/* 当前门户已停用：提示条 */}
        {selected != null && selected.status === 0 && (
          <div className="mb-3 flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
            <Ban className="mt-0.5 size-3.5 shrink-0" />
            <span>
              当前门户「{selected.name}」已停用，仅可查看历史会话。请切换到其他智能体后创建新会话。
            </span>
          </div>
        )}

        {/* Combobox 触发按钮 + 下拉（absolute 覆盖层，不撑开弹窗高度） */}
        <div className="relative" ref={listRef}>
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            role="combobox"
            aria-label="切换智能体"
            aria-haspopup="listbox"
            aria-expanded={open}
            className={cn(
              'flex h-10 w-full items-center justify-between gap-2 rounded-md border bg-background px-3 text-sm transition-colors focus:outline-none focus:ring-2 focus:ring-ring',
              open ? 'border-ring' : 'border-border hover:bg-accent/40',
            )}
          >
            <span className={cn('flex min-w-0 items-center gap-2', !selected && 'text-muted-foreground')}>
              {selected && (
                <span className="flex size-5 shrink-0 items-center justify-center rounded bg-muted text-[10px] font-semibold text-muted-foreground">
                  {selected.name.charAt(0).toUpperCase()}
                </span>
              )}
              <span className="truncate">{agents === null ? '加载中…' : triggerLabel}</span>
            </span>
            <ChevronDown className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
          </button>

          {/* 下拉选项列表 */}
          {open && (
            <div role="listbox" className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-md border bg-background shadow-lg">
              <div className="border-b px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                可用智能体
              </div>
              <div className="max-h-56 overflow-y-auto p-1">
                {loading && (
                  <div className="flex items-center gap-1.5 px-2.5 py-2 text-xs text-muted-foreground">
                    <Loader2 className="h-3 w-3 animate-spin" /> 加载中…
                  </div>
                )}
                {error && (
                  <div className="flex items-center justify-between gap-2 px-2.5 py-2">
                    <span className="text-xs text-destructive">{error}</span>
                    <Button type="button" variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={load}>
                      <RefreshCw className="size-3" /> 重试
                    </Button>
                  </div>
                )}
                {!loading && !error && agents !== null && agents.length === 0 && (
                  <div className="px-2.5 py-2 text-xs text-muted-foreground">暂无智能体</div>
                )}
                {!loading &&
                  !error &&
                  agents?.map((a) => {
                    const disabled = a.status === 0
                    const isSelected = selected != null && a.id === selected.id
                    return (
                      <button
                        key={a.id}
                        type="button"
                        role="option"
                        aria-selected={isSelected}
                        disabled={disabled}
                        onClick={() => go(a.id)}
                        className={cn(
                          'flex w-full cursor-pointer items-center justify-between gap-2 rounded px-2.5 py-1.5 text-left text-sm transition-colors',
                          isSelected ? 'bg-accent' : 'hover:bg-accent/50',
                          disabled && 'cursor-not-allowed opacity-50 hover:bg-transparent',
                        )}
                        title={disabled ? '已停用，不可进入' : undefined}
                      >
                        <span className="flex min-w-0 items-center gap-2">
                          <span className="flex size-5 shrink-0 items-center justify-center rounded bg-muted text-[10px] font-semibold text-muted-foreground">
                            {a.name.charAt(0).toUpperCase()}
                          </span>
                          <span className="min-w-0">
                            <span className="block truncate">{a.name}</span>
                            <span className="block font-mono text-[11px] text-muted-foreground">{a.id}</span>
                          </span>
                        </span>
                        {disabled && (
                          <Badge variant="outline" className="shrink-0 text-[10px] text-muted-foreground">
                            已停用
                          </Badge>
                        )}
                        {!disabled && isSelected && <Check className="size-4 shrink-0 text-primary" />}
                      </button>
                    )
                  })}
              </div>
            </div>
          )}
        </div>

        <div className="mt-4 flex items-center justify-between gap-2">
          <p className="text-[11px] text-muted-foreground">选择后跳转对应门户并刷新对话列表</p>
          <Button type="button" variant="secondary" onClick={onClose}>
            取消
          </Button>
        </div>
      </div>
    </div>
  )
}
