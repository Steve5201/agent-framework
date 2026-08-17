import { useChatStore } from '@/stores/chat'
import { Button } from '@/components/ui/button'

/**
 * 阶段3·本地工具确认弹窗。
 *
 * 桌面端（Tauri webview）收到 local_shell 等本地工具调用时弹出，
 * 展示将要在本机执行的命令，由用户决定"允许执行 / 拒绝"：
 *   - 允许：Rust 端本地执行 → 结果回填 agent-service 唤醒会话；
 *   - 拒绝：回填"用户拒绝"的失败结果，agent 据此调整策略。
 *
 * 浏览器环境不弹窗（无法执行本地命令，onToolCall 已立即回填降级结果）。
 */
export default function LocalToolModal() {
  const pending = useChatStore((s) => s.pendingLocalCall)
  const resolveLocalCall = useChatStore((s) => s.resolveLocalCall)

  if (!pending) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded-lg border bg-background p-5 shadow-lg">
        <h2 className="text-base font-semibold">允许在本地执行命令？</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          智能体请求在您的计算机上执行以下 shell 命令（工具：{pending.name}）：
        </p>
        <pre className="mt-3 max-h-40 overflow-auto break-all whitespace-pre-wrap rounded border bg-muted p-3 font-mono text-xs">
          {pending.command || '（空命令）'}
        </pre>
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="outline" onClick={() => void resolveLocalCall(false)}>
            拒绝
          </Button>
          <Button onClick={() => void resolveLocalCall(true)}>允许执行</Button>
        </div>
      </div>
    </div>
  )
}
