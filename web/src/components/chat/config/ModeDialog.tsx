// ModeDialog —— 运行模式弹窗（P4-F）：single（单智能体）↔ orchestrate（多智能体编排）。
//
// 只编辑会话 config.mode（及 orchestrate 方案的 orchestrate_plan）；保存时保留
// 其它配置字段（经 mergeSessionConfig 全量合并）。orchestrate 模式下，用户消息
// 由服务端按编排方案拆解为子任务协作完成，过程进度经 SSE task_status 实时下发
// （前端渲染节点状态流）。
//
// 编排方案（P4-J 动态编排）：
//   - fixed（默认）：固定教研流水线（研究→大纲→正文→审核），流程可控、便宜；
//   - dynamic：LLM 动态分解子任务 DAG，更贴合开放性问题（多一次 LLM 调用）。
//
// 交互与 LLMDialog / AgentSwitcher 保持一致：combobox 单选下拉（展开为 absolute
// 覆盖层，不撑开弹窗高度），点击外部/Esc 关闭。
import { useEffect, useRef, useState } from 'react'
import { Check, ChevronDown, Network, Sparkles, UserRound, Workflow, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig } from '@/types/api'
import { useChatStore } from '@/stores/chat'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import { cn } from '@/lib/utils'

interface Props {
  sessionConfig?: SessionConfig
  onClose: () => void
}

const MODES: { value: 'single' | 'orchestrate'; label: string; desc: string }[] = [
  {
    value: 'single',
    label: '单智能体',
    desc: '一个智能体直接对话（可调用工具），响应快、成本低',
  },
  {
    value: 'orchestrate',
    label: '多智能体编排',
    desc: '内置教研角色池拆解协作（研究→大纲→正文→审核），质量更高、耗时更长',
  },
]

/** 编排方案选项（仅 orchestrate 模式展示；缺省 = fixed，向后兼容）。 */
const PLANS: { value: 'fixed' | 'dynamic'; label: string; desc: string }[] = [
  {
    value: 'fixed',
    label: '固定教研流水线',
    desc: '研究 → 大纲 → 正文 → 审核，流程可控、成本低',
  },
  {
    value: 'dynamic',
    label: '动态分解',
    desc: 'LLM 按目标实时拆解子任务 DAG，更灵活（多一次模型调用）',
  },
]

export default function ModeDialog({ sessionConfig, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)

  const [mode, setMode] = useState<'single' | 'orchestrate'>(sessionConfig?.mode ?? 'single')
  const [plan, setPlan] = useState<'fixed' | 'dynamic'>(sessionConfig?.orchestrate_plan ?? 'fixed')
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const listRef = useRef<HTMLDivElement>(null)

  const selected = MODES.find((m) => m.value === mode)

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
    setSaving(true)
    setSaveError('')
    try {
      const cfg: SessionConfig = { mode }
      if (mode === 'orchestrate') cfg.orchestrate_plan = plan
      await updateConfig(activeId, mergeSessionConfig(sessionConfig, cfg))
      onClose()
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-sm rounded-xl border bg-background p-5 shadow-xl">
        {/* 头部：图标 + 标题 + 关闭 */}
        <div className="mb-3 flex items-start justify-between gap-2">
          <div className="flex items-center gap-2.5">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-emerald-500/15 text-emerald-600 dark:text-emerald-300">
              <Workflow className="size-4" />
            </span>
            <div>
              <h2 className="text-sm font-semibold">运行模式</h2>
              <p className="text-[11px] text-muted-foreground">
                {sessionConfig?.mode === 'orchestrate' ? '当前：多智能体编排' : '当前：单智能体'}
              </p>
            </div>
          </div>
          <Button type="button" variant="ghost" size="icon" className="-mr-1 -mt-1 size-7" onClick={onClose} aria-label="关闭">
            <X className="size-4" />
          </Button>
        </div>

        {/* Combobox 触发按钮 */}
        <div className="relative mb-4" ref={listRef}>
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className={cn(
              'flex h-10 w-full items-center justify-between rounded-md border bg-background px-3 text-sm transition-colors focus:outline-none focus:ring-2 focus:ring-ring',
              open ? 'border-ring' : 'border-border hover:bg-accent/40',
            )}
          >
            <span className="flex min-w-0 items-center gap-2">
              <span className="flex size-6 shrink-0 items-center justify-center rounded bg-muted text-muted-foreground">
                {mode === 'single' ? <UserRound className="size-3.5" /> : <Network className="size-3.5" />}
              </span>
              <span className="min-w-0">
                <span className="block truncate font-medium">{selected?.label}</span>
                <span className="block truncate text-[11px] text-muted-foreground">{selected?.desc}</span>
              </span>
            </span>
            <ChevronDown className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
          </button>

          {open && (
            <div className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-md border bg-background shadow-lg">
              <div className="border-b px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                运行模式
              </div>
              <ul className="max-h-56 overflow-y-auto p-1">
                {MODES.map((m) => {
                  const active = mode === m.value
                  return (
                    <li key={m.value}>
                      <button
                        type="button"
                        role="option"
                        aria-selected={active}
                        onClick={() => {
                          setMode(m.value)
                          setOpen(false)
                        }}
                        className={cn(
                          'flex w-full items-center justify-between gap-2 rounded px-2.5 py-1.5 text-left text-sm transition-colors',
                          active ? 'bg-accent' : 'hover:bg-accent/50',
                        )}
                      >
                        <span className="flex min-w-0 items-center gap-2">
                          <span className="flex size-6 shrink-0 items-center justify-center rounded bg-muted text-muted-foreground">
                            {m.value === 'single' ? <UserRound className="size-3.5" /> : <Network className="size-3.5" />}
                          </span>
                          <span className="min-w-0">
                            <span className="block truncate font-medium">{m.label}</span>
                            <span className="block truncate text-xs text-muted-foreground">{m.desc}</span>
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

        {/* 编排方案（仅多智能体编排模式展示） */}
        {mode === 'orchestrate' && (
          <div className="mb-4">
            <p className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground">
              <Sparkles className="size-3" />
              编排方案
              <span className="text-muted-foreground/70">（决定子任务如何拆解与协作）</span>
            </p>
            <div className="grid grid-cols-1 gap-1.5">
              {PLANS.map((p) => {
                const active = plan === p.value
                return (
                  <button
                    key={p.value}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    onClick={() => setPlan(p.value)}
                    className={cn(
                      'flex items-start justify-between gap-2 rounded-md border border-border px-3 py-2 text-left transition-colors hover:bg-accent/40 focus:outline-none focus:ring-2 focus:ring-ring',
                    )}
                  >
                    <span className="min-w-0">
                      <span className="block text-sm font-medium">{p.label}</span>
                      <span className="block text-[11px] text-muted-foreground">{p.desc}</span>
                    </span>
                    {active && (
                      <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground">
                        <Check className="size-3.5" strokeWidth={3} />
                      </span>
                    )}
                  </button>
                )
              })}
            </div>
          </div>
        )}

        {saveError && <p className="mb-3 text-xs text-destructive">{saveError}</p>}

        <div className="flex items-center justify-between gap-2">
          <p className="text-[11px] text-muted-foreground">切换后对后续新消息生效，历史消息不受影响</p>
          <div className="flex gap-2">
            <Button type="button" variant="secondary" onClick={onClose} disabled={saving}>
              取消
            </Button>
            <Button type="button" onClick={handleSave} disabled={saving}>
              {saving ? '保存中…' : '保存'}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
