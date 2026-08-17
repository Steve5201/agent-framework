import { useEffect, useRef, useState } from 'react'
import { Loader2, Cpu, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig } from '@/types/api'
import { useChatStore } from '@/stores/chat'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import { useSessionResources } from './useSessionResources'
import Toggle from './Toggle'

/**
 * CapabilitiesDialog 能力开关弹窗（从原 SessionConfigDialog 拆出）。
 *
 * 只展示"能力（capability）"分区的开关；技能归属 SkillsDialog。
 * 两者共用 enabled_resources 字段，保存时经 buildSaved 合并另一侧，
 * 并保留当前思考模式配置，避免互相覆盖。
 */
interface Props {
  agentId?: string // 当前智能体域：切换智能体后能力/技能列表跟随刷新
  sessionConfig?: SessionConfig
  onClose: () => void
}

export default function CapabilitiesDialog({ agentId, sessionConfig, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)

  const { loading, error, capabilities, capIds, initialSelected, buildSaved } =
    useSessionResources(agentId, sessionConfig)

  const [selected, setSelected] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  // 一次性初始化守卫（与 SkillsDialog 一致）：仅首载填一次，全取消不再回填。
  const initialized = useRef(false)
  useEffect(() => {
    if (!loading && !initialized.current) {
      initialized.current = true
      setSelected(initialSelected('capability'))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loading])

  const capsSelected = capabilities.length > 0 && capabilities.every((c) => selected.includes(c.id))

  const toggle = (id: string) =>
    setSelected((prev) => (prev.includes(id) ? prev.filter((n) => n !== id) : [...prev, id]))

  const handleSave = async () => {
    if (!activeId) return
    setSaving(true)
    setSaveError('')
    try {
      // 只改本会话能力勾选；其余配置（技能/思考/知识库/MCP/兼容旧数据）经
      // mergeSessionConfig 全量保留（UpdateSessionConfig 为后端全量替换）。
      // buildSaved 返回 {resources, set}：全不选时 set=true（显式清空），
      // 全选/部分选择时 set=false。set 标记必须显式随每次保存下发，
      // 避免覆盖上一次"全不选"遗留的 set=true。
      const saved = buildSaved('capability', selected)
      const config = mergeSessionConfig(sessionConfig, {
        enabled_resources: saved.resources,
        enabled_resources_set: saved.set,
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
            <Cpu className="h-4 w-4" /> 能力开关
          </h2>
          <Button type="button" variant="ghost" size="icon" onClick={onClose} aria-label="关闭">
            <X />
          </Button>
        </div>

        {loading ? (
          <div className="flex items-center gap-2 py-6 text-xs text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" /> 加载中…
          </div>
        ) : (
          <div className="mb-4">
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium">能力</h3>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs"
                onClick={() => setSelected(capsSelected ? [] : capIds)}
              >
                {capsSelected ? '全部关闭' : '全选'}
              </Button>
            </div>
            <ul className="space-y-1">
              {capabilities.length === 0 && (
                <li className="py-1 text-xs text-muted-foreground">当前无可用能力</li>
              )}
              {capabilities.map((c) => (
                <li key={c.id} className="flex items-center justify-between rounded border px-3 py-2">
                  <div className="min-w-0">
                    <p className="text-sm font-medium">{c.name}</p>
                    <p className="truncate text-xs text-muted-foreground">{c.description}</p>
                  </div>
                  <Toggle checked={selected.includes(c.id)} onChange={() => toggle(c.id)} />
                </li>
              ))}
            </ul>
          </div>
        )}

        {error && <p className="mb-3 text-xs text-destructive">{error}</p>}
        {saveError && <p className="mb-3 text-xs text-destructive">{saveError}</p>}

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button type="button" onClick={handleSave} disabled={saving || loading}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </div>
      </div>
    </div>
  )
}
