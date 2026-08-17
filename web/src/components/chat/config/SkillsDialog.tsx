import { useEffect, useRef, useState } from 'react'
import { Loader2, Wand2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig } from '@/types/api'
import { useChatStore } from '@/stores/chat'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import { useSessionResources } from './useSessionResources'
import Toggle from './Toggle'

/**
 * SkillsDialog 技能勾选弹窗（从原 SessionConfigDialog 拆出）。
 * 只展示"技能（skill）"分区（仅名称/说明，不含任何代码）；能力开关
 * 归属 CapabilitiesDialog。两者共用 enabled_resources，保存时经
 * buildSaved 合并另一侧，并保留思考模式配置。
 *
 * 管理端同步：可用技能列表 = 管理端启用且连接成功的技能（agent 热加载
 * 注册表）；useSessionResources 轮询刷新，弹窗打开期间管理端启停技能
 * 实时反映。真正决定对话中是否生效 = 本会话勾选（enabled_resources）。
 *
 * 修复历史 bug：初始化用 ref 守卫（仅首次），用户取消全部技能后不会
 * 被 effect 重新填回。
 */
interface Props {
  agentId?: string // 当前智能体域：切换智能体后技能列表跟随刷新
  sessionConfig?: SessionConfig
  onClose: () => void
}

export default function SkillsDialog({ agentId, sessionConfig, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)

  const { loading, error, skills, skillIds, initialSelected, buildSaved } =
    useSessionResources(agentId, sessionConfig)

  const [selected, setSelected] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  // 一次性初始化守卫：资源首载完成后仅填一次勾选，之后用户取消全部
  // 不再被 effect 覆盖（旧实现用 selected.length===0 判定，全取消会回填）。
  const initialized = useRef(false)
  useEffect(() => {
    if (!loading && !initialized.current) {
      initialized.current = true
      setSelected(initialSelected('skill'))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loading])

  const allSelected = skills.length > 0 && skills.every((s) => selected.includes(s.id))

  const toggle = (id: string) =>
    setSelected((prev) => (prev.includes(id) ? prev.filter((n) => n !== id) : [...prev, id]))

  const handleSave = async () => {
    if (!activeId) return
    setSaving(true)
    setSaveError('')
    try {
      // 只改本会话技能勾选；其余配置（能力/思考/知识库/MCP/兼容旧数据）经
      // mergeSessionConfig 全量保留（UpdateSessionConfig 为后端全量替换）。
      // buildSaved 返回 {resources, set}：全不选时 set=true（显式清空），
      // 全选/部分选择时 set=false（跟随默认/历史语义）。set 标记必须显式
      // 随每次保存下发，避免覆盖上一次"全不选"遗留的 set=true。
      const saved = buildSaved('skill', selected)
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
            <Wand2 className="h-4 w-4" /> 技能
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
              <h3 className="text-sm font-medium">技能（管理端启用后此处可勾选）</h3>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs"
                onClick={() => setSelected(allSelected ? [] : skillIds)}
              >
                {allSelected ? '全部关闭' : '全选'}
              </Button>
            </div>
            <ul className="max-h-72 space-y-1 overflow-y-auto">
              {skills.length === 0 && (
                <li className="py-1 text-xs text-muted-foreground">当前无可用技能</li>
              )}
              {skills.map((s) => (
                <li key={s.id} className="flex items-center justify-between rounded border px-3 py-2">
                  <div className="min-w-0">
                    <p className="text-sm font-medium">{s.name}</p>
                    <p className="truncate text-xs text-muted-foreground">{s.description}</p>
                  </div>
                  <Toggle
                    checked={selected.includes(s.id)}
                    onChange={() => toggle(s.id)}
                    aria-label={`启用技能 ${s.name}`}
                  />
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
