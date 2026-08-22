import { useEffect, useState } from 'react'
import { MoreHorizontal } from 'lucide-react'
import MenuPanel from './Panel'
import { cn } from '@/lib/utils'
import { registerBackHandler, unregisterBackHandler } from '@/lib/backstack'

/**
 * 菜单触发按钮：点击打开菜单面板（Panel）。
 *  - 独立三角图标按钮：hover 背景+颜色加深（变色明显）；
 *  - 仅三角按钮可交互（周边元素如用户区 Avatar/用户名纯展示，不响应点击）。
 * 游客不渲染菜单面板（filterMenuItems 仅登录用户可见功能），由调用方决定是否渲染本按钮。
 */
export default function MenuButton({ className }: { className?: string }) {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (!open) return
    registerBackHandler(() => setOpen(false))
    return () => unregisterBackHandler()
  }, [open])
  return (
    <>
      <button
        type="button"
        aria-label="菜单"
        onClick={() => setOpen(true)}
        className={cn('flex items-center rounded-lg', className)}
      >
        <span className="flex size-10 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground md:size-8">
          <MoreHorizontal className="size-6 md:size-4" />
        </span>
      </button>
      {open && <MenuPanel onClose={() => setOpen(false)} />}
    </>
  )
}
