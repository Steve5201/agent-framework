import { useEffect, useState } from 'react'
import type { ResourceInfo, SessionConfig } from '@/types/api'
import { listResources } from '@/lib/api'
import { isTauri } from '@/lib/localTools'

/**
 * 能力 / 技能共用同一字段 enabled_resources 的选择逻辑。
 *
 * 语义：enabled_resources 是资源白名单，空/缺省 = 全部启用。
 * 能力弹窗与技能弹窗各自只编辑自己类型的勾选，保存时经 buildSaved
 * 合并另一侧（保持另一侧选择不变），避免两个弹窗互相覆盖。
 *
 * 实时同步：管理端（skill/MCP 管理界面）更新技能列表/启用状态后，
 * agent 热加载注册表 → 本 hook 每 3s 轮询 listResources 刷新可用项，
 * 弹窗打开期间即可看到管理端最新启用的技能（新建会话默认按会话勾选生效）。
 * 轮询只更新列表，不覆盖用户当前勾选。
 *
 * agentId：当前智能体域。切换智能体（多租户）后 agentId 变化 → effect 重跑，
 * 配置区立即刷新为目标域的资源清单（能力全局一致，技能/MCP 按域）。
 */
export function useSessionResources(agentId: string | undefined, sessionConfig?: SessionConfig) {
  const [resources, setResources] = useState<ResourceInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    let first = true
    const load = () => {
      // 仅首载显示"加载中"，后续轮询静默刷新（避免弹窗每 3s 闪一次转圈）
      if (first) {
        first = false
        setLoading(true)
      }
      void listResources(agentId)
        .then((rs) => {
          if (cancelled) return
          // 本地执行（local）能力仅桌面端支持：非 Tauri 环境从能力列表隐藏
          //（勾选无意义——浏览器收到 local_shell 调用会立即回填失败降级）。
          setResources(rs.filter((r) => r.type !== 'capability' || r.id !== 'local' || isTauri()))
          setError('') // 轮询成功后清除历史错误（值未变时 React 自动跳过）
        })
        .catch(() => {
          if (!cancelled) setError('资源列表加载失败')
        })
        .finally(() => {
          if (!cancelled) setLoading(false)
        })
    }
    load()
    // 弹窗打开期间轮询：管理端启停技能/新增技能实时反映到配置区
    const timer = window.setInterval(load, 3000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [agentId])

  const capabilities = resources.filter((r) => r.type === 'capability')
  const skills = resources.filter((r) => r.type === 'skill')
  const allIds = resources.map((r) => r.id)
  const capIds = capabilities.map((r) => r.id)
  const skillIds = skills.map((r) => r.id)

  // 资源选择语义（P3 反馈修复：支持"全选/全不选"显式语义）。
  //  - enabled_resources_set=true：资源选择被显式锁定——空数组 = 不启用任何资源；
  //  - set=false/缺省：空/缺省数组 = 全部启用（历史语义），非空 = 白名单。
  const explicitlySet = sessionConfig?.enabled_resources_set === true
  const stored = sessionConfig?.enabled_resources
  const storedSet = explicitlySet
    ? new Set(stored ?? [])
    : stored && stored.length > 0
      ? new Set(stored)
      : null
  const isEnabled = (id: string) => storedSet === null || storedSet.has(id)

  const capabilitiesAllEnabled = capIds.length > 0 && capIds.every(isEnabled)
  const skillsAllEnabled = skillIds.length > 0 && skillIds.every(isEnabled)

  /** 初始化某类型已选 id（无显式白名单 = 全选）。 */
  function initialSelected(kind: 'capability' | 'skill'): string[] {
    const ids = kind === 'capability' ? capIds : skillIds
    return ids.filter(isEnabled)
  }

  /** 构建保存用的资源选择（含 set 标记）。
   *   - kind 侧以调用方最新选择为准；另一侧沿用当前会话配置（isEnabled 判定）；
   *   - 全选 → resources=[] + set=false（跟随默认，避免白名单随资源集变动失准）；
   *   - 全不选 → resources=[] + set=true（显式清空，后端只保留基础对话）；
   *   - 部分选择 → resources=白名单 + set=false（历史语义不变）。
   */
  function buildSaved(kind: 'capability' | 'skill', selected: string[]): { resources: string[]; set: boolean } {
    const otherIds = kind === 'capability' ? skillIds : capIds
    const otherSelected = otherIds.filter(isEnabled)
    const list = [...selected, ...otherSelected]
    if (allIds.length > 0 && allIds.every((id) => list.includes(id))) {
      return { resources: [], set: false }
    }
    return { resources: list, set: list.length === 0 }
  }

  return {
    loading,
    error,
    capabilities,
    skills,
    allIds,
    capIds,
    skillIds,
    initialSelected,
    buildSaved,
    capabilitiesAllEnabled,
    skillsAllEnabled,
  }
}
