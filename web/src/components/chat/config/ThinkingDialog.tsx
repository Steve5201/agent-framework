import { useState } from 'react'
import { Sparkles, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig, ThinkingConfig } from '@/types/api'
import { useChatStore } from '@/stores/chat'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import Toggle from './Toggle'

/**
 * ThinkingDialog 思考模式弹窗（从原 SessionConfigDialog 拆出）。
 * 只编辑 thinking 配置；保存时保留当前 enabled_resources 不覆盖。
 */
interface Props {
  sessionConfig?: SessionConfig
  onClose: () => void
}

// 与后端 reasoning_effort 校验（low/high/max）及管理端默认配置页选项集对齐。
// 只保留三个强度值，去掉无意义的「默认」项：实例默认已改为 low（P3-A8），
// 用户选择必须是显式 low/high/max 之一，避免空串在保存/加载间来回跳变。
const EFFORTS: { value: string; label: string }[] = [
  { value: 'low', label: 'low（快速）' },
  { value: 'high', label: 'high（深入）' },
  { value: 'max', label: 'max（最强推理）' },
]

// defaultEffort 会话未显式配置强度时的实例默认值（与后端 applyThinkingConfig 一致）。
const defaultEffort = 'low'

export default function ThinkingDialog({ sessionConfig, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)

  const [thinking, setThinking] = useState<ThinkingConfig>(() => {
    const effort = sessionConfig?.thinking?.reasoning_effort
    return {
      // 先铺会话已有思考配置，再回填显式强度（无空值歧义）。
      // enabled 必须读取会话已存值：否则关闭思考后重开弹窗会被硬编码 true
      // 重新点亮（P3 反馈：思考模式开关无法正常更改）。
      ...(sessionConfig?.thinking ?? {}),
      enabled: sessionConfig?.thinking?.enabled ?? true,
      reasoning_effort: effort || defaultEffort,
    }
  })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const handleSave = async () => {
    if (!activeId) return
    setSaving(true)
    setSaveError('')
    try {
      // 只改思考配置；其余（能力/技能白名单/知识库/MCP/兼容旧数据）经
      // mergeSessionConfig 全量保留（UpdateSessionConfig 为后端全量替换）。
      // reasoning_effort 恒为显式 low/high/max（无空值歧义）。
      const config = mergeSessionConfig(sessionConfig, {
        thinking: {
          enabled: thinking.enabled,
          reasoning_effort: thinking.reasoning_effort || defaultEffort,
        },
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
            <Sparkles className="h-4 w-4" /> 思考模式
          </h2>
          <Button type="button" variant="ghost" size="icon" onClick={onClose} aria-label="关闭">
            <X />
          </Button>
        </div>

        <div className="rounded border p-3">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="text-sm font-medium">思考模式</h3>
              <p className="text-xs text-muted-foreground">关闭后模型直接回答，不产生思考过程（省 token）</p>
            </div>
            <Toggle checked={thinking.enabled} onChange={(v) => setThinking({ ...thinking, enabled: v })} />
          </div>
          {thinking.enabled && (
            <div className="mt-3 flex items-center gap-2">
              <label htmlFor="effort" className="text-xs text-muted-foreground">
                推理强度
              </label>
              <select
                id="effort"
                value={thinking.reasoning_effort}
                onChange={(e) => setThinking({ ...thinking, reasoning_effort: e.target.value })}
                className="h-8 flex-1 rounded border bg-background px-2 text-sm"
              >
                {EFFORTS.map((e) => (
                  <option key={e.value} value={e.value}>
                    {e.label}
                  </option>
                ))}
              </select>
            </div>
          )}
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
