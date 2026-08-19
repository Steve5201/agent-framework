import { useState } from 'react'
import { Shield, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig } from '@/types/api'
import { useChatStore } from '@/stores/chat'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import { isAdminRole } from '@/lib/roles'
import Toggle from './Toggle'

/**
 * SandboxDialog 沙盒配置弹窗（管理员级：仅 agent_admin / super_admin / admin 可见可改）。
 * 会话级沙盒配置：联网开关 + 资源限制覆盖。普通用户配置区不展示（registry visible 判定）。
 * 后端 UpdateSessionConfig 对非管理员角色强制覆盖回快照原值（双保险）。
 */
interface Props {
  sessionConfig?: SessionConfig
  role?: string
  onClose: () => void
}

const defaultValues = {
  network: false,
  memory: 0,
  cpu: 0,
  nofile: 0,
  timeout: 0,
}

export default function SandboxDialog({ sessionConfig, role, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)

  const [form, setForm] = useState(() => ({
    network: sessionConfig?.sandbox_network_enabled ?? defaultValues.network,
    memory: sessionConfig?.sandbox_memory_mb ?? defaultValues.memory,
    cpu: sessionConfig?.sandbox_cpu_seconds ?? defaultValues.cpu,
    nofile: sessionConfig?.sandbox_nofile_limit ?? defaultValues.nofile,
    timeout: sessionConfig?.sandbox_max_timeout ?? defaultValues.timeout,
  }))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  if (!isAdminRole(role)) {
    return null
  }

  const handleSave = async () => {
    if (!activeId) return
    setSaving(true)
    setSaveError('')
    try {
      const config = mergeSessionConfig(sessionConfig, {
        sandbox_network_enabled: form.network,
        sandbox_memory_mb: form.memory || undefined,
        sandbox_cpu_seconds: form.cpu || undefined,
        sandbox_nofile_limit: form.nofile || undefined,
        sandbox_max_timeout: form.timeout || undefined,
      })
      await updateConfig(activeId, config)
      onClose()
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-md rounded-lg border bg-background p-5 shadow-lg">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="flex items-center gap-2 text-base font-semibold">
            <Shield className="h-4 w-4" /> 沙盒配置
          </h2>
          <Button type="button" variant="ghost" size="icon" onClick={onClose} aria-label="关闭">
            <X />
          </Button>
        </div>

        <div className="space-y-3">
          <div className="rounded border p-3">
            <div className="flex items-center justify-between">
              <div>
                <h3 className="text-sm font-medium">沙盒联网</h3>
                <p className="text-xs text-muted-foreground">
                  开启后沙盒内工具（如 fetch_url_render 渲染 JS 动态页）可访问外网；默认禁网
                </p>
              </div>
              <Toggle
                checked={form.network}
                onChange={(v) => setForm({ ...form, network: v })}
              />
            </div>
            {form.network && (
              <p className="mt-2 text-xs text-destructive">
                警告：联网会放宽沙盒网络隔离，仅建议在可信环境对可信会话开启。
              </p>
            )}
          </div>

          <div className="rounded border p-3">
            <h3 className="text-sm font-medium">资源限制覆盖（0 = 跟随服务默认）</h3>
            <div className="mt-2 space-y-2">
              {(
                [
                  ['memory', '内存上限（MB）', 'sandbox_memory_mb'],
                  ['cpu', 'CPU 时间（秒）', 'sandbox_cpu_seconds'],
                  ['nofile', '最大打开文件数', 'sandbox_nofile_limit'],
                  ['timeout', '单次执行最大超时（秒）', 'sandbox_max_timeout'],
                ] as const
              ).map(([key, label]) => (
                <label key={key} className="flex items-center justify-between gap-2">
                  <span className="text-xs text-muted-foreground">{label}</span>
                  <input
                    type="number"
                    min={0}
                    value={form[key]}
                    onChange={(e) =>
                      setForm({ ...form, [key]: Math.max(0, Number(e.target.value) || 0) })
                    }
                    className="h-8 w-28 rounded border bg-background px-2 text-sm"
                  />
                </label>
              ))}
            </div>
          </div>
        </div>

        {saveError && <p className="mb-3 mt-3 text-xs text-destructive">{saveError}</p>}

        <div className="mt-4 flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button type="button" onClick={handleSave} disabled={saving}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </div>
      </div>
    </div>
  )
}