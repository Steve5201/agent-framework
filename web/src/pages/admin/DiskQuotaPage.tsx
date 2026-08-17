import { useCallback, useEffect, useState } from 'react'
import {
  adminClearDiskQuota,
  adminListDiskQuota,
  adminSetDiskQuota,
} from '@/lib/api'
import type { DiskQuota } from '@/types/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { AlertTriangle, HardDrive, Loader2, Plus, RefreshCw, RotateCcw } from 'lucide-react'
import { cn } from '@/lib/utils'

/** 简单弹窗（与 UsersPage 复用同款样式）。 */
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

/** 角色默认配额（与后端 RoleDiskQuotaDefaults 一致，部署方可用 AGENT_DISK_QUOTA_MB_* 覆盖）。 */
const ROLE_DEFAULTS = [
  { role: '普通用户', quota: '256 MB' },
  { role: '普通管理员', quota: '512 MB' },
  { role: '智能体超管', quota: '1 GB' },
  { role: '最高超管', quota: '不限' },
]

/** 配额展示文本（MB；0 = 不限） */
function quotaText(mb: number): string {
  if (mb <= 0) return '不限'
  if (mb >= 1024 && mb % 1024 === 0) return `${mb / 1024} GB`
  return `${mb} MB`
}

/**
 * 磁盘配额（super_admin 专属写模块）：管理用户工作区保护区（protected/）的
 * 磁盘配额上限。protected/ 是唯一不会被清理器自动删除的空间，显式覆盖记录
 * 存于 agent 库 sandbox_disk_quota；无记录的用户走角色默认。
 */
