import { useState } from 'react'
import { Flame, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import Toggle from './config/Toggle'
import { isFreeMode, setFreeMode } from '@/lib/freeMode'
import { isTauri } from '@/lib/localTools'

/**
 * FreeModeToggle 自由模式开关（本地个人化）。
 *
 * 自由模式仅作用于"本机本地 shell"（local_shell，桌面端在本机直接执行命令）：
 * 开启后不再逐条弹确认、不再受默认 30s 超时限制。这是纯本地偏好，与服务器/
 * 角色无关，任何用户在自己的桌面端均可开启。
 *
 * 安全：因跳过确认 + 放开超时，命令会直接以当前用户权限在本机执行——每次
 * 开启（而非仅首次）都弹警告确认。仅 Tauri 桌面端渲染，浏览器不可见。
 */
export default function FreeModeToggle() {
  const [on, setOn] = useState(() => isFreeMode())
  const [warnOpen, setWarnOpen] = useState(false)

  if (!isTauri()) return null

  const handleChange = (next: boolean) => {
    if (next) {
      // 开启前弹警告确认（每次开启都弹，不记忆"已看过"）。
      setWarnOpen(true)
      return
    }
    setFreeMode(false)
    setOn(false)
  }

  return (
    <>
      <div className="flex shrink-0 items-center gap-1.5 rounded-full border bg-card px-2.5 py-1" title="自由模式：本地 shell 不询问、不限超时（本机个人化开关）">
        <Flame className="h-3.5 w-3.5 text-orange-500" aria-hidden />
        <span className="text-[11px] text-muted-foreground">自由模式</span>
        <Toggle checked={on} onChange={handleChange} aria-label="自由模式" />
      </div>

      {warnOpen && (
        <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
          <div className="w-full max-w-md rounded-lg border bg-background p-5 shadow-lg">
            <div className="mb-4 flex items-center justify-between">
              <h2 className="flex items-center gap-2 text-base font-semibold text-orange-600">
                <Flame className="h-4 w-4" /> 开启自由模式？
              </h2>
              <Button type="button" variant="ghost" size="icon" onClick={() => setWarnOpen(false)} aria-label="关闭">
                <X />
              </Button>
            </div>
            <div className="space-y-2 text-sm text-muted-foreground">
              <p>
                自由模式开启后，智能体请求在你的<strong>本机执行本地 shell 命令</strong>时：
              </p>
              <ul className="list-inside list-disc space-y-1 text-[13px]">
                <li>不再逐条弹出确认框，直接以当前用户权限执行；</li>
                <li>不再受默认 30 秒超时限制，命令可持续运行直至完成。</li>
              </ul>
              <p className="text-[13px]">
                该开关<strong>仅对本机生效</strong>，是个人化偏好；不影响服务器沙盒、其他用户或其他设备。
              </p>
              <p className="rounded border border-destructive/40 bg-destructive/5 p-2 text-[13px] text-destructive">
                风险提示：命令将直接在本机执行且无确认，请确保你信任当前会话的智能体。
              </p>
            </div>
            <div className="mt-4 flex justify-end gap-2">
              <Button type="button" variant="secondary" onClick={() => setWarnOpen(false)}>
                取消
              </Button>
              <Button
                type="button"
                onClick={() => {
                  setFreeMode(true)
                  setOn(true)
                  setWarnOpen(false)
                }}
              >
                确认开启
              </Button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}