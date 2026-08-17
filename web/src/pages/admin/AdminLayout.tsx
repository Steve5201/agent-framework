import { useEffect, useState } from 'react'
import { Link, NavLink, Outlet } from 'react-router-dom'
import {
  ArrowLeft,
  BookOpen,
  Bot,
  Boxes,
  Cpu,
  Database,
  HardDrive,
  Loader2,
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

/**
 * 管理端布局：左侧模块导航（元信息来自 GET /v1/admin/modules，动态渲染
 * "已实现 / 规划中"状态），右侧为当前模块内容。新模块后端注册后前端自动出现。
 */
export default function AdminLayout() {
  const me = useAuthStore((s) => s.user)
  const [modules, setModules] = useState<AdminModule[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    adminListModules()
      .then((list) => {
        setModules(list)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
  }, [])

  return (
    <div className="flex h-screen bg-muted/30">
      {/* 侧边栏：卡片化，微渐变品牌区 + 模块导航 */}
      <aside className="flex w-60 shrink-0 flex-col border-r bg-background">
        <div className="relative overflow-hidden border-b px-4 py-4">
          <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-indigo-500/10 via-transparent to-transparent" />
          <div className="relative flex items-center gap-2.5">
            <div className="flex size-8 items-center justify-center rounded-lg bg-indigo-500/15 text-indigo-600 dark:text-indigo-400">
              <ShieldCheck className="size-4.5" />
            </div>
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="text-sm font-semibold leading-tight">管理端</span>
                <Badge variant="outline" className="shrink-0 bg-indigo-500/10 px-1.5 py-0 text-[10px] text-indigo-600 dark:text-indigo-300">
                  {ROLE_LABELS[me?.role ?? ''] ?? me?.role ?? '管理员'}
                </Badge>
              </div>
              <div className="truncate text-[11px] text-muted-foreground">技能 / MCP / 智能体配置</div>
            </div>
          </div>
        </div>

        <div className="px-3 pt-3 text-[11px] font-medium tracking-wide text-muted-foreground">
          功能模块
        </div>
        <nav className="flex-1 space-y-0.5 overflow-y-auto p-2">
          {error && (
            <div className="px-3 py-2 text-xs text-destructive">模块列表加载失败：{error}</div>
          )}
          {!error && modules.length === 0 && (
            <div className="flex justify-center py-6">
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
            </div>
          )}
          {modules.map((m) => (
            <NavLink
              key={m.key}
              to={`/admin/${m.key}`}
              className={({ isActive }) =>
                cn(
                  'group relative flex items-center justify-between gap-2 rounded-md px-2.5 py-2 text-sm transition-colors',
                  isActive
                    ? 'bg-indigo-500/10 font-medium text-indigo-700 dark:text-indigo-300'
                    : 'text-foreground/75 hover:bg-accent/70 hover:text-foreground',
                )
              }
            >
              {({ isActive }) => (
                <>
                  {isActive && (
                    <span className="absolute -left-2 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-full bg-indigo-500" />
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
                        'bg-indigo-500/10 text-indigo-600 hover:bg-indigo-500/10 dark:text-indigo-300',
                    )}
                  >
                    {m.implemented ? '已实现' : '规划中'}
                  </Badge>
                </>
              )}
            </NavLink>
          ))}
        </nav>

        <div className="border-t p-2">
          <Link
            to="/"
            className="flex items-center gap-2 rounded-md px-2.5 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent/70 hover:text-foreground"
          >
            <ArrowLeft className="size-4" /> 返回对话
          </Link>
        </div>
      </aside>

      <main className="flex-1 overflow-y-auto">
        <Outlet />
      </main>
    </div>
  )
}
