import { useEffect, useRef, useState } from 'react'
import { Bot, Check, ChevronDown, Cpu, Info, Loader2, Star, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig } from '@/types/api'
import { getAgentDefaults, listPublicModels } from '@/lib/api'
import { DEFAULT_AGENT_ID } from '@/lib/roles'
import { useChatStore } from '@/stores/chat'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import { cn } from '@/lib/utils'

/**
 * LLMDialog 大模型选择弹窗（对话配置区"大模型"，P3 大模型管理）。
 *
 * 单选交互复用 AgentSwitcher 的 Combobox 模式。模型列表来自公开端点
 * /v1/models（经 gateway 代理到 llm-gateway，只含启用的模型，无任何密钥）。
 *
 * 【绑定语义（P3 反馈）】只绑定确切的大模型：选中任何一项（含系统默认）都
 * 写死 config.model = 该模型名，llm-gateway 按名路由。回退链仅在"加载会话时
 * 发现之前绑定的大模型已不存在（被删除/禁用）"才触发：
 *   1. 会话绑定的模型（config.model，存在且在列表中）→ 直接绑定显示；
 *   2. 会话模型失效/未设置 → 取智能体默认配置大模型（GET /v1/agent/defaults）；
 *   3. 仍无 → 系统默认模型。
 * 弹窗打开期间 3s 轮询，管理端增删/启停实时反映；当前选中项失效时按同一
 * 回退链重选，不覆盖用户已选的合法模型。
 */

interface Props {
  sessionConfig?: SessionConfig
  /** 会话所属智能体域（回退链读取智能体默认配置用；''/缺省 = 后端默认域） */
  agentId?: string
  onClose: () => void
}

interface PublicModel {
  name: string
  provider_name?: string
  is_default?: boolean
}

/** 回退链第 2/3 步：智能体默认配置大模型优先，其次系统默认。 */
async function resolveFallbackModel(list: PublicModel[], agentId?: string): Promise<string> {
  const defName = list.find((m) => m.is_default)?.name ?? ''
  try {
    const defaults = await getAgentDefaults(agentId || DEFAULT_AGENT_ID)
    const m = defaults.model ?? ''
    if (m && list.some((x) => x.name === m)) return m
  } catch {
    /* 智能体默认配置读取失败不阻断：回退系统默认 */
  }
  return defName
}

