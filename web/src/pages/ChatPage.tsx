import { useEffect } from 'react'
import { Link, useParams } from 'react-router-dom'
import { LogIn, Menu, ShieldAlert } from 'lucide-react'
import { useChatStore } from '@/stores/chat'
import { useAuthStore } from '@/stores/auth'
import { isTauri } from '@/lib/storage'
import { DEFAULT_AGENT_ID, loadRememberedAgent } from '@/lib/roles'
import SessionSidebar from '@/components/chat/SessionSidebar'
import MenuButton from '@/menus/MenuButton'
import MessageList from '@/components/chat/MessageList'
import ChatInput from '@/components/chat/ChatInput'
import LocalToolModal from '@/components/chat/LocalToolModal'
import { Button, buttonVariants } from '@/components/ui/button'
import { cn } from '@/lib/utils'

// 模块级：跨组件挂载跟踪上次登录态。游客 → 登录页 → 回本域的流程会卸载/重挂
// ChatPage，useRef 在重挂时丢失旧值（agentId 相同 initAgent 会跳过、status 与
// 初始值相同不触发），导致"登录后不刷新会话列表"。模块级变量可跨挂载感知
// guest → authed / authed → guest 的变化并强制 resetScope（对每个域均生效）。
let lastStatus: 'loading' | 'authed' | 'guest' | null = null

/**
 * 对话主页面（阶段2·独立地址 + 游客模式）：
 *  - mode="agent"：/agent/:agentId，每个智能体独立地址；未登录默认游客
 *    （无配置按钮区，仅提供登录入口），登录后展示完整配置区。
 *  - mode="admin"：/admin/chat，管理端对话域（仅管理员，AdminGuard 拦截）。
 *    会话域回退到切换器记住的智能体（loadRememberedAgent）——从管理端返回
 *    对话界面时，新建会话与会话列表严格遵循当前选中的智能体归属；无记忆则
 *    回退管理端域（''）。
 *  - 桌面：左侧固定侧栏 + 右侧消息区/输入框
 *  - 移动：侧栏为抽屉，主区带顶栏菜单按钮
 *  - 消息区与会话列表各自独立滚动（高信息密度布局）
 */
