import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { ChevronLeft, ChevronRight, X } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { filterMenuItems } from './registry'
import './items' // 触发各菜单功能自注册（模块加载即 registerMenu，注册表驱动）
import type { MenuCtx, MenuItem } from './types'
import { cn } from '@/lib/utils'

/**
 * 菜单面板（标准系统菜单）：列表 ↔ 子界面两级结构。
 *  - 列表：注册表过滤出的当前用户可见菜单项；action 项点击直接执行并关闭，
 *    renderPanel 项点击进入子界面（如"设置"）。
 *  - 子界面：顶部"← 菜单"返回列表、X 关闭回主界面（ctx.back / ctx.close）。
 * 角色/环境过滤完全由注册表驱动，本组件不感知具体功能。
 */
export default function MenuPanel({ onClose }: { onClose: () => void }) {
  const user = useAuthStore((s) => s.user)
  const logout = useAuthStore((s) => s.logout)
  const navigate = useNavigate()
  const location = useLocation()
  const [activeKey, setActiveKey] = useState<string | null>(null)

  const items = filterMenuItems(user)
  const activeItem = items.find((i) => i.key === activeKey) ?? null

  const ctx: MenuCtx = {
    user,
    navigate,
    location,
    logout,
    back: () => setActiveKey(null),
    close: onClose,
  }

  // Esc 关闭
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  /** 执行 action 项：await 完成后统一关闭面板（导航/登出后回到主界面）。 */
  async function runAction(item: MenuItem) {
    const r = item.action?.(ctx)
    if (r && typeof (r as Promise<unknown>).then === 'function') await r
    onClose()
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="presentation">
      <div
        className="flex max-h-[85vh] w-full max-w-sm flex-col overflow-hidden rounded-xl border bg-background shadow-2xl"
        role="dialog"
        aria-modal="true"
        aria-label={activeItem ? `菜单 · ${activeItem.label}` : '菜单'}
      >
        {/* 头部 */}
        <div className="flex items-center justify-between border-b px-4 py-3">
          {activeItem ? (
            <>
              <button
                type="button"
                onClick={() => setActiveKey(null)}
                className="flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground"
              >
                <ChevronLeft className="size-4" /> 菜单
              </button>
              <span className="text-sm font-semibold">{activeItem.label}</span>
              <button type="button" aria-label="关闭菜单" onClick={onClose}>
                <X className="size-4 text-muted-foreground transition-colors hover:text-foreground" />
              </button>
            </>
          ) : (
            <>
              <span className="text-sm font-semibold">菜单</span>
              <button type="button" aria-label="关闭菜单" onClick={onClose}>
                <X className="size-4 text-muted-foreground transition-colors hover:text-foreground" />
              </button>
            </>
          )}
        </div>

        {/* 内容 */}
        <div className="flex-1 overflow-y-auto p-2">
          {activeItem ? (
            activeItem.renderPanel?.(ctx)
          ) : items.length === 0 ? (
            <div className="py-8 text-center text-sm text-muted-foreground">当前无可用的菜单功能</div>
          ) : (
            <ul className="space-y-0.5">
              {items.map((item) => {
                const Icon = item.icon
                return (
                  <li key={item.key}>
                    <button
                      type="button"
                      onClick={() => (item.renderPanel ? setActiveKey(item.key) : void runAction(item))}
                      className={cn(
                        'flex w-full items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-accent',
                        item.group === 'danger' && 'text-destructive hover:bg-destructive/10',
                      )}
                    >
                      {Icon && <Icon className="size-4 shrink-0" />}
                      <span className="min-w-0 flex-1">
                        <span className="block truncate">{item.label}</span>
                        {item.description && (
                          <span className="block truncate text-[11px] text-muted-foreground">{item.description}</span>
                        )}
                      </span>
                      {item.renderPanel && <ChevronRight className="size-4 shrink-0 text-muted-foreground" />}
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      </div>
    </div>
  )
}
