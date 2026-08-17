import { useCallback, useEffect, useState } from 'react'
import {
  adminClearUserQuota,
  adminCreateUser,
  adminDeleteUser,
  adminListQuota,
  adminListUsers,
  adminResetPassword,
  adminSetUserQuota,
} from '@/lib/api'
import type { AdminUser, UserQuota } from '@/types/api'
import { isSuperAdmin, ROLE_LABELS, getUserAgentId } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { KeyRound, Loader2, Plus, RefreshCw, RotateCcw, Search, Trash2, Users, Wallet } from 'lucide-react'
import { cn } from '@/lib/utils'

/** 简单弹窗（管理端复用，点击遮罩不关闭）。 */
function Modal({
  title,
  subtitle,
  children,
  footer,
}: {
  title: string
  subtitle?: string
  children: React.ReactNode
  footer: React.ReactNode
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div
        className="flex max-h-[85vh] w-full max-w-xl flex-col overflow-hidden rounded-xl border bg-background shadow-2xl"
        role="dialog"
        aria-modal="true"
      >
        <div className="border-b px-5 py-3.5">
          <div className="text-sm font-semibold">{title}</div>
          {subtitle && <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>}
        </div>
        <div className="flex-1 overflow-y-auto p-5">{children}</div>
        <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
          {footer}
        </div>
      </div>
    </div>
  )
}

const ROLE_STYLE: Record<string, string> = {
  super_admin: 'bg-amber-500/10 text-amber-600 dark:text-amber-300',
  agent_admin: 'bg-indigo-500/10 text-indigo-600 dark:text-indigo-300',
  admin: 'bg-sky-500/10 text-sky-600 dark:text-sky-300',
  user: 'bg-muted text-muted-foreground',
}

/** 配额单元格：展示当前有效配额与本月用量；无覆盖记录显示「默认」（跟随角色）。 */
function QuotaCell({
  quota,
  onEdit,
  onClear,
}: {
  quota?: UserQuota
  onEdit: () => void
  onClear: () => void
}) {
  if (!quota) {
    return (
      <button
        type="button"
        title="设置配额（0 = 不限）"
        onClick={onEdit}
        className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
      >
        <Wallet className="size-3.5" /> 默认
      </button>
    )
  }
  const used = Number(quota.used_this_month).toLocaleString('zh-CN')
  const quotaText =
    quota.token_quota_month <= 0
      ? '不限'
      : quota.token_quota_month.toLocaleString('zh-CN')
  return (
    <div className="flex items-center gap-1.5">
      <button
        type="button"
        title="修改配额（0 = 不限）"
        onClick={onEdit}
        className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
      >
        <Wallet className="size-3.5" />
        <span>
          已用 {used} / <span className="font-medium text-foreground">{quotaText}</span>
        </span>
      </button>
      <button
        type="button"
        title="恢复角色默认配额"
        onClick={onClear}
        className="rounded-md p-1 text-muted-foreground hover:bg-accent hover:text-destructive"
      >
        <RotateCcw className="size-3.5" />
      </button>
    </div>
  )
}

/** 角色等级（与后端 authsvc Role.Rank 一致）：user=0、admin=1、agent_admin=2、super_admin=3。
 *  重置密码/删除要求当前账号等级严格高于目标账号（平级一律拒绝，防横向越权）。 */
const ROLE_RANK: Record<string, number> = {
  user: 0,
  admin: 1,
  agent_admin: 2,
  super_admin: 3,
}

function rankOf(role?: string): number {
  return ROLE_RANK[role ?? ''] ?? 0
}

/** 密码强度校验（与后端一致）：≥8 位且同时包含字母与数字。返回错误文案；通过返回空串。 */
function passwordError(pw: string): string {
  if (pw.length < 8) return '密码须不少于 8 位'
  if (!/[A-Za-z]/.test(pw) || !/[0-9]/.test(pw)) return '密码须同时包含字母与数字'
  return ''
}

/**
 * 用户管理（super_admin / agent_admin）：
 *  - super_admin：全局范围，可创建任意角色（含 super_admin），可指定 agent_id；
 *  - agent_admin：仅本智能体组范围，可创建 user / admin（后端强制归属自身组）。
 *  - 普通 admin 无此模块（后端模块清单已按角色裁剪，路由守卫兜底）。
 */