export default function ChatPage({ mode }: { mode: 'agent' | 'admin' }) {
  const { agentId } = useParams<{ agentId?: string }>()
  const initAgent = useChatStore((s) => s.initAgent)
  const resetScope = useChatStore((s) => s.resetScope)
  const sidebarOpen = useChatStore((s) => s.sidebarOpen)
  const setSidebarOpen = useChatStore((s) => s.setSidebarOpen)
  const status = useAuthStore((s) => s.status)

  const scope = mode === 'agent' ? (agentId ?? '') : loadRememberedAgent()
  const isGuest = status === 'guest'
  const loginUrl = `/login/${scope}`

  // 超管专属门户（/agent/*）未登录即禁止对话：游客态只渲染登录提示页。
  // '*' 不是注册表里的真实智能体（后端 validateAgentID 拒绝以它建会话），
  // 游客对话必然失败，故直接拦截，流程与"超管在自己的 * 域登录"统一。
  const superPortalBlocked = mode === 'agent' && scope === '*' && isGuest

  useEffect(() => {
    // 超管域游客被拦截：不加载会话列表（避免拉跨域列表 / 触发后端拒绝）
    if (superPortalBlocked) return
    initAgent(scope)
  }, [initAgent, scope, superPortalBlocked])

  // 登录/登出切换（游客 ↔ 登录用户）后强制重拉会话列表：
  // 不同身份的会话不同，且登出后旧列表必须清空。用模块级 lastStatus
  // 而非 useRef——重挂载时仍能感知跨实例的登录态变化。
  useEffect(() => {
    if (lastStatus === null) {
      lastStatus = status
      return
    }
    if (lastStatus !== status && status !== 'loading' && !superPortalBlocked) {
      resetScope(scope)
    }
    lastStatus = status
  }, [resetScope, scope, status, superPortalBlocked])

  // 桌面端重开兜底：auth hydrate 完成（登录态最终确认）后按最终身份重拉列表。
  // persist 恢复的 user/status 早于 Tauri 异步 token 就绪，挂载时的初始
  // loadSessions 可能以游客身份拉到空/旧列表——hydrate 完成后必须兜底刷新。
  useEffect(() => {
    const onAuthHydrated = () => {
      if (!superPortalBlocked) void resetScope(scope)
    }
    window.addEventListener('agent:auth-hydrated', onAuthHydrated)
    return () => window.removeEventListener('agent:auth-hydrated', onAuthHydrated)
  }, [resetScope, scope, superPortalBlocked])

  // 桌面端窗口重新激活（后台恢复/托盘回显，页面未重载）时刷新列表：
  // 期间其它端新建的会话在这里同步显示，无需手动点刷新按钮。
  useEffect(() => {
    if (!isTauri()) return
    const onFocus = () => {
      if (superPortalBlocked) return
      void useChatStore.getState().loadSessions()
    }
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [superPortalBlocked])

  // 智能体域名称展示（管理端域固定文案；agent 域用 URL 中的 ID，'*' = 超管专属门户）
  const scopeTitle =
    mode === 'admin'
      ? scope
        ? `管理端对话 · ${scope}`
        : '管理端对话'
      : scope === '*'
        ? '超管专属门户'
        : `智能体 ${scope || '默认'}`

  // 超管专属门户游客态：不渲染对话界面，仅展示登录提示（见 ChatPage 头注释）
  if (superPortalBlocked) {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <div className="w-full max-w-sm space-y-4 text-center">
          <ShieldAlert className="mx-auto size-10 text-muted-foreground" />
          <div>
            <h2 className="text-lg font-semibold">超管专属门户</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              该门户仅供最高超管登录使用，游客模式不开放对话。
              <br />
              请登录后进入管理端对话，或以游客身份访问普通智能体门户。
            </p>
          </div>
          <div className="flex flex-col gap-2">
            <Link
              to={loginUrl}
              className={cn(buttonVariants(), 'w-full')}
              aria-label="超管门户登录"
            >
              <LogIn className="mr-1.5 h-4 w-4" />
              登录
            </Link>
            <Link
              to={`/agent/${DEFAULT_AGENT_ID}`}
              className={cn(buttonVariants({ variant: 'outline' }), 'w-full')}
            >
              以游客身份访问默认智能体
            </Link>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full overflow-hidden">
      {/* 桌面侧栏 */}
      <aside className="hidden w-64 shrink-0 border-r bg-background md:block">
        <SessionSidebar />
      </aside>

      {/* 移动端抽屉 */}
      {sidebarOpen && (
        <div className="fixed inset-0 z-40 md:hidden">
          <div
            className="absolute inset-0 bg-black/40"
            onClick={() => setSidebarOpen(false)}
            aria-hidden
          />
          <aside className="absolute inset-y-0 left-0 w-72 border-r bg-background shadow-lg">
            <SessionSidebar onNavigate={() => setSidebarOpen(false)} />
          </aside>
        </div>
      )}

      <main className="flex min-w-0 flex-1 flex-col">
        {/* 移动端顶栏 */}
        <div className="flex items-center border-b px-2 py-1.5 md:hidden">
          <Button
            variant="ghost"
            size="icon"
            onClick={() => setSidebarOpen(true)}
            aria-label="打开会话列表"
          >
            <Menu />
          </Button>
          <span className="ml-1 truncate text-sm font-medium">{scopeTitle}</span>
          {isGuest && mode === 'agent' ? (
            <Link
              to={loginUrl}
              className={cn(buttonVariants({ variant: 'ghost', size: 'sm' }), 'ml-auto')}
            >
              <LogIn className="mr-1 h-3.5 w-3.5" />
              登录
            </Link>
          ) : (
            /* 登录用户：顶栏右侧菜单入口（设置/管理端/退出登录等，注册表驱动） */
            <MenuButton className="ml-auto" />
          )}
        </div>

        {/* 游客模式提示条（桌面端） */}
        {isGuest && mode === 'agent' && (
          <div className="flex items-center justify-center gap-3 border-b bg-muted/40 px-4 py-2 text-sm">
            <span className="text-muted-foreground">
              游客模式：对话可正常使用，能力/技能配置需登录后开放。
            </span>
            <Link
              to={loginUrl}
              className={cn(buttonVariants({ variant: 'outline', size: 'sm' }), 'cursor-pointer')}
            >
              <LogIn className="mr-1 h-3.5 w-3.5" />
              登录 / 注册
            </Link>
          </div>
        )}

        {/* 消息区（独立滚动） */}
        <div className="flex-1 overflow-y-auto">
          <MessageList />
        </div>

        {/* 游客模式隐藏配置按钮区（能力/思考/技能）；登录或管理端展示 */}
        <ChatInput canConfigure={!isGuest} />
      </main>

      {/* 本地工具确认弹窗（仅桌面端触发） */}
      <LocalToolModal />
    </div>
  )
}
