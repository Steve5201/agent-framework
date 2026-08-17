import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Check, ChevronDown, Copy, GitBranch, Loader2, RefreshCw, Trash2 } from 'lucide-react'
import type { ChatMessage } from '@/types/api'
import { useChatStore } from '@/stores/chat'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// 消息气泡操作区（标准化开发管线）
//
// 【管线标准】新增一个气泡按钮 = 定义一个 MessageActionDef：
//   1. key    唯一键（React key）；
//   2. scope  显示范围，且只有两种，后期新增按钮也只在这两种中选择：
//        - 'every'           所有气泡都显示（如：复制、删除本轮）；
//        - 'last-assistant'  只在最后一条助手气泡显示（如：重新生成、分支）。
//   3. visible 附加可见条件（默认 true），如"已落库且非流式"；
//   4. onClick 点击回调（业务在 chat store，组件零业务逻辑）。
// 组件统一负责渲染与 scope 过滤：改功能不动 UI，改 UI 不动业务。
// ---------------------------------------------------------------------------

/** 气泡按钮显示范围：后期新增按钮只会有这两种。 */
export type MessageActionScope = 'every' | 'last-assistant'

export interface MessageActionDef {
  /** 唯一键（React key） */
  key: string
  icon: ReactNode
  /** 悬停提示 / 无障碍标签 */
  label: string
  /** 显示范围：every=所有气泡；last-assistant=仅最后一条助手气泡 */
  scope: MessageActionScope
  /** 附加可见条件（默认 true）——如"已落库且非流式" */
  visible?: boolean
  disabled?: boolean
  loading?: boolean
  onClick: () => void
}

interface MessageActionsProps {
  message: ChatMessage
  /** 自定义操作列表；缺省按角色生成默认操作集 */
  actions?: MessageActionDef[]
  /** 对齐方向（用户气泡靠右、助手气泡靠左） */
  align?: 'left' | 'right'
}

/** 单条操作按钮。 */
function ActionButton({ action }: { action: MessageActionDef }) {
  return (
    <button
      type="button"
      title={action.label}
      aria-label={action.label}
      disabled={action.disabled || action.loading}
      onClick={action.onClick}
      className={cn(
        'inline-flex items-center rounded p-1 text-muted-foreground transition-colors',
        'hover:bg-muted hover:text-foreground disabled:pointer-events-none disabled:opacity-40',
      )}
    >
      {action.loading ? <Loader2 className="size-3.5 animate-spin" /> : action.icon}
    </button>
  )
}

/** 版本切换下拉：同一轮有多个版本（重新生成过）时展示，点击切换活跃版本。
 *  注意：仅当 totalVersions > 1 时由父组件渲染本组件（hooks 顺序稳定）。 */
