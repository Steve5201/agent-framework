import { useState } from 'react'
import { Flame, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import Toggle from './Toggle'
import { isFreeMode, setFreeMode } from '@/lib/freeMode'

/**
 * FreeModeDialog 自由模式配置弹窗（纯本地个人化开关）。
 *
 * 自由模式仅作用于"本机本地 shell"（local_shell，桌面端在本机直接执行命令）：
 * 开启后不再逐条弹确认、不再受默认 30s 超时限制。这是纯本地偏好，与服务器/
 * 角色无关，任何用户在自己的桌面端均可开启。
 *
 * 安全：因跳过确认 + 放开超时，命令会直接以当前用户权限在本机执行——每次
 * 开启（而非仅首次）都在弹窗内展示风险提示并要求二次确认。
 * 作为配置按钮区的一项注册在 registry.tsx（仅 Tauri 桌面端 visible）。
 */
export default function FreeModeDialog({ onClose }: { onClose: () => void }) {
  const [on, setOn] = useState(() => isFreeMode())
  // 开启确认态：true 时显示风险提示与"确认开启"按钮，用户确认后才真正开启。
  const [confirming, setConfirming] = useState(false)

  const handleToggle = (next: boolean) => {
    if (next) {
      setConfirming(true)
      return
    }
    setFreeMode(false)
    setOn(false)
    setConfirming(false)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-md rounded-lg border bg-background p-5 shadow-lg">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="flex items-center gap-2 text-base font-semibold">
            <Flame className="h-4 w-4 text-orange-500" /> 自由模式
          </h2>
          <Button type="button" variant="ghost" size="icon" onClick={onClose} aria-label="关闭">
            <X />
          </Button>
        </div>

        <div className="space-y-3">
          <div className="rounded border p-3">
            <div className="flex items-center justify-between">
              <div>
                <h3 className="text-sm font-medium">自由模式</h3>
                <p className="text-xs text-muted-foreground">
                  本地 shell 不再逐条弹确认、不再受默认 30s 超时限制（本机个人化开关）
                </p>
              </div>
              <Toggle checked={on} onChange={handleToggle} aria-label="自由模式" />
            </div>
          </div>

          <p className="text-[13px] leading-relaxed text-muted-foreground">
            自由模式开启后，智能体请求在你的<strong>本机执行本地 shell 命令</strong>时：不再逐条弹出确认框，
            直接以当前用户权限执行；不再受默认 30 秒超时限制，命令可持续运行直至完成。
            该开关<strong>仅对本机生效</strong>，不影响服务器沙盒、其他用户或其他设备。
          </p>

          {confirming && (
            <div className="rounded border border-destructive/40 bg-destructive/5 p-3">
              <p className="text-[13px] text-destructive">
                风险提示：命令将直接在本机执行且无确认，请确保你信任当前会话的智能体。
              </p>
              <div className="mt-2 flex justify-end gap-2">
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  onClick={() => setConfirming(false)}
                >
                  取消
                </Button>
                <Button
                  type="button"
                  size="sm"
                  onClick={() => {
                    setFreeMode(true)
                    setOn(true)
                    setConfirming(false)
                  }}
                >
                  确认开启
                </Button>
              </div>
            </div>
          )}
        </div>

        <div className="mt-4 flex justify-end">
          <Button type="button" variant="secondary" onClick={onClose}>
            关闭
          </Button>
        </div>
      </div>
    </div>
  )
}