import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { useChatStore } from '@/stores/chat'
import { useAuthStore } from '@/stores/auth'
import { configRegistry } from './registry'
import type { ConfigCtx } from './types'

/**
 * ConfigButtonArea 输入区标准配置按钮区。
 *
 * - 渲染 configRegistry 中 visible(ctx) 为 true 的按钮（每个按钮开自己的配置弹窗）；
 * - DialogHost 负责弹窗开关：key + nonce 双保险实现"每次打开都重挂载"，
 *   使弹窗内部状态（勾选/开关）每次打开都以会话最新配置为初始值；
 * - 新配置项只需在 registry.tsx 注册，本组件骨架不再改动。
 */
export default function ConfigButtonArea() {
  const activeId = useChatStore((s) => s.activeId)
  const sessions = useChatStore((s) => s.sessions)
  const createSession = useChatStore((s) => s.createSession)
  const user = useAuthStore((s) => s.user)

  const [openKey, setOpenKey] = useState<string | null>(null)
  const [nonce, setNonce] = useState(0)

  const activeSession = sessions.find((s) => s.id === activeId) ?? null
  const ctx: ConfigCtx = { user, activeSession }
  const items = configRegistry.filter((i) => i.visible(ctx))
  const openItem = configRegistry.find((i) => i.key === openKey) ?? null

  return (
    <>
      <div className="flex shrink-0 items-center gap-0.5">
        {items.map((item) => (
          <Button
            key={item.key}
            type="button"
            variant="ghost"
            size="icon"
            title={item.label}
            aria-label={item.label}
            onClick={() => {
              void (async () => {
                // 会话依赖型配置项：无活动会话时先自动新建会话，再弹配置窗。
                // 保证用户"直接输入开新聊"（sendMessage 内部会建会话）前，
                // 也有权配置能力/思考/技能等对话选项。
                if (item.requiresSession && !activeId) {
                  try {
                    await createSession()
                  } catch (e) {
                    alert(`创建会话失败，无法配置：${(e as Error).message}`)
                    return
                  }
                }
                setOpenKey(item.key)
                setNonce((n) => n + 1)
              })()
            }}
            className="h-8 w-8 text-muted-foreground"
          >
            {item.icon}
          </Button>
        ))}
      </div>

      {/* DialogHost：按当前打开的 key 渲染对应弹窗 */}
      {openItem && (
        <div key={`${openItem.key}-${nonce}`}>{openItem.renderDialog(ctx, () => setOpenKey(null))}</div>
      )}
    </>
  )
}