function VersionMenu({
  message,
  align,
  switchVersion,
}: {
  message: ChatMessage
  align: 'left' | 'right'
  switchVersion: (id: string, version: number) => Promise<void>
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState<number | null>(null)
  const ref = useRef<HTMLDivElement>(null)

  const total = message.totalVersions ?? 0
  const current = message.version ?? 0

  // 点击外部关闭下拉
  useEffect(() => {
    if (!open) return
    function onDocClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [open])

  return (
    <div ref={ref} className="relative inline-flex">
      <button
        type="button"
        title={`切换回答版本（共 ${total} 个）`}
        aria-label="切换回答版本"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={cn(
          'inline-flex items-center gap-0.5 rounded px-1 py-0.5 text-[11px] text-muted-foreground transition-colors',
          'hover:bg-muted hover:text-foreground',
        )}
      >
        <ChevronDown className="size-3" />
        <span>{current + 1}/{total}</span>
      </button>
      {open && (
        <div
          className={cn(
            'absolute top-full z-20 mt-1 min-w-[120px] overflow-hidden rounded-md border bg-popover p-1 shadow-md',
            align === 'right' ? 'right-0' : 'left-0',
          )}
        >
          {Array.from({ length: total }, (_, i) => i).map((v) => (
            <button
              key={v}
              type="button"
              disabled={v === current || busy !== null}
              onClick={async () => {
                setBusy(v)
                try {
                  await switchVersion(message.id, v)
                  setOpen(false)
                } catch (err) {
                  alert(`切换版本失败：${(err as Error).message}`)
                } finally {
                  setBusy(null)
                }
              }}
              className={cn(
                'flex w-full items-center justify-between rounded px-2 py-1.5 text-xs transition-colors',
                v === current
                  ? 'bg-accent font-medium'
                  : 'hover:bg-accent/60 disabled:opacity-40',
              )}
            >
              <span>版本 {v + 1}</span>
              {v === current && <Check className="size-3" />}
              {busy === v && <Loader2 className="size-3 animate-spin" />}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

/**
 * 按消息角色生成默认操作集（新增按钮在此注册，注明 scope）。
 *
 * 只有两种 scope（见 MessageActionScope）：
 *   - 'every'：所有气泡都显示（复制、删除本轮）；
 *   - 'last-assistant'：只在最后一条助手气泡显示（重新生成、分支）——
 *     它们基于"当前上下文末端"工作，中间轮次展示无意义，主流智能体均如此。
 * 具体可见条件（已落库、非流式等）写在 visible 上，由组件统一过滤。
 */
function buildActions(message: ChatMessage): MessageActionDef[] {
  const store = useChatStore.getState()
  const isStored = Boolean(message.serverId)
  const isStreaming = message.status === 'streaming'
  const regenerating = store.regeneratingId === message.id
  const actions: MessageActionDef[] = []

  // 复制：任意有内容的消息（所有轮次）。错误上抛，由组件层统一提示 + 反馈动画。
  if (message.content) {
    actions.push({
      key: 'copy',
      scope: 'every',
      icon: <Copy className="size-3.5" />,
      label: '复制内容',
      onClick: async () => {
        await store.copyMessage(message.id)
      },
    })
  }

  // 重新生成：仅最后一条助手气泡（scope 已限制位置；另需已落库、非流式、
  // 且一次只允许一个进行中）
  actions.push({
    key: 'regenerate',
    scope: 'last-assistant',
    icon: <RefreshCw className="size-3.5" />,
    label: '重新生成（保留旧版本，可切换）',
    visible: isStored && !isStreaming,
    disabled: Boolean(store.regeneratingId),
    loading: regenerating,
    onClick: () => {
      store
        .regenerateMessage(message.id)
        .catch((err) => alert(`重新生成失败：${(err as Error).message}`))
    },
  })

  // 分支：仅最后一条助手气泡（scope 已限制位置；另需已落库、非流式）
  actions.push({
    key: 'branch',
    scope: 'last-assistant',
    icon: <GitBranch className="size-3.5" />,
    label: '在此创建分支',
    visible: isStored && !isStreaming,
    onClick: () => {
      store
        .branchMessage(message.id)
        .catch((err) => alert(`创建分支失败：${(err as Error).message}`))
    },
  })

  // 删除整轮：所有已落库且非流式的气泡
  if (isStored && !isStreaming) {
    actions.push({
      key: 'delete',
      scope: 'every',
      icon: <Trash2 className="size-3.5" />,
      label: '删除本轮对话',
      onClick: async () => {
        if (!window.confirm('删除本轮完整对话（含提问与回答）？删除后不可恢复。')) return
        try {
          await store.deleteMessage(message.id)
        } catch (err) {
          alert(`删除失败：${(err as Error).message}`)
        }
      },
    })
  }

  return actions
}

/** 消息气泡操作区：位于气泡下方，用户与助手气泡共用。 */
export default function MessageActions({ message, actions, align = 'left' }: MessageActionsProps) {
  const switchVersion = useChatStore((s) => s.switchVersion)
  const messages = useChatStore((s) => s.messages)
  // 复制成功反馈：图标短暂切换为对勾（pop 动画），1.2s 后复位。
  const [copied, setCopied] = useState(false)
  const copyResetTimer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(copyResetTimer.current), [])

  // 最后一条助手消息 ID（末端操作的重生成/分支只出现在它下面）。
  // 流式结束重拉历史后 serverId 变化，同样会触发本组件重渲染。
  const lastAssistantId = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === 'assistant') return messages[i].id
    }
    return ''
  }, [messages])

  // 按 scope 统一过滤（管线核心）：'every' 恒显示；'last-assistant'
  // 仅当本消息是最后一条助手气泡时显示；再叠加各动作的 visible 条件。
  const isLastAssistant = message.role === 'assistant' && message.id === lastAssistantId
  const list = (actions ?? buildActions(message)).filter(
    (a) => a.visible !== false && (a.scope === 'every' || isLastAssistant),
  )
  if (list.length === 0) return null

  return (
    <div
      className={cn(
        'mt-1 flex items-center gap-0.5 text-muted-foreground',
        align === 'right' ? 'justify-end' : 'justify-start',
      )}
    >
      {list.map((a) =>
        a.key === 'copy' ? (
          <ActionButton
            key={a.key}
            action={{
              ...a,
              icon: copied ? <Check className="size-3.5 msg-copy-pop" /> : a.icon,
              onClick: async () => {
                try {
                  await a.onClick()
                  setCopied(true)
                  window.clearTimeout(copyResetTimer.current)
                  copyResetTimer.current = window.setTimeout(() => setCopied(false), 1200)
                } catch (err) {
                  alert(`复制失败：${(err as Error).message}`)
                }
              },
            }}
          />
        ) : (
          <ActionButton key={a.key} action={a} />
        ),
      )}
      {/* 版本切换独立于操作列表渲染（带下拉子菜单，位置跟随气泡）；仅多版本时出现 */}
      {message.role === 'assistant' && (message.totalVersions ?? 0) > 1 && (
        <VersionMenu message={message} align={align} switchVersion={switchVersion} />
      )}
    </div>
  )
}
