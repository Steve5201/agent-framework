import { useEffect, useState, type FormEvent } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router-dom'
import { ApiError, login, mergeGuestSessions, register } from '@/lib/api'
import { clearGuestId, getGuestId, hasGuestId } from '@/lib/guest'
import { DEFAULT_AGENT_ID } from '@/App'
import { isAdminRole, isAllAgentScope } from '@/lib/roles'
import { getServerUrl, setServerUrl as persistServerUrl } from '@/lib/settings'
import { clearRemembered, loadRemembered, saveRemembered } from '@/lib/remember'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

type Mode = 'login' | 'register'

/**
 * 门户登录页（阶段3·多租户门户化）：
 *  - 地址即门户：/login/:agentId（如 /login/tutor），无需再输入智能体 ID；
 *  - 普通门户支持注册（注册即该门户普通用户）；超管门户 /login/* 隐藏注册
 *    （超管账号仅由最高超管在管理端创建，'*' 门户后端亦拒绝注册）；
 *  - 管理员账号经本门户登录需归属该门户（后端校验，防跨域）。
 */
export default function LoginPage() {
  const navigate = useNavigate()
  const status = useAuthStore((s) => s.status)
  const user = useAuthStore((s) => s.user)
  const applySession = useAuthStore((s) => s.applySession)

  const { agentId } = useParams<{ agentId?: string }>()
  // 门户 ID 来自地址；无 URL 参数（如直达 /login）由 App 路由重定向到默认门户，
  // 此处再兜底一次，避免表单以空门户提交（Early Return 须置于所有 hooks 之后，
  // 保证 hooks 调用顺序稳定）。
  const effectiveAgentId = agentId ?? ''

  const [mode, setMode] = useState<Mode>('login')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // 记住密码（按门户域隔离）：勾选后保存当前域凭据，切换门户互不影响
  const [rememberMe, setRememberMe] = useState(false)

  // 挂载时回填当前门户域已保存的凭据 → 预填用户名/密码并勾选；
  // 无凭据则恢复上次勾选偏好。依赖 effectiveAgentId：切换门户时重新回填。
  useEffect(() => {
    let cancelled = false
    void (async () => {
      const creds = await loadRemembered(effectiveAgentId)
      if (cancelled) return
      if (creds) {
        setUsername(creds.username)
        setPassword(creds.password)
        setRememberMe(true)
        return
      }
      try {
        setRememberMe(localStorage.getItem('agent.remember_me') === '1')
      } catch {
        /* ignore */
      }
    })()
    return () => {
      cancelled = true
    }
  }, [effectiveAgentId])

  // 服务器地址设置（登录前可改，用于连接不同的部署机器）
  const [serverUrl, setServerUrl] = useState(getServerUrl())
  const [serverMsg, setServerMsg] = useState('')

  // 空门户兜底（所有 hooks 之后才能提前返回）
  if (!effectiveAgentId) {
    return <Navigate to={`/agent/${DEFAULT_AGENT_ID}`} replace />
  }

  // 仅普通门户允许注册；超管门户（*）隐藏注册入口（账号由最高超管创建）
  const isSuperPortal = isAllAgentScope(effectiveAgentId)
  const canRegister = !isSuperPortal
  const title = isSuperPortal ? '智能体助手 · 超管专属门户' : `智能体助手 · ${effectiveAgentId}`
  const subTitle =
    mode === 'register'
      ? '创建账号（注册即该门户普通用户）'
      : isSuperPortal
        ? '最高超管专属登录（账号由系统播种，无注册入口）'
        : `登录以继续对话（智能体 ${effectiveAgentId}）`

  function handleSaveServer() {
    try {
      persistServerUrl(serverUrl)
      setServerMsg('已保存，立即生效')
    } catch (err) {
      setServerMsg((err as Error).message)
    }
  }

  // 已登录直接进对话页：管理员（含 super_admin/agent_admin/admin）→ 管理端域；其它 → 对应智能体域
  if (status === 'authed') {
    const target = isAdminRole(user?.role)
      ? '/admin/chat'
      : `/agent/${effectiveAgentId || DEFAULT_AGENT_ID}`
    return <Navigate to={target} replace />
  }

  /**
   * 登录成功后：合并游客会话到账号（失败不阻断登录，仅记录并丢弃本地游客态），
   * 然后按角色跳转——管理员进管理端，普通用户回对应智能体门户。
   */
  async function mergeGuestAndLand(role: string | undefined) {
    if (hasGuestId()) {
      const guestId = getGuestId()
      if (guestId) {
        await mergeGuestSessions(guestId).catch((err) => {
          console.warn('[login] 游客会话合并失败，仅丢弃本地游客会话', err)
        })
      }
      clearGuestId()
    }
    const target = isAdminRole(role)
      ? '/admin/chat'
      : `/agent/${effectiveAgentId || DEFAULT_AGENT_ID}`
    navigate(target, { replace: true })
  }

  /** 记住密码（按当前门户域）：勾选 → 保存当前域凭据与偏好；取消 → 清除当前域凭据。 */
  function persistRemember(name: string, password: string) {
    if (rememberMe) {
      void saveRemembered(effectiveAgentId, name, password)
      try {
        localStorage.setItem('agent.remember_me', '1')
      } catch {
        /* ignore */
      }
    } else {
      void clearRemembered(effectiveAgentId)
      try {
        localStorage.removeItem('agent.remember_me')
      } catch {
        /* ignore */
      }
    }
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')

    // 表单校验（具体到字段，避免模糊提示）
    const name = username.trim()
    if (!name) return setError('请输入用户名')
    // 与后端 auth-service 校验规则一致：≥8 位且同时含字母与数字
    if (password.length < 8 || !/[a-zA-Z]/.test(password) || !/[0-9]/.test(password)) {
      return setError('密码至少 8 位，且须同时包含字母与数字')
    }
    if (mode === 'register' && password !== confirm) return setError('两次输入的密码不一致')

    setSubmitting(true)
    try {
      if (mode === 'login') {
        const resp = await login(name, password, effectiveAgentId)
        await applySession(resp.access_token, resp.refresh_token, resp.user)
        persistRemember(name, password)
        await mergeGuestAndLand(resp.user?.role)
      } else {
        await register(name, password, effectiveAgentId)
        // 注册成功 → 直接登录（体验顺滑，避免让用户再输一遍）
        const resp = await login(name, password, effectiveAgentId)
        await applySession(resp.access_token, resp.refresh_token, resp.user)
        persistRemember(name, password)
        await mergeGuestAndLand(resp.user?.role)
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : (err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    // overflow-y-auto + 卡片 my-auto：内容高于窗口时可滚动，底部设置区不会被裁掉
    // （桌面端窗口较矮时"服务器地址"设置区必须可见）。
    <div className="flex h-full justify-center overflow-y-auto p-4">
      <Card className="my-auto w-full max-w-sm">
        <CardHeader>
          <CardTitle className="text-lg">{title}</CardTitle>
          <CardDescription>{subTitle}</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4" noValidate>
            <div className="space-y-1.5">
              <Label htmlFor="username">用户名</Label>
              <Input
                id="username"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="请输入用户名"
                maxLength={64}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">密码</Label>
              <Input
                id="password"
                type="password"
                autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="至少 8 位，含字母和数字"
                maxLength={128}
              />
            </div>
            {mode === 'register' && (
              <div className="space-y-1.5">
                <Label htmlFor="confirm">确认密码</Label>
                <Input
                  id="confirm"
                  type="password"
                  autoComplete="new-password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  placeholder="再次输入密码"
                  maxLength={128}
                />
              </div>
            )}

            <label htmlFor="remember-me" className="flex items-start gap-2 text-sm">
              <input
                id="remember-me"
                type="checkbox"
                checked={rememberMe}
                onChange={(e) => setRememberMe(e.target.checked)}
                className="mt-0.5 h-4 w-4 accent-primary"
              />
              <span className="text-sm">
                记住密码
                <span className="block text-xs text-muted-foreground">
                  按智能体门户分别保存，切换门户无需重复输入
                </span>
              </span>
            </label>

            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}

            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting ? '请稍候…' : mode === 'login' ? '登录' : '注册并登录'}
            </Button>
          </form>

          {/* 返回游客模式：不登录直接回到智能体对话页（本地游客会话不丢失）。
              覆盖"从游客模式进入登录页后想返回"以及"登录页兜底"两种场景。
              超管专属门户（*）例外：游客态禁止对话（见 ChatPage），故隐藏该入口。 */}
          {!isSuperPortal && (
            <div className="mt-2 text-center">
              <button
                type="button"
                onClick={() =>
                  navigate(`/agent/${effectiveAgentId || DEFAULT_AGENT_ID}`, { replace: true })
                }
                className={cn(
                  'cursor-pointer text-xs text-muted-foreground underline-offset-4 hover:underline',
                )}
              >
                以游客身份继续（不登录）
              </button>
            </div>
          )}

          {canRegister && (
            <div className="mt-4 text-center text-sm">
              {mode === 'login' ? '还没有账号？' : '已有账号？'}{' '}
              <button
                type="button"
                onClick={() => {
                  setMode(mode === 'login' ? 'register' : 'login')
                  setError('')
                }}
                className={cn(
                  'cursor-pointer font-medium underline-offset-4 hover:underline',
                  'text-foreground',
                )}
              >
                {mode === 'login' ? '去注册' : '去登录'}
              </button>
            </div>
          )}

          {/* 服务器地址设置：默认本机，可改为任意部署机器地址（桌面端也在此设置） */}
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
