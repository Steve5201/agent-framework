import { useEffect, useRef, useState } from 'react'
import { Database, Info, Loader2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig } from '@/types/api'
import { listKbs } from '@/lib/api'
import { useChatStore } from '@/stores/chat'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import Toggle from './Toggle'

/**
 * KBDialog 知识库勾选弹窗（对话配置区"知识库"，P3-A6 反转语义）。
 *
 * 语义（与后端装配一致）：
 *   - 勾选 = 本会话启用知识库检索（kb_search 装配），且限定在所选知识库内；
 *   - 不勾选 = 本会话不使用知识库检索（kb_search 不装配，模型不可调用）。
 * 要"检索全部知识库"就把全部勾上。
 *
 * 管理端同步：可用知识库列表轮询刷新（/v1/agent/kbs，域由后端锁定），
 * 弹窗打开期间管理端新建/停用知识库实时反映；勾选只在首载初始化一次。
 */
interface Props {
  /** 会话配置（读取现有 kb_ids 初始化勾选） */
  sessionConfig?: SessionConfig
  /** 会话所属智能体域；空 = 后端默认域 */
  agentId?: string
  onClose: () => void
}

export default function KBDialog({ sessionConfig, agentId, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)

  const [bases, setBases] = useState<Array<{ id: string; name: string; description: string; doc_count: number }>>(
    [],
  )
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  // 弹窗打开时快照会话的初始 kb_ids（一次性初始化用）。
  // 用 ref 避免 effect 依赖 sessionConfig：其后续变化不应重置用户勾选。
  const initialKbIdsRef = useRef(sessionConfig?.kb_ids)

  // 首载：拉取列表 + 一次性初始化勾选（会话已配置的 kb_ids ∩ 当前域列表）
  useEffect(() => {
    let cancelled = false
    void (async () => {
      // 异步路径内进入加载态：避免 effect 同步 setState 级联渲染
      setLoading(true)
      setError('')
      try {
        const list = await listKbs(agentId)
        if (cancelled) return
        setBases(list)
        setSelected((initialKbIdsRef.current ?? []).filter((id) => list.some((b) => b.id === id)))
      } catch (e) {
        if (!cancelled) setError((e as Error).message)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [agentId])

  // 轮询：管理端新建/停用知识库实时反映到配置区（不覆盖用户当前勾选）
  useEffect(() => {
    let cancelled = false
    const timer = window.setInterval(() => {
      listKbs(agentId)
        .then((list) => {
          if (!cancelled) {
            setBases(list)
            setError('')
          }
        })
        .catch(() => {})
    }, 3000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [agentId])

  const allSelected = bases.length > 0 && bases.every((b) => selected.includes(b.id))

  const toggle = (id: string) =>
    setSelected((prev) => (prev.includes(id) ? prev.filter((n) => n !== id) : [...prev, id]))

  const handleSave = async () => {
    if (!activeId) return
    setSaving(true)
    setSaveError('')
    try {
      // 只改 kb_ids，其余配置经 mergeSessionConfig 全量保留（全量替换需带回）。
      // kb_ids_set=true：显式锁定知识库选择（含清空=不使用知识库），不再跟随默认。
      const config = mergeSessionConfig(sessionConfig, { kb_ids: selected, kb_ids_set: true })
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
            <Database className="h-4 w-4" /> 知识库
          </h2>
          <Button type="button" variant="ghost" size="icon" onClick={onClose} aria-label="关闭">
            <X />
          </Button>
        </div>

        <p className="mb-3 flex items-start gap-1.5 text-xs text-muted-foreground">
          <Info className="mt-0.5 h-3 w-3 shrink-0" />
          勾选 = 本会话启用知识库检索（kb_search），限定在所选知识库内；不勾选 = 本会话不使用知识库检索。要检索全部知识库，全选即可。
        </p>

        {loading ? (
          <div className="flex items-center gap-2 py-6 text-xs text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" /> 加载中…
          </div>
        ) : (
          <div className="mb-4">
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium">知识库</h3>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs"
                onClick={() => setSelected(allSelected ? [] : bases.map((b) => b.id))}
              >
                {allSelected ? '全部取消' : '全选'}
              </Button>
            </div>
            <ul className="max-h-72 space-y-1 overflow-y-auto">
              {bases.length === 0 && (
                <li className="py-1 text-xs text-muted-foreground">当前智能体域暂无可选知识库</li>
              )}
              {bases.map((b) => (
                <li key={b.id} className="flex items-center justify-between rounded border px-3 py-2">
                  <div className="min-w-0">
                    <p className="flex items-center gap-2 text-sm font-medium">
                      {b.name}
                      <span className="shrink-0 text-xs text-muted-foreground">{b.doc_count} 文档</span>
                    </p>
                    {b.description && (
                      <p className="truncate text-xs text-muted-foreground">{b.description}</p>
                    )}
                  </div>
                  <Toggle
                    checked={selected.includes(b.id)}
                    onChange={() => toggle(b.id)}
                    aria-label={`使用知识库 ${b.name}`}
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
          <Button type="button" onClick={handleSave} disabled={saving || loading || !!error}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </div>
      </div>
    </div>
  )
}