export default function DiskQuotaPage() {
  const [quotas, setQuotas] = useState<DiskQuota[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  // 设置弹窗状态
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<DiskQuota | null>(null) // null = 新增；对象 = 编辑
  const [userIdInput, setUserIdInput] = useState('')
  const [mbInput, setMbInput] = useState('')
  const [formError, setFormError] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    adminListDiskQuota()
      .then((list) => {
        setQuotas(list)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  function refresh() {
    setLoading(true)
    void load()
  }

  /** 打开设置弹窗：新增清空；编辑预填当前覆盖值 */
  function openForm(q: DiskQuota | null) {
    setFormOpen(true)
    setEditing(q)
    setUserIdInput(q ? String(q.user_id) : '')
    setMbInput(q ? String(q.disk_quota_mb) : '')
    setFormError('')
  }

  /** 关闭设置弹窗 */
  function closeForm() {
    setFormOpen(false)
    setEditing(null)
    setFormError('')
  }

  async function submit() {
    const uid = userIdInput.trim()
    const mb = mbInput.trim()
    if (!/^\d+$/.test(uid) || Number(uid) <= 0) {
      setFormError('user_id 须为正整数')
      return
    }
    if (!/^\d+$/.test(mb)) {
      setFormError('配额须为非负整数（MB，0 = 不限）')
      return
    }
    setBusy(true)
    setFormError('')
    try {
      await adminSetDiskQuota(uid, Number(mb))
      setNotice(`用户 #${uid} 的保护区磁盘配额已更新为 ${quotaText(Number(mb))}`)
      closeForm()
      void load()
    } catch (e) {
      setFormError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  /** 删除配额覆盖（恢复角色默认） */
  async function remove(q: DiskQuota) {
    if (!window.confirm(`确定删除用户 #${q.user_id} 的磁盘配额覆盖（恢复角色默认）？`)) return
    try {
      await adminClearDiskQuota(String(q.user_id))
      setNotice(`用户 #${q.user_id} 已恢复角色默认磁盘配额`)
      void load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div className="mx-auto max-w-5xl p-6">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="flex size-8 items-center justify-center rounded-lg bg-indigo-500/15 text-indigo-600 dark:text-indigo-300">
              <HardDrive className="size-4.5" />
            </div>
            <h1 className="text-lg font-semibold tracking-tight">磁盘配额</h1>
          </div>
          <p className="mt-1.5 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            管理用户工作区保护区（protected/）的磁盘配额上限。protected/ 是唯一不会被自动
            清理的空间，仅存用户明确保留 / 经确认的长期内容；临时产物由清理器 TTL 回收，不占配额。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={refresh} disabled={loading}>
            <RefreshCw className={cn('size-4', loading && 'animate-spin')} /> 刷新
          </Button>
          <Button onClick={() => openForm(null)}>
            <Plus className="size-4" /> 设置配额
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-3 flex items-center gap-1.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertTriangle className="size-4 shrink-0" /> <span>{error}</span>
        </div>
      )}
      {notice && (
        <div className="mb-3 flex items-center gap-1.5 rounded-md border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-600 dark:text-emerald-300">
          {notice}
        </div>
      )}

      {/* 角色默认配额说明 */}
      <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-4">
        {ROLE_DEFAULTS.map((r) => (
          <div key={r.role} className="rounded-lg border bg-card px-3 py-2.5">
            <div className="text-[11px] text-muted-foreground">{r.role}默认</div>
            <div className="mt-0.5 text-sm font-semibold tabular-nums">{r.quota}</div>
          </div>
        ))}
      </div>

      {/* 显式覆盖列表 */}
      <div className="overflow-hidden rounded-lg border bg-card">
        {loading ? (
          <div className="flex justify-center py-16">
            <Loader2 className="size-5 animate-spin text-muted-foreground" />
          </div>
        ) : quotas.length === 0 ? (
          <div className="py-16 text-center">
            <p className="text-sm text-muted-foreground">
              暂无显式配额记录，所有用户均按角色默认执行。
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">用户</th>
                <th className="px-4 py-2.5 font-medium">保护区配额</th>
                <th className="px-4 py-2.5 font-medium">更新时间</th>
                <th className="px-4 py-2.5 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {quotas.map((q) => (
                <tr key={q.user_id} className="border-b transition-colors last:border-0 hover:bg-accent/40">
                  <td className="px-4 py-2.5 font-mono">#{q.user_id}</td>
                  <td className="px-4 py-2.5">
                    <Badge variant="outline" className="font-mono text-[11px]">
                      {quotaText(q.disk_quota_mb)}
                    </Badge>
                  </td>
                  <td className="px-4 py-2.5 font-mono text-xs text-muted-foreground">
                    {new Date(q.updated_at).toLocaleString('zh-CN')}
                  </td>
                  <td className="px-4 py-2.5">
                    <div className="flex justify-end gap-1.5">
                      <Button variant="outline" size="sm" onClick={() => openForm(q)}>
                        修改
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => void remove(q)}>
                        <RotateCcw className="size-3.5" /> 恢复默认
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* 设置 / 修改配额弹窗 */}
      {formOpen && (
        <Modal
          title={editing ? `修改配额：用户 #${editing.user_id}` : '设置磁盘配额'}
          subtitle={
            editing
              ? '覆盖该用户的角色默认配额；0 = 不限'
              : '对指定用户设置保护区磁盘配额覆盖；0 = 不限'
          }
          footer={
            <>
              <Button variant="outline" onClick={closeForm} disabled={busy}>
                取消
              </Button>
              <Button onClick={() => void submit()} disabled={busy}>
                {busy ? <Loader2 className="size-4 animate-spin" /> : '保存配额'}
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            {formError && (
              <div className="flex items-center gap-1.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
                <AlertTriangle className="size-3.5 shrink-0" /> {formError}
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="dq-uid">user_id</Label>
              <Input
                id="dq-uid"
                inputMode="numeric"
                placeholder="用户 ID（正整数）"
                value={userIdInput}
                disabled={editing !== null}
                onChange={(e) => {
                  setUserIdInput(e.target.value)
                  setFormError('')
                }}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="dq-mb">配额（MB）</Label>
              <Input
                id="dq-mb"
                inputMode="numeric"
                placeholder="如 1024；0 = 不限"
                value={mbInput}
                onChange={(e) => {
                  setMbInput(e.target.value)
                  setFormError('')
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void submit()
                }}
              />
            </div>
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              仅约束保护区（protected/）写入；临时区与散落产物由清理器按 TTL 自动回收。
            </p>
          </div>
        </Modal>
      )}
    </div>
  )
}