export default function LLMDialog({ sessionConfig, agentId, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)

  const [models, setModels] = useState<PublicModel[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState('')
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const listRef = useRef<HTMLDivElement>(null)

  // 首载：拉取列表 + 初始化选中（会话绑定模型 → 智能体默认 → 系统默认回退链）。
  useEffect(() => {
    let cancelled = false
    void (async () => {
      // 异步路径内进入加载态：避免 effect 同步 setState 级联渲染
      setLoading(true)
      setError('')
      try {
        const list = await listPublicModels()
        if (cancelled) return
        setModels(list)
        const cur = sessionConfig?.model ?? ''
        if (cur && list.some((m) => m.name === cur)) {
          // 会话已绑定该模型且仍可用：直接绑定显示（用户诉求：改完重开应显示所选模型）。
          setSelected(cur)
        } else {
          // 绑定模型失效/未设置：优先智能体默认配置大模型，再回退系统默认。
          const fallback = await resolveFallbackModel(list, agentId)
          if (!cancelled) setSelected(fallback)
        }
      } catch (e) {
        if (!cancelled) setError((e as Error).message)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 轮询：管理端增删/启停模型实时反映到配置区。当前选中项被删除/禁用时，
  // 按回退链（智能体默认配置大模型 → 系统默认）重选，而非直接落到系统默认。
  useEffect(() => {
    let cancelled = false
    const timer = window.setInterval(() => {
      void (async () => {
        try {
          const list = await listPublicModels()
          if (cancelled) return
          setModels(list)
          setError('')
          setSelected((cur) => {
            if (cur && list.some((m) => m.name === cur)) return cur // 合法选中，不覆盖
            return '' // 触发下方回退（异步拉取智能体默认后落定）
          })
        } catch {
          /* 轮询失败静默，下次重试 */
        }
      })()
    }, 3000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [])

  // 轮询回退链的落定：selected 被置空（原选中失效）时，异步取回退模型。
  // 用独立的 effect 响应 selected===''，避免在 setInterval 里叠加异步竞态。
  useEffect(() => {
    if (selected !== '' || models.length === 0) return
    let cancelled = false
    void resolveFallbackModel(models, agentId).then((m) => {
      if (!cancelled) setSelected(m)
    })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected, models])

  // 点击外部关闭下拉
  useEffect(() => {
    if (!open) return
    const onDocClick = (e: MouseEvent) => {
      if (listRef.current && !listRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [open])

  // Esc 关闭弹窗
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const handleSave = async () => {
    if (!activeId) return
    if (!selected) {
      setSaveError('暂无可选模型，无法保存')
      return
    }
    setSaving(true)
    setSaveError('')
    try {
      // 只绑定确切模型：无论是否系统默认都写死 config.model（P3 反馈），
      // 其余配置经 mergeSessionConfig 全量保留。llm-gateway 按名路由。
      const config = mergeSessionConfig(sessionConfig, { model: selected })
      await updateConfig(activeId, config)
      onClose()
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const selectedModel = models.find((m) => m.name === selected)

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-sm rounded-xl border bg-background p-5 shadow-xl">
        {/* 头部：图标 + 标题 + 关闭 */}
        <div className="mb-3 flex items-start justify-between gap-2">
          <div className="flex items-center gap-2.5">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-violet-500/15 text-violet-600 dark:text-violet-300">
              <Bot className="size-4" />
            </span>
            <div>
              <h2 className="text-sm font-semibold">大模型</h2>
              <p className="text-[11px] text-muted-foreground">
                {sessionConfig?.model
                  ? `当前绑定：${sessionConfig.model}`
                  : '尚未绑定（保存后按所选模型锁定）'}
              </p>
            </div>
          </div>
          <Button type="button" variant="ghost" size="icon" className="-mr-1 -mt-1 size-7" onClick={onClose} aria-label="关闭">
            <X className="size-4" />
          </Button>
        </div>

        <div className="mb-4 flex items-start gap-1.5 rounded-md border bg-muted/30 px-3 py-2 text-xs leading-relaxed text-muted-foreground">
          <Info className="mt-0.5 size-3 shrink-0" />
          <span>
            本会话固定使用所选大模型（含系统默认也按所选绑定）；若绑定模型已被删除/停用，自动回退智能体默认配置模型，再回退系统默认。
          </span>
        </div>

        {loading ? (
          <div className="flex items-center gap-2 py-6 text-xs text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" /> 加载中…
          </div>
        ) : (
          <div className="relative mb-4" ref={listRef}>
            <button
              type="button"
              onClick={() => setOpen((v) => !v)}
              disabled={models.length === 0}
              className={cn(
                'flex h-10 w-full items-center justify-between rounded-md border bg-background px-3 text-sm transition-colors focus:outline-none focus:ring-2 focus:ring-ring',
                open ? 'border-ring' : 'border-border hover:bg-accent/40',
                models.length === 0 && 'cursor-not-allowed opacity-60',
              )}
            >
              {selectedModel ? (
                <span className="flex min-w-0 items-center gap-2">
                  <span
                    className={cn(
                      'flex size-6 shrink-0 items-center justify-center rounded',
                      selectedModel.is_default
                        ? 'bg-amber-500/15 text-amber-600 dark:text-amber-300'
                        : 'bg-muted text-muted-foreground',
                    )}
                  >
                    <Cpu className="size-3.5" />
                  </span>
                  <span className="min-w-0">
                    <span className="flex items-center gap-1.5 font-medium">
                      <span className="truncate">{selectedModel.name}</span>
                      {selectedModel.is_default && (
                        <span className="flex shrink-0 items-center gap-0.5 text-[10px] text-amber-600 dark:text-amber-300">
                          <Star className="size-2.5 fill-current" /> 系统默认
                        </span>
                      )}
                    </span>
                    {selectedModel.provider_name && (
                      <span className="block truncate text-[11px] text-muted-foreground">{selectedModel.provider_name}</span>
                    )}
                  </span>
                </span>
              ) : (
                <span className="text-muted-foreground">暂无可选模型</span>
              )}
              <ChevronDown className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
            </button>

            {open && (
              <div className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-md border bg-background shadow-lg">
                <div className="border-b px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  可选模型
                </div>
                <ul className="max-h-56 overflow-y-auto p-1">
                  {models.map((m) => {
                    const active = selected === m.name
                    return (
                      <li key={m.name}>
                        <button
                          type="button"
                          role="option"
                          aria-selected={active}
                          onClick={() => {
                            setSelected(m.name)
                            setOpen(false)
                          }}
                          className={cn(
                            'flex w-full items-center justify-between gap-2 rounded px-2.5 py-1.5 text-left text-sm transition-colors',
                            active ? 'bg-accent' : 'hover:bg-accent/50',
                          )}
                        >
                          <span className="flex min-w-0 items-center gap-2">
                            <span
                              className={cn(
                                'flex size-6 shrink-0 items-center justify-center rounded',
                                m.is_default
                                  ? 'bg-amber-500/15 text-amber-600 dark:text-amber-300'
                                  : 'bg-muted text-muted-foreground',
                              )}
                            >
                              <Cpu className="size-3.5" />
                            </span>
                            <span className="min-w-0">
                              <span className="flex items-center gap-1.5 font-medium">
                                <span className="truncate">{m.name}</span>
                                {m.is_default && (
                                  <span className="shrink-0 text-[10px] text-amber-600 dark:text-amber-300">默认</span>
                                )}
                              </span>
                              {m.provider_name && (
                                <span className="block truncate text-xs text-muted-foreground">{m.provider_name}</span>
                              )}
                            </span>
                          </span>
                          {active && <Check className="size-4 shrink-0 text-primary" />}
                        </button>
                      </li>
                    )
                  })}
                </ul>
              </div>
            )}
          </div>
        )}

        {error && <p className="mb-3 text-xs text-destructive">{error}</p>}
        {saveError && <p className="mb-3 text-xs text-destructive">{saveError}</p>}

        <div className="flex items-center justify-between gap-2">
          <p className="text-[11px] text-muted-foreground">绑定模型失效时自动回退，不影响历史会话</p>
          <div className="flex gap-2">
            <Button type="button" variant="secondary" onClick={onClose} disabled={saving}>
              取消
            </Button>
            <Button type="button" onClick={handleSave} disabled={saving || loading || !!error || !selected}>
              {saving ? '保存中…' : '保存'}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