export default function UsersPage() {
  const me = useAuthStore((s) => s.user)
  const superAdmin = isSuperAdmin(me?.role)
  const myAgent = getUserAgentId(me)
  const pageSize = 20

  const [users, setUsers] = useState<AdminUser[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [keyword, setKeyword] = useState('')
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  // 创建弹窗
  const [creating, setCreating] = useState(false)
  const [form, setForm] = useState({ username: '', password: '', role: 'user', agent_id: '' })
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')

  // 重置密码弹窗
  const [resetTarget, setResetTarget] = useState<AdminUser | null>(null)
  const [resetPass, setResetPass] = useState('')
  const [resetBusy, setResetBusy] = useState(false)
  const [resetError, setResetError] = useState('')

  // 用户 token 配额（仅 super_admin 可见/可操作；后端 requireSuperAdmin 兜底）
  const [quotas, setQuotas] = useState<Record<string, UserQuota>>({})
  const [quotaError, setQuotaError] = useState('')
  const [quotaTarget, setQuotaTarget] = useState<AdminUser | null>(null)
  const [quotaInput, setQuotaInput] = useState('')
  const [quotaBusy, setQuotaBusy] = useState(false)
  const [quotaFormError, setQuotaFormError] = useState('')

  const loadQuota = useCallback(() => {
    if (!superAdmin) return
    adminListQuota()
      .then((qs) => {
        const m: Record<string, UserQuota> = {}
        for (const q of qs) m[String(q.user_id)] = q
        setQuotas(m)
        setQuotaError('')
      })
      .catch((e) => setQuotaError((e as Error).message))
  }, [superAdmin])

  const load = useCallback(() => {
    adminListUsers({ page, page_size: pageSize, keyword: query })
      .then((resp) => {
        setUsers(resp.users)
        setTotal(resp.total)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
    if (superAdmin) void loadQuota()
  }, [page, query, superAdmin, loadQuota])

  useEffect(() => {
    void load()
  }, [load])

  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  function openCreate() {
    setForm({ username: '', password: '', role: 'user', agent_id: superAdmin ? '' : myAgent })
    setSaveError('')
    setCreating(true)
  }

  async function submit() {
    const username = form.username.trim()
    const password = form.password
    if (!username) {
      setSaveError('用户名不能为空')
      return
    }
    if (password.length < 8) {
      setSaveError('密码至少 8 位')
      return
    }
    if (!superAdmin && (form.role === 'super_admin' || form.role === 'agent_admin')) {
      setSaveError('智能体超管只能创建本智能体组的 user / admin')
      return
    }
    setBusy(true)
    setSaveError('')
    try {
      await adminCreateUser({
        username,
        password,
        role: form.role || undefined,
        // 非超管强制归属自身组，不传（后端锁定）；超管可选指定
        agent_id: superAdmin ? form.agent_id.trim() || undefined : undefined,
      })
      setCreating(false)
      setNotice(`用户「${username}」创建成功`)
      void load()
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  /** 打开重置密码弹窗（仅对等级严格低于自己的账号开放，防横向越权） */
  function openReset(u: AdminUser) {
    setResetTarget(u)
    setResetPass('')
    setResetError('')
  }

  async function resetSubmit() {
    if (!resetTarget) return
    const err = passwordError(resetPass)
    if (err) {
      setResetError(err)
      return
    }
    setResetBusy(true)
    setResetError('')
    try {
      await adminResetPassword(resetTarget.id, resetPass)
      setNotice(`用户「${resetTarget.username}」的密码已重置`)
      setResetTarget(null)
    } catch (e) {
      setResetError((e as Error).message)
    } finally {
      setResetBusy(false)
    }
  }

  /** 删除用户（后端禁止删自己/最后一名最高超管；等级校验同重置密码） */
  async function remove(u: AdminUser) {
    if (!window.confirm(`确定删除用户「${u.username}」（ID ${u.id}）？该操作不可恢复。`)) return
    try {
      await adminDeleteUser(u.id)
      setNotice(`用户「${u.username}」已删除`)
      void load()
    } catch (e) {
      alert(`删除失败：${(e as Error).message}`)
    }
  }

  /** 当前账号是否可管理目标账号：非自己且等级严格更高（与后端 CanManageUser 一致）。 */
  function canManage(u: AdminUser): boolean {
    if (me && u.id === me.id) return false
    return rankOf(me?.role) > rankOf(u.role)
  }

  /** 打开设置配额弹窗（预填当前覆盖值） */
  function openQuota(u: AdminUser) {
    const q = quotas[String(u.id)]
    setQuotaTarget(u)
    setQuotaInput(q ? String(q.token_quota_month) : '')
    setQuotaFormError('')
  }

  async function quotaSubmit() {
    if (!quotaTarget) return
    const v = quotaInput.trim()
    if (!/^\d+$/.test(v)) {
      setQuotaFormError('配额须为非负整数（0 = 不限）')
      return
    }
    setQuotaBusy(true)
    setQuotaFormError('')
    try {
      await adminSetUserQuota(quotaTarget.id, Number(v))
      setNotice(`用户「${quotaTarget.username}」的配额已更新`)
      setQuotaTarget(null)
      void loadQuota()
    } catch (e) {
      setQuotaFormError((e as Error).message)
    } finally {
      setQuotaBusy(false)
    }
  }

  /** 删除配额覆盖（恢复角色默认：管理员不限 / 普通用户 1000 万） */
  async function clearQuota(u: AdminUser) {
    if (
      !window.confirm(`确定将用户「${u.username}」的配额恢复为角色默认（管理员不限 / 普通用户 1000 万）？`)
    )
      return
    try {
      await adminClearUserQuota(u.id)
      setNotice(`用户「${u.username}」已恢复角色默认配额`)
      void loadQuota()
    } catch (e) {
      alert(`恢复默认失败：${(e as Error).message}`)
    }
  }

  return (
    <div className="mx-auto max-w-5xl p-6">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="flex size-8 items-center justify-center rounded-lg bg-indigo-500/15 text-indigo-600 dark:text-indigo-300">
              <Users className="size-4.5" />
            </div>
            <h1 className="text-lg font-semibold tracking-tight">用户管理</h1>
          </div>
          <p className="mt-1.5 max-w-xl text-xs leading-relaxed text-muted-foreground">
            {superAdmin
              ? '全局用户：可创建任意角色并指定智能体归属（含各智能体的超管 / 普通管理员 / 普通用户）。'
              : `本智能体「${myAgent}」的用户组：只能创建普通用户与普通管理员。`}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => { setLoading(true); void load() }} disabled={loading}>
            <RefreshCw className={cn('size-4', loading && 'animate-spin')} /> 刷新
          </Button>
          <Button onClick={openCreate}>
            <Plus className="size-4" /> 新建用户
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>
      )}
      {notice && (
        <div className="mb-3 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-sm text-primary">{notice}</div>
      )}
      {quotaError && (
        <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          配额数据加载失败（列表仍可用）：{quotaError}
        </div>
      )}

      {/* 搜索栏 */}
      <div className="mb-3 flex items-center gap-2">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="pl-8"
            placeholder="按用户名搜索"
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                setLoading(true)
                setPage(1)
                setQuery(keyword.trim())
              }
            }}
          />
        </div>
        <Button
          variant="outline"
          size="icon"
          title="搜索"
          onClick={() => {
            setLoading(true)
            setPage(1)
            setQuery(keyword.trim())
          }}
        >
          <Search className="size-3.5" />
        </Button>
      </div>

      {loading ? (
        <div className="flex justify-center py-16">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : users.length === 0 ? (
        <div className="rounded-xl border border-dashed bg-card/50 py-16 text-center">
          <p className="text-sm text-muted-foreground">暂无用户{query ? `（关键字「${query}」）` : ''}。</p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">用户名</th>
                <th className="px-3 py-2.5 font-medium">角色</th>
                <th className="px-3 py-2.5 font-medium">所属智能体</th>
                <th className="px-4 py-2.5 font-medium">用户 ID</th>
                {superAdmin && <th className="px-3 py-2.5 font-medium">本月 token / 配额</th>}
                <th className="px-4 py-2.5 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id} className="border-b transition-colors last:border-0 hover:bg-accent/40">
                  <td className="px-4 py-2.5 font-medium">{u.username}</td>
                  <td className="px-3 py-2.5">
                    <Badge
                      variant="outline"
                      className={cn('text-[10px]', ROLE_STYLE[u.role ?? 'user'] ?? ROLE_STYLE.user)}
                    >
                      {ROLE_LABELS[u.role ?? 'user'] ?? u.role}
                    </Badge>
                  </td>
                  <td className="px-3 py-2.5 font-mono text-xs text-muted-foreground">{getUserAgentId(u)}</td>
                  <td className="max-w-[180px] truncate px-4 py-2.5 font-mono text-xs text-muted-foreground" title={u.id}>
                    {u.id}
                  </td>
                  {superAdmin && (
                    <td className="px-3 py-2.5">
                      <QuotaCell quota={quotas[String(u.id)]} onEdit={() => openQuota(u)} onClear={() => void clearQuota(u)} />
                    </td>
                  )}
                  <td className="px-4 py-2.5 text-right">
                    {canManage(u) ? (
                      <div className="flex justify-end gap-0.5">
                        <Button
                          variant="ghost"
                          size="icon"
                          title="重置密码（要求当前账号权限高于该账号）"
                          onClick={() => openReset(u)}
                        >
                          <KeyRound className="size-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title="删除用户"
                          className="text-destructive"
                          onClick={() => void remove(u)}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    ) : (
                      <span className="text-[11px] text-muted-foreground/60" title="不能对自己或权限不低于自己的账号操作">
                        {me && u.id === me.id ? '当前账号' : '权限不足'}
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {/* 分页 */}
          <div className="flex items-center justify-between border-t px-4 py-2.5 text-xs text-muted-foreground">
            <span>
              共 {total} 人 · 第 {page}/{totalPages} 页
            </span>
            <div className="flex items-center gap-1">
              <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => { setLoading(true); setPage((p) => p - 1) }}>
                上一页
              </Button>
              <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => { setLoading(true); setPage((p) => p + 1) }}>
                下一页
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* 新建用户弹窗 */}
      {creating && (
        <Modal
          title="新建用户"
          subtitle={superAdmin ? '最高超管可创建任意角色，并指定智能体归属' : `智能体超管仅能创建本智能体组（${myAgent}）的 user / admin`}
          footer={
            <>
              <Button variant="outline" onClick={() => setCreating(false)} disabled={busy}>
                取消
              </Button>
              <Button onClick={() => void submit()} disabled={busy}>
                {busy ? <Loader2 className="size-4 animate-spin" /> : '创建'}
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            {saveError && (
              <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{saveError}</div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="u-name">用户名</Label>
              <Input
                id="u-name"
                value={form.username}
                maxLength={50}
                placeholder="如：alice"
                onChange={(e) => setForm((f) => ({ ...f, username: e.target.value }))}
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="u-pass">密码（≥6 位）</Label>
              <Input
                id="u-pass"
                type="password"
                value={form.password}
                placeholder="初始密码"
                onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="u-role">角色</Label>
              <select
                id="u-role"
                value={form.role}
                onChange={(e) => setForm((f) => ({ ...f, role: e.target.value }))}
                className="h-9 w-full rounded-md border bg-transparent px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <option value="user">普通用户</option>
                <option value="admin">普通管理员</option>
                {superAdmin && <option value="agent_admin">智能体超管</option>}
                {superAdmin && <option value="super_admin">最高超管</option>}
              </select>
            </div>
            {superAdmin && (
              <div className="space-y-1.5">
                <Label htmlFor="u-agent">智能体归属（可选，留空 = 不绑定）</Label>
                <Input
                  id="u-agent"
                  value={form.agent_id}
                  maxLength={64}
                  placeholder="如：math"
                  onChange={(e) => setForm((f) => ({ ...f, agent_id: e.target.value }))}
                />
              </div>
            )}
          </div>
        </Modal>
      )}

      {/* 重置密码弹窗 */}
      {resetTarget && (
        <Modal
          title={`重置密码：${resetTarget.username}`}
          subtitle="重置后该账号所有会话令牌将立即失效（强制重新登录）"
          footer={
            <>
              <Button variant="outline" onClick={() => setResetTarget(null)} disabled={resetBusy}>
                取消
              </Button>
              <Button onClick={() => void resetSubmit()} disabled={resetBusy}>
                {resetBusy ? <Loader2 className="size-4 animate-spin" /> : '重置密码'}
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            {resetError && (
              <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {resetError}
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="u-reset-pass">新密码（≥8 位，含字母与数字）</Label>
              <Input
                id="u-reset-pass"
                type="password"
                value={resetPass}
                autoFocus
                placeholder="输入新密码"
                onChange={(e) => {
                  setResetPass(e.target.value)
                  setResetError('')
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void resetSubmit()
                }}
              />
              <p className="text-xs text-muted-foreground">
                重置后目标账号的登录态全部失效，需用新密码重新登录。
              </p>
            </div>
          </div>
        </Modal>
      )}

      {/* 设置配额弹窗（仅超管；后端 requireSuperAdmin 兜底） */}
      {quotaTarget && (
        <Modal
          title={`设置配额：${quotaTarget.username}`}
          subtitle="0 = 不限；留空不填则保持原值。优先级：此值 > 角色默认"
          footer={
            <>
              <Button variant="outline" onClick={() => setQuotaTarget(null)} disabled={quotaBusy}>
                取消
              </Button>
              <Button onClick={() => void quotaSubmit()} disabled={quotaBusy}>
                {quotaBusy ? <Loader2 className="size-4 animate-spin" /> : '保存配额'}
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            {quotaFormError && (
              <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {quotaFormError}
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="u-quota">每月 token 配额（0 = 不限）</Label>
              <Input
                id="u-quota"
                inputMode="numeric"
                value={quotaInput}
                placeholder="如：10000000；0 表示不限"
                onChange={(e) => {
                  setQuotaInput(e.target.value)
                  setQuotaFormError('')
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void quotaSubmit()
                }}
                autoFocus
              />
              <p className="text-xs text-muted-foreground">
                当前角色默认：管理员不限，普通用户 1000 万 token/月。设置后立即生效，本月用量在
                llm-gateway 实时聚合。
              </p>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
