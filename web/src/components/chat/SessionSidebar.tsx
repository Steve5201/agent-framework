import { useState, type KeyboardEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Plus, Trash2, LogIn, Loader2, Power, RefreshCw, Pencil, Settings2 } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'
import { isTauri } from '@/lib/storage'
import { getPortalAgentId } from '@/lib/portal'
import { DEFAULT_AGENT_ID } from '@/lib/roles'
import { Button } from '@/components/ui/button'
import { Avatar } from '@/components/ui/avatar'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import MenuButton from '@/menus/MenuButton'

interface Props {
  /** 移动端点击会话/新建后关闭抽屉 */
  onNavigate?: () => void
}

/** 相对时间（会话列表）：刚刚 / N分钟前 / 今天 HH:mm / 昨天 / N天前 / 日期 */
function relativeTime(iso: string): string {
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const now = new Date()
  const mins = Math.floor((now.getTime() - t.getTime()) / 60000)
  if (mins < 1) return '刚刚'
  if (mins < 60) return `${mins}分钟前`
  if (now.toDateString() === t.toDateString()) {
    return `今天 ${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
  }
  const days = Math.floor(mins / 1440)
  if (days === 1) return '昨天'
  if (days < 7) return `${days}天前`
  return `${t.getFullYear()}/${t.getMonth() + 1}/${t.getDate()}`
}

/** 单个会话行：单击选中、双击重命名、悬停删除。 */
function SessionRow({
  session,
  active,
}: {
  session: { id: string; title: string; updated_at: string }
  active: boolean
}) {
  const selectSession = useChatStore((s) => s.selectSession)
  const renameSession = useChatStore((s) => s.renameSession)
  const deleteSession = useChatStore((s) => s.deleteSession)

  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(session.title)

  function commit() {
    setEditing(false)
    const title = value.trim()
    if (!title || title === session.title) {
      setValue(session.title)
      return
    }
    renameSession(session.id, title).catch((err) => alert(`重命名失败：${(err as Error).message}`))
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') commit()
    if (e.key === 'Escape') {
      setEditing(false)
      setValue(session.title)
    }
  }

  return (
    <li className="group relative">
      {editing ? (
        <Input
          autoFocus
          value={value}
          maxLength={100}
          onChange={(e) => setValue(e.target.value)}
          onBlur={commit}
          onKeyDown={onKeyDown}
          className="h-8 px-2 text-sm"
          aria-label="会话名称"
        />
      ) : (
        <button
          type="button"
          onClick={() => void selectSession(session.id)}
          onDoubleClick={() => {
            setValue(session.title)
            setEditing(true)
          }}
          title={session.title || '新对话'}
          className={cn(
            'relative w-full cursor-pointer rounded-md px-3 py-2 pl-4 text-left text-sm transition-colors',
            active
              ? 'bg-primary/10 font-medium text-primary'
              : 'text-foreground/90 hover:bg-accent/60',
          )}
        >
          {active && (
            <span
              className="absolute left-0 top-1/2 h-5 w-[3px] -translate-y-1/2 rounded-full bg-primary"
              aria-hidden
            />
          )}
          <span className="flex min-w-0 items-baseline gap-2 pr-9">
            <span className="truncate">{session.title || '新对话'}</span>
            <span className="shrink-0 text-[11px] text-muted-foreground/70">
              {relativeTime(session.updated_at)}
            </span>
          </span>
        </button>
      )}
      {!editing && (
        <>
          <button
            type="button"
            title="重命名"
            aria-label="重命名"
            onClick={() => {
              setValue(session.title)
              setEditing(true)
            }}
            className={cn(
              'absolute right-7 top-1/2 -translate-y-1/2 cursor-pointer rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-background hover:text-foreground',
              'group-hover:opacity-100 focus-visible:opacity-100',
            )}
          >
            <Pencil className="size-3.5" />
          </button>
          <button
            type="button"
            title="删除会话"
            aria-label="删除会话"
            onClick={() => {
              if (!window.confirm(`删除会话「${session.title || '新对话'}」？不可恢复。`)) return
              deleteSession(session.id).catch((err) => alert(`删除失败：${(err as Error).message}`))
            }}
            className={cn(
              'absolute right-1.5 top-1/2 -translate-y-1/2 cursor-pointer rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-background hover:text-destructive',
              'group-hover:opacity-100 focus-visible:opacity-100',
            )}
          >
            <Trash2 className="size-3.5" />
          </button>
        </>
      )}
    </li>
  )
}

/** 会话侧栏：会话列表（独立滚动）+ 新建 + 用户登出。 */
export default function SessionSidebar({ onNavigate }: Props) {
  const sessions = useChatStore((s) => s.sessions)
  const activeId = useChatStore((s) => s.activeId)
  const sessionsLoading = useChatStore((s) => s.sessionsLoading)
  const createSession = useChatStore((s) => s.createSession)
  const loadSessions = useChatStore((s) => s.loadSessions)

  const user = useAuthStore((s) => s.user)
  const status = useAuthStore((s) => s.status)

  const navigate = useNavigate()
  const location = useLocation()

  const isGuest = status === 'guest'

  // 当前所在智能体域（用于游客登录入口 / 退出后的游客落地页）
  const isAgentRoute = location.pathname.startsWith('/agent/')
  const currentAgentId = isAgentRoute ? location.pathname.split('/')[2] ?? '' : ''
  // 登录目标：优先当前门户地址；桌面端无地址栏时退回已配置门户；否则默认门户
  const loginTarget = isAgentRoute
    ? `/login/${currentAgentId}`
    : isTauri()
      ? `/login/${getPortalAgentId() || DEFAULT_AGENT_ID}`
      : '/login'

  /** 游客进入登录页（保留当前智能体门户） */
  function goLogin() {
    void navigate(loginTarget)
    onNavigate?.()
  }

  /** 退出整个桌面应用（仅 Tauri 环境有此入口；浏览器无此概念）。 */
  async function quitApp() {
    try {
      const { invoke } = await import('@tauri-apps/api/core')
      await invoke('app_exit')
    } catch {
      /* 非 Tauri 环境或 IPC 失败：静默忽略 */
    }
  }

  return (
    <div className="flex h-full flex-col">
      {/* 顶栏：应用名 + 刷新（新建对话为下方全宽主按钮） */}
      <div className="border-b px-3 pb-2.5 pt-2.5">
        <div className="flex items-center justify-between">
          <span className="text-sm font-semibold">智能体助手</span>
          <div className="flex items-center gap-0.5">
            {isTauri() && (
              <Button
                variant="ghost"
                size="icon"
                title="门户配置（切换要连接的智能体门户）"
                aria-label="门户配置"
                onClick={() => {
                  void navigate('/portal')
                  onNavigate?.()
                }}
              >
                <Settings2 className="size-4" />
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon"
              title="刷新会话列表（同步其它端的新会话）"
              aria-label="刷新会话列表"
              onClick={() => void loadSessions()}
              disabled={sessionsLoading}
            >
              <RefreshCw className={cn('size-4', sessionsLoading && 'animate-spin')} />
            </Button>
          </div>
        </div>
        <Button
          className="mt-2 w-full"
          title="新建会话"
          onClick={() => {
            void createSession()
              .then(onNavigate)
              .catch((err) => alert(`新建会话失败：${(err as Error).message}`))
          }}
        >
          <Plus /> 新建对话
        </Button>
      </div>

      {/* 会话列表（独立滚动区） */}
      <div className="flex-1 overflow-y-auto p-2">
        {sessionsLoading && sessions.length === 0 ? (
          <div className="flex justify-center py-6 text-muted-foreground">
            <Loader2 className="animate-spin" />
          </div>
        ) : sessions.length === 0 ? (
          <div className="px-3 py-6 text-center text-xs text-muted-foreground">
            暂无会话，点击右上角新建
          </div>
        ) : (
          <ul className="space-y-0.5">
            {sessions.map((s) => (
              <SessionRow key={s.id} session={s} active={s.id === activeId} />
            ))}
          </ul>
        )}
      </div>

      {/* 底部：游客 → 登录入口 + 退出应用（桌面端）；已登录 → 用户 + 管理端 / 退出 */}
      {isGuest ? (
        <div className="flex items-center justify-between border-t px-3 py-2.5">
          <div className="flex min-w-0 items-center gap-2">
            <Avatar fallback="游" />
            <span className="truncate text-xs text-muted-foreground">游客模式</span>
          </div>
          <div className="flex items-center gap-0.5">
            {isTauri() && (
              <Button
                variant="ghost"
                size="icon"
                title="退出应用"
                aria-label="退出应用"
                onClick={() => void quitApp()}
              >
                <Power className="size-3.5" />
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon"
              title="登录 / 注册"
              aria-label="登录"
              onClick={goLogin}
            >
              <LogIn className="size-4" />
            </Button>
          </div>
        </div>
      ) : (
        <div className="border-t p-1.5">
          {/* 用户区：Avatar+用户名 纯展示；菜单入口为独立三角按钮（点击弹出菜单）。
              原分散在底部的 3 个图标按钮已收拢到菜单系统（src/menus，注册表驱动）。 */}
          <div className="flex min-w-0 items-center gap-2 px-2 py-1.5">
            <Avatar fallback={user?.username} />
            <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{user?.username}</span>
            <MenuButton />
          </div>
        </div>
      )}
    </div>
  )
}
