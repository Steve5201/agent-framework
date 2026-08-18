import { useEffect, useState } from 'react'
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom'
import {
  ArrowLeft,
  BookOpen,
  Bot,
  Boxes,
  Cpu,
  Database,
  HardDrive,
  Loader2,
  Menu,
  ScrollText,
  Server,
  ShieldCheck,
  Users,
  Wrench,
} from 'lucide-react'
import { adminListModules } from '@/lib/api'
import type { AdminModule } from '@/types/api'
import { ROLE_LABELS } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth'
import { Badge } from '@/components/ui/badge'
import { Button, buttonVariants } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/** 模块 key → 图标（顺序与后端 /v1/admin/modules 注册顺序一致；新增模块只需在此追加一行）。 */
const MODULE_ICONS: Record<string, React.ComponentType<{ className?: string }>> = {
  agents: Bot,
  skills: Wrench,
  kb: BookOpen,
  mcp: Server,
  users: Users,
  models: Cpu,
  data: Database,
  logs: ScrollText,
  'disk-quota': HardDrive,
}

function ModuleIcon({ m }: { m: AdminModule }) {
  const Icon = MODULE_ICONS[m.key] ?? Boxes
  return <Icon className="size-4 shrink-0" />
}

/** 模块分组（参照 ui_admin.html nav-section）：核心功能 / 系统，未知 key 归"其它"。 */
const MODULE_SECTIONS: { title: string; keys: string[] }[] = [
  { title: '核心功能', keys: ['agents', 'skills', 'kb', 'mcp', 'users', 'models', 'data'] },
  { title: '系统', keys: ['logs', 'disk-quota'] },
]

function groupModules(modules: AdminModule[]) {
  const known = new Set(MODULE_SECTIONS.flatMap((s) => s.keys))
  const groups = MODULE_SECTIONS.map((s) => ({
    title: s.title,
    items: modules.filter((m) => s.keys.includes(m.key)),
  }))
  const other = modules.filter((m) => !known.has(m.key))
  if (other.length > 0) groups.push({ title: '其它', items: other })
  return groups.filter((g) => g.items.length > 0)
}

/** 侧栏内容（桌面常驻 / 移动抽屉共用）。onNavigate：抽屉模式下点击导航后收起。 */
function SidebarContent({
  me,
  modules,
  error,
  onNavigate,
}: {
  me: ReturnType<typeof useAuthStore.getState>['user']
  modules: AdminModule[]
  error: string
  onNavigate?: () => void
}) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* 品牌区 */}
      <div className="relative overflow-hidden border-b px-4 py-4">
        <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-blue-500/10 via-transparent to-transparent" />
        <div className="relative flex items-center gap-2.5">
          <div className="flex size-8 items-center justify-center rounded-lg bg-blue-500/15 text-blue-600 dark:text-blue-400">
            <ShieldCheck className="size-4.5" />
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="text-sm font-semibold leading-tight">管理端</span>
              <Badge variant="outline" className="shrink-0 bg-blue-500/10 px-1.5 py-0 text-[10px] text-blue-600 dark:text-blue-300">
                {ROLE_LABELS[me?.role ?? ''] ?? me?.role ?? '管理员'}
              </Badge>
            </div>
            <div className="truncate text-[11px] text-muted-foreground">技能 / MCP / 智能体配置</div>
          </div>
        </div>
      </div>

      <nav className="flex-1 space-y-4 overflow-y-auto p-2">
        {error && (
          <div className="px-3 py-2 text-xs text-destructive">模块列表加载失败：{error}</div>
        )}
        {!error && modules.length === 0 && (
          <div className="flex justify-center py-6">
            <Loader2 className="size-4 animate-spin text-muted-foreground" />
          </div>
        )}
        {groupModules(modules).map((group) => (
          <div key={group.title}>
            <div className="px-2.5 pb-1.5 text-[11px] font-medium tracking-wider text-muted-foreground">
              {group.title}
            </div>
            <div className="space-y-0.5">
              {group.items.map((m) => (
                <NavLink
                  key={m.key}
                  to={`/admin/${m.key}`}
                  onClick={onNavigate}
                  className={({ isActive }) =>
                    cn(
                      'group relative flex items-center justify-between gap-2 rounded-md px-2.5 py-2 text-sm transition-colors',
                      isActive
                        ? 'bg-blue-500/10 font-medium text-blue-700 dark:text-blue-300'
                        : 'text-foreground/75 hover:bg-accent/70 hover:text-foreground',
                    )
                  }
                >
                  {({ isActive }) => (
                    <>
                      {isActive && (
                        <span className="absolute -left-2 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-full bg-blue-500" />
                      )}
                      <span className="flex min-w-0 items-center gap-2">
                        <ModuleIcon m={m} />
                        <span className="truncate">{m.name}</span>
                      </span>
                      <Badge
                        variant={m.implemented ? 'default' : 'secondary'}
                        className={cn(
                          'shrink-0 text-[10px]',
                          m.implemented &&
                            'bg-blue-500/10 text-blue-600 hover:bg-blue-500/10 dark:text-blue-300',
                        )}
                      >
                        {m.implemented ? '已实现' : '规划中'}
                      </Badge>
                    </>
                  )}
                </NavLink>
              ))}
            </div>
          </div>
        ))}
      </nav>

      {/* 底部用户档案卡（参照 ui_admin.html user-profile）：纯展示，操作走右上菜单 */}
      <div className="border-t p-2">
        <div className="flex items-center gap-2.5 rounded-md px-2 py-2">
          <div className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-blue-500/15 text-sm font-semibold text-blue-600 dark:text-blue-400">
            {me?.username?.charAt(0).toUpperCase() ?? '?'}
          </div>
          <div className="min-w-0">
            <div className="truncate text-sm font-medium">{me?.username ?? '未登录'}</div>
            <div className="truncate text-[11px] text-muted-foreground">
              {ROLE_LABELS[me?.role ?? ''] ?? me?.role ?? '管理员'}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/**
 * 管理端布局：左侧模块导航（元信息来自 GET /v1/admin/modules，动态渲染
 * "已实现 / 规划中"状态），右侧为当前模块内容。新模块后端注册后前端自动出现。
 */
