import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { getServerUrl, setServerUrl as persistServerUrl } from '@/lib/settings'
import { getPortalAgentId, setPortalAgentId } from '@/lib/portal'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { isAllAgentScope } from '@/lib/roles'
import { ArrowLeft } from 'lucide-react'

/** 门户配置页：桌面端首次运行落地页；侧栏常驻"门户配置"按钮可达。
 *  配置要连接的智能体门户（地址即门户：/agent/:agentId）。配置保存后进入该门户。 */
export default function PortalConfigPage() {
  const navigate = useNavigate()

  // 已配置门户可回显（超管可配置 '*' 专属门户，含义 = 全部智能体）
  const [agentId, setAgentId] = useState(getPortalAgentId())
  const [error, setError] = useState('')

  // 服务器地址：桌面端无地址栏，连接不同部署机器必须在此设置
  const [serverUrl, setServerUrl] = useState(getServerUrl())
  const [serverMsg, setServerMsg] = useState('')

  function handleSaveServer() {
    try {
      persistServerUrl(serverUrl)
      setServerMsg('已保存，立即生效')
    } catch (err) {
      setServerMsg((err as Error).message)
    }
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    const id = agentId.trim()
    // 与后端 authsvc 校验一致：普通门户限字母/数字/中划线；超管门户用 '*'（全门户标识）
    if (!id) return setError('请输入智能体 ID')
    if (!isAllAgentScope(id) && !/^[A-Za-z0-9-]{1,64}$/.test(id)) {
      return setError('非法的智能体 ID（仅限字母/数字/中划线，≤64 字符）')
    }
    const prev = getPortalAgentId()
    setPortalAgentId(id)
    // 未更换门户（与已配置一致）：仅确认进入，保留当前登录态；
    // 更换门户才退出账号（域间登录态隔离），新门户以游客身份进入。
    if (id !== prev) {
      await useAuthStore.getState().logout()
    }
    navigate(`/agent/${id}`, { replace: true })
  }

  return (
    <div className="flex h-full justify-center overflow-y-auto p-4">
      <Card className="my-auto w-full max-w-sm">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-lg">门户配置</CardTitle>
            {/* 返回：从聊天页侧栏进入后点错/不想改时回原页面。
                无历史（桌面端首次运行落地）时隐藏，避免空返回。 */}
            {window.history.length > 1 && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => navigate(-1)}
                aria-label="返回"
              >
                <ArrowLeft className="mr-1 h-4 w-4" /> 返回
              </Button>
            )}
          </div>
          <CardDescription>选择要连接的智能体门户（地址即门户，无需再输入智能体 ID）</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4" noValidate>
            <div className="space-y-1.5">
              <Label htmlFor="agent-id">智能体 ID</Label>
              <Input
                id="agent-id"
                value={agentId}
                onChange={(e) => {
                  setAgentId(e.target.value)
                  setError('')
                }}
                placeholder="如 tutor"
                maxLength={64}
              />
              <p className="text-xs text-muted-foreground">
                输入要进入的智能体门户（如 tutor）。最高超管可配置{' '}
                <code className="rounded bg-muted px-1">*</code>（全部智能体专属门户）
              </p>
            </div>

            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}

            <Button type="submit" className="w-full">
              进入门户
            </Button>
          </form>

          <div className="mt-5 space-y-2 border-t pt-4">
            <Label htmlFor="server-url">服务器地址</Label>
            <div className="flex gap-2">
              <Input
                id="server-url"
                value={serverUrl}
                onChange={(e) => {
                  setServerUrl(e.target.value)
                  setServerMsg('')
                }}
                placeholder="http://localhost:8080"
                maxLength={128}
              />
              <Button type="button" variant="outline" onClick={handleSaveServer}>
                保存
              </Button>
            </div>
            {serverMsg && <p className="text-xs text-muted-foreground">{serverMsg}</p>}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
