import { useState } from 'react'
import { KeyRound, Save } from 'lucide-react'
import { changePassword } from '@/lib/api'
import { getServerUrl, setServerUrl } from '@/lib/settings'
import { isTauri } from '@/lib/storage'
import { getPortalAgentId } from '@/lib/portal'
import { DEFAULT_AGENT_ID, ROLE_LABELS } from '@/lib/roles'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'
import type { MenuCtx } from '../types'

/** 设置子界面：当前账号 / 修改密码 / 服务器地址 / 门户配置（桌面端）。
 *  演示菜单"打开子界面 → 设置完返回主界面"的标准能力（renderPanel 模式）。 */
export default function SettingsPanel({ ctx }: { ctx: MenuCtx }) {
  const [url, setUrl] = useState(getServerUrl())
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [pwMsg, setPwMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [pwBusy, setPwBusy] = useState(false)

  function save() {
    try {
      setServerUrl(url)
      setMsg({ ok: true, text: '已保存，立即生效（无需刷新）' })
    } catch (e) {
      setMsg({ ok: false, text: (e as Error).message })
    }
  }

  async function submitPassword() {
    setPwMsg(null)
    if (!oldPw || !newPw) {
      setPwMsg({ ok: false, text: '请填写原密码与新密码' })
      return
    }
    if (newPw !== confirmPw) {
      setPwMsg({ ok: false, text: '两次输入的新密码不一致' })
      return
    }
    if (newPw.length < 8 || !/[A-Za-z]/.test(newPw) || !/\d/.test(newPw)) {
      setPwMsg({ ok: false, text: '新密码须不少于 8 位，且同时包含字母与数字' })
      return
    }
    setPwBusy(true)
    try {
      await changePassword(oldPw, newPw)
      setPwMsg({ ok: true, text: '修改成功，请重新登录' })
      setOldPw('')
      setNewPw('')
      setConfirmPw('')
    } catch (e) {
      setPwMsg({ ok: false, text: (e as Error).message })
    } finally {
      setPwBusy(false)
    }
  }

  return (
    <div className="space-y-4 p-1">
      {/* 当前账号 */}
      <div className="rounded-lg border bg-muted/30 p-3">
        <div className="text-[11px] text-muted-foreground">当前账号</div>
        <div className="mt-0.5 flex items-center gap-2 text-sm">
          <span className="font-medium">{ctx.user?.username}</span>
          <span className="text-xs text-muted-foreground">
            {ctx.user?.role ? (ROLE_LABELS[ctx.user.role] ?? ctx.user.role) : '-'}
          </span>
        </div>
      </div>

      {/* 修改密码 */}
      <div className="space-y-1.5">
        <Label className="text-xs text-muted-foreground">修改密码</Label>
        <div className="space-y-1.5 rounded-lg border p-3">
          <Input
            className="h-8 text-xs"
            type="password"
            placeholder="原密码"
            value={oldPw}
            onChange={(e) => setOldPw(e.target.value)}
            autoComplete="current-password"
          />
          <Input
            className="h-8 text-xs"
            type="password"
            placeholder="新密码（≥8 位，含字母与数字）"
            value={newPw}
            onChange={(e) => setNewPw(e.target.value)}
            autoComplete="new-password"
          />
          <Input
            className="h-8 text-xs"
            type="password"
            placeholder="确认新密码"
            value={confirmPw}
            onChange={(e) => setConfirmPw(e.target.value)}
            autoComplete="new-password"
          />
          <Button size="sm" variant="outline" className="w-full" onClick={submitPassword} disabled={pwBusy}>
            <KeyRound className="size-3.5" /> {pwBusy ? '提交中…' : '确认修改'}
          </Button>
          {pwMsg && (
            <p className={cn('text-xs', pwMsg.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-destructive')}>
              {pwMsg.text}
            </p>
          )}
        </div>
      </div>

      {/* 服务器地址 */}
      <div className="space-y-1.5">
        <Label className="text-xs text-muted-foreground">服务器地址</Label>
        <div className="flex gap-2">
          <Input
            className="h-8 font-mono text-xs"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="http://localhost:8080"
          />
          <Button size="sm" onClick={save}>
            <Save className="size-3.5" /> 保存
          </Button>
        </div>
        {msg && (
          <p className={cn('text-xs', msg.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-destructive')}>
            {msg.text}
          </p>
        )}
      </div>

      {/* 门户配置（桌面端无地址栏，需在此切换要连接的智能体门户） */}
      {isTauri() && (
        <div className="space-y-1.5">
          <Label className="text-xs text-muted-foreground">门户配置</Label>
          <div className="flex items-center justify-between rounded-lg border p-3">
            <div className="min-w-0">
              <div className="truncate text-sm">当前门户：{getPortalAgentId() || DEFAULT_AGENT_ID}</div>
              <div className="text-[11px] text-muted-foreground">切换要连接的智能体门户</div>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                ctx.close()
                ctx.navigate('/portal')
              }}
            >
              前往配置
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