export default function AdminLayout() {
  const me = useAuthStore((s) => s.user)
  const [modules, setModules] = useState<AdminModule[]>([])
  const [error, setError] = useState('')
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const { pathname } = useLocation()

  useEffect(() => {
    adminListModules()
      .then((list) => {
        setModules(list)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
  }, [])

  // 顶栏标题：取当前路由模块名（未知/加载中回退到路径段或"管理端"）
  const seg = pathname.split('/')[2] ?? ''
  const currentTitle = modules.find((m) => m.key === seg)?.name ?? (seg ? seg : '管理端')

  return (
    <div className="flex h-screen bg-muted/30 md:flex-row">
      {/* 桌面侧边栏（md 以上常驻） */}
      <aside className="hidden w-60 shrink-0 flex-col border-r bg-card md:flex">
        <SidebarContent me={me} modules={modules} error={error} />
      </aside>

      {/* 移动端抽屉：汉堡触发，覆盖全屏，点击遮罩/导航后收起 */}
      {sidebarOpen && (
        <div className="fixed inset-0 z-40 md:hidden">
          <div
            className="absolute inset-0 bg-black/40"
            onClick={() => setSidebarOpen(false)}
            aria-hidden
          />
          <aside className="absolute inset-y-0 left-0 w-60 border-r bg-card shadow-lg">
            <SidebarContent
              me={me}
              modules={modules}
              error={error}
              onNavigate={() => setSidebarOpen(false)}
            />
          </aside>
        </div>
      )}

      {/* 主区：sticky 顶栏（标题+面包屑，右侧返回对话）+ 内容 */}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center justify-between border-b bg-card px-4 md:px-6">
          <div className="flex min-w-0 items-center gap-2.5">
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => setSidebarOpen(true)}
              aria-label="打开管理端导航"
              className="shrink-0 md:hidden"
            >
              <Menu className="size-4.5" />
            </Button>
            <div className="min-w-0">
              <div className="truncate text-sm font-semibold md:text-base">{currentTitle}</div>
              <div className="hidden items-center gap-1.5 text-xs text-muted-foreground md:flex">
                <span>管理后台</span>
                <span className="text-muted-foreground/50">/</span>
                <span className="font-medium text-foreground">{currentTitle}</span>
              </div>
            </div>
          </div>
          <Link
            to="/"
            className={cn(buttonVariants({ variant: 'outline', size: 'sm' }), 'shrink-0')}
          >
            <ArrowLeft className="size-3.5" /> 返回对话
          </Link>
        </header>

        <main className="flex-1 overflow-y-auto">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
