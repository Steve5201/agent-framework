import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  adminBindAgentOwner,
  adminDeleteAgent,
  adminGetAgent,
  adminGetAgentDefaults,
  adminGetAgentUsage,
  adminListKbs,
  adminListMcpServers,
  adminPutAgentDefaults,
  adminSetAgentStatus,
  adminUpdateAgent,
  listPublicModels,
  listResources,
} from '@/lib/api'
import type { Agent, AgentDefaults, AgentUsage, KnowledgeBase, McpServer, Model, ResourceInfo } from '@/types/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import { Loader2, ArrowLeft, Pencil, Play, Square, Trash2, CircleDollarSign, Hash, Coins, Clock, RefreshCw, Boxes, Gauge, Info, Settings2, CalendarDays, User, Wrench, Server, BookOpen, Check, CheckCircle2, AlertCircle, Cpu, Star, ChevronDown, Network, UserRound } from 'lucide-react'
import SkillsPage from '@/pages/admin/SkillsPage'
import McpPage from '@/pages/admin/McpPage'
import KnowledgeBasePage from '@/pages/admin/KnowledgeBasePage'
import { isTauri } from '@/lib/localTools'

/** 状态徽标样式（与后端 status：1=启用 0=停用） */
const STATUS_STYLE: Record<number, { label: string; cls: string }> = {
  1: { label: '启用', cls: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-300' },
  0: { label: '停用', cls: 'bg-muted text-muted-foreground' },
}

const DEFAULTS_REASONING_OPTIONS = [
  { value: 'low', label: 'low（快速）' },
  { value: 'high', label: 'high（深入）' },
  { value: 'max', label: 'max（最强推理）' },
]

// 实例默认推理强度：代码级默认改为 low（与后端 applyThinkingConfig 保持一致）。
const defaultEffort = 'low'

type TabKey = 'info' | 'resources' | 'defaults' | 'usage'
const TABS: { key: TabKey; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { key: 'info', label: '信息', icon: Info },
  { key: 'resources', label: '资源', icon: Boxes },
  { key: 'defaults', label: '默认配置', icon: Settings2 },
  { key: 'usage', label: '用量', icon: Gauge },
]

/** 逗号分隔文本 → 去空白/去重的数组（默认配置的文本型字段用）。 */
function splitList(text: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of text.split(',')) {
    const v = raw.trim()
    if (v && !seen.has(v)) {
      seen.add(v)
      out.push(v)
    }
  }
  return out
}

/** 多选组：options 显示 label，value 集合由外部持有。
 *  全选/清空只作用于本列表的项（能力/技能共用一个集合时互不覆盖）。
 *  "显式全不选"由调用方用 ExplicitNoneToggle 在列表下方统一提供——
 *  空集合 + set=true 才能表达"新会话默认不启用任何项"（presence 标记），
 *  与"不设默认（沿用实例全局行为）"区分。 */
function MultiSelect({
  label,
  options,
  selected,
  onChange,
  placeholder,
}: {
  label: string
  options: { value: string; label: string }[]
  selected: Set<string>
  onChange: (next: Set<string>) => void
  placeholder: string
}) {
  const values = new Set(options.map((o) => o.value))
  // 全选/清空只增删本列表的项，避免影响共享集合里其它列表的勾选。
  const selectAll = () => onChange(new Set([...selected, ...values]))
  const clearList = () => onChange(new Set([...selected].filter((x) => !values.has(x))))
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <Label>{label}</Label>
        {options.length > 0 && (
          <div className="flex shrink-0 items-center gap-2 text-[11px]">
            <button type="button" className="text-primary hover:underline" onClick={selectAll}>
              全选
            </button>
            <span className="text-muted-foreground/50">·</span>
            <button type="button" className="text-primary hover:underline" onClick={clearList}>
              清空
            </button>
          </div>
        )}
      </div>
      {options.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">{placeholder}</div>
      ) : (
        <div className="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto rounded-md border p-2">
          {options.map((o) => {
            const checked = selected.has(o.value)
            return (
              <button
                key={o.value}
                type="button"
                aria-pressed={checked}
                onClick={() => {
                  const next = new Set(selected)
                  if (checked) next.delete(o.value)
                  else next.add(o.value)
                  onChange(next)
                }}
                className={cn(
                  'inline-flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium transition-all',
                  checked
                    ? 'bg-primary text-primary-foreground shadow-sm'
                    : 'border border-input bg-background text-muted-foreground hover:border-primary/60 hover:text-foreground',
                )}
              >
                {checked && <Check className="size-3" />}
                {o.label}
              </button>
            )
          })}
        </div>
      )}
      <p className="text-[11px] text-muted-foreground">空 = 不设默认（沿用实例全局行为）</p>
    </div>
  )
}

/** 显式全不选开关：勾选 = 清空该类别选中项 + 类别 set=true（presence 标记，
 *  新会话默认不启用任何该类别的项），与"不设默认（沿用实例全局行为）"区分。
 *  disabled（可选）仅用于知识库/MCP 等"先清空再勾选"的旧交互；能力/技能开关
 *  勾选即清空对应列表（无需先手动清空），避免"勾了但保存时被选中项覆盖"的歧义。 */
function ExplicitNoneToggle({
  label,
  hint,
  checked,
  disabled,
  onChange,
}: {
  label: string
  hint: string
  checked: boolean
  disabled?: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label
      className={cn(
        'flex cursor-pointer items-start gap-2 rounded-md border border-dashed px-3 py-2',
        disabled && 'cursor-not-allowed opacity-50',
      )}
    >
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-0.5 size-3.5 accent-primary"
      />
      <span className="min-w-0">
        <span className="text-xs font-medium">显式全不选：{label}</span>
        <span className="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">{hint}</span>
      </span>
    </label>
  )
}

/** 智能体详情页：信息 / 资源（技能·MCP·知识库）/ 默认配置 / 用量。 */
export default function AgentDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()

  const [agent, setAgent] = useState<Agent | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [tab, setTab] = useState<TabKey>('info')
  const [busy, setBusy] = useState(false) // 页头操作（启停/删除）

  // 信息编辑表单（模型/推理强度不在基本信息页维护——大模型走默认配置页占位，
  // 等大模型管理模块接入后再启用；见 defaults Tab「默认大模型」区）。
  const [editing, setEditing] = useState(false)
  const [form, setForm] = useState({ description: '', avatar: '', welcome: '', system_prompt: '' })
  const [saveError, setSaveError] = useState('')

  // 绑定/更换/解绑超管弹窗（仅最高超管）
  const [ownerOpen, setOwnerOpen] = useState(false)
  const [ownerId, setOwnerId] = useState('')
  const [ownerBusy, setOwnerBusy] = useState(false)
  const [ownerError, setOwnerError] = useState('')

  // 默认配置
  const [defaultsDirty, setDefaultsDirty] = useState(false)
  const [defaultsMsg, setDefaultsMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const [resources, setResources] = useState<ResourceInfo[]>([])
  const [kbList, setKbList] = useState<KnowledgeBase[]>([])
  const [mcpList, setMcpList] = useState<McpServer[]>([])
  const [selResources, setSelResources] = useState<Set<string>>(new Set())
  const [selKbs, setSelKbs] = useState<Set<string>>(new Set())
  const [selMcp, setSelMcp] = useState<Set<string>>(new Set())
  const [thinkingEnabled, setThinkingEnabled] = useState(false)
  const [reasoningEffort, setReasoningEffort] = useState('')
  const [toolsText, setToolsText] = useState('')
  // 运行模式默认（P4-F）：single（单智能体）↔ orchestrate（多智能体编排）
  const [mode, setMode] = useState<'single' | 'orchestrate'>('single')
  // 编排方案默认（P4-J）：fixed（固定教研流水线）↔ dynamic（LLM 动态分解）
  const [plan, setPlan] = useState<'fixed' | 'dynamic'>('fixed')
  // 运行模式 combobox（对齐配置区 ModeDialog 交互）
  const [modeOpen, setModeOpen] = useState(false)
  const modeRef = useRef<HTMLDivElement>(null)
  // 显式全不选（presence 标记）：空数组 + set=true = 新会话默认不启用任何项，
  // 与"不设默认（沿用实例全局行为）"区分。能力与技能是独立配置类别，
  // 各自持有标记（enabled_capabilities_set / enabled_skills_set）。
  const [capExplicit, setCapExplicit] = useState(false)
  const [skillExplicit, setSkillExplicit] = useState(false)
  const [selKbsExplicit, setSelKbsExplicit] = useState(false)
  const [selMcpExplicit, setSelMcpExplicit] = useState(false)
  // 管理员级运行限制（快照固化到新会话，普通用户配置区不可见/不可改）：
  // 工具调用轮次 / 短期记忆窗口 / 思考（工具调用）轮次。空字符串 = 不设置（0）。
  const [adminRounds, setAdminRounds] = useState('')
  const [adminMessages, setAdminMessages] = useState('')
  const [adminThinkingRounds, setAdminThinkingRounds] = useState('')
  // 默认大模型（公开 /v1/models 列表 = 实际可选的启用模型）
  const [modelList, setModelList] = useState<Model[]>([])
  // 当前选中；modelTouched 区分"未动过"（保留原默认配置）与"主动改选"
  const [selModel, setSelModel] = useState('')
  const [origModel, setOrigModel] = useState('')
  const [modelTouched, setModelTouched] = useState(false)
  // 默认大模型 combobox（对齐配置区 LLMDialog 交互）
  const [modelOpen, setModelOpen] = useState(false)
  const modelRef = useRef<HTMLDivElement>(null)

  // 用量
  const [usage, setUsage] = useState<AgentUsage | null>(null)
  const [usageDays, setUsageDays] = useState(7)
  const [usageLoading, setUsageLoading] = useState(false)

  const loadAgent = useCallback(() => {
    setLoading(true)
    adminGetAgent(id)
      .then((a) => {
        setAgent(a)
        setError('')
        setForm({
          description: a.description ?? '',
          avatar: a.avatar ?? '',
          welcome: a.welcome ?? '',
          system_prompt: a.system_prompt ?? '',
        })
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [id])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 进入页面时加载详情
    void loadAgent()
  }, [loadAgent])

  // 默认配置 Tab：加载 defaults + 三个域的候选列表
  const loadDefaults = useCallback(() => {
    if (id === '') return
    adminGetAgentDefaults(id)
      .then((d) => {
        setSelResources(new Set(d.enabled_resources ?? []))
        setSelKbs(new Set(d.kb_ids ?? []))
        setSelMcp(new Set(d.mcp_servers ?? []))
        // 能力/技能独立全不选（P3 反馈）：各自恢复 presence 标记。
        // 旧数据兼容：只有旧的 enabled_resources_set=true 且数组为空（无新标记）
        // → 视为旧式"全不选"，迁移为新式（能力+技能都显式全不选）。
        const legacyNone =
          d.enabled_resources_set === true &&
          (d.enabled_resources ?? []).length === 0 &&
          !d.enabled_capabilities_set &&
          !d.enabled_skills_set
        setCapExplicit(d.enabled_capabilities_set === true || legacyNone)
        setSkillExplicit(d.enabled_skills_set === true || legacyNone)
        // 显式空选：仅当 set 标记存在且列表为空时才算（空 + 未标记 = 不设默认）。
        setSelKbsExplicit(d.kb_ids_set === true && (d.kb_ids ?? []).length === 0)
        setSelMcpExplicit(d.mcp_servers_set === true && (d.mcp_servers ?? []).length === 0)
        setThinkingEnabled(d.thinking?.enabled ?? false)
        // 强度三值制（P3-A8）：无显式值回填实例默认 low，选项集不再含空项。
        setReasoningEffort(d.thinking?.reasoning_effort || defaultEffort)
        setToolsText((d.enabled_tools ?? []).join(', '))
        // 运行模式默认：single / orchestrate（空 = single）。
        setMode(d.mode ?? 'single')
        // 编排方案默认：fixed / dynamic（空 = fixed）。
        setPlan(d.orchestrate_plan ?? 'fixed')
        // 管理员级运行限制：0/缺省 = 不设置（空输入框）。
        setAdminRounds(d.max_rounds ? String(d.max_rounds) : '')
        setAdminMessages(d.max_messages ? String(d.max_messages) : '')
        setAdminThinkingRounds(d.max_thinking_rounds ? String(d.max_thinking_rounds) : '')
        // 默认大模型：快照已选的值保留为原值（保存时未改动则原样写回）；
        // 下拉无"不设置"选项——选项即实际可选的启用模型，未设置时展示系统默认。
        const curModel = d.model ?? ''
        setOrigModel(curModel)
        setModelTouched(false)
        setSelModel(curModel)
        listPublicModels().then((list) => {
          setModelList(list)
          if (curModel && list.some((m) => m.name === curModel)) {
            setSelModel(curModel)
          } else {
            // 快照值已删除/禁用，或本就未设置：展示系统默认模型（空 = 暂无可用模型）。
            setSelModel(list.find((m) => m.is_default)?.name ?? '')
          }
        }).catch(() => {})
      })
      .catch(() => {
        /* 默认配置读取失败不阻塞 Tab 展示 */
      })
    // 候选列表只取"可用"资源（P3 反馈：管理端停用的技能/MCP/知识库 = 不可用，
    // 不出现在默认配置页可选范围）；并裁剪已选集合里已停用/不存在的项，避免
    // 保存时校验失败或默认值指向不可用资源。
    Promise.allSettled([listResources(id), adminListKbs(id), adminListMcpServers(id)]).then(([r, k, m]) => {
      if (r.status === 'fulfilled') {
        // 本地执行（local）能力仅桌面端支持：非 Tauri 环境从候选列表隐藏
        //（浏览器勾选无意义——收到 local_shell 调用会立即回填失败降级）。
        const cap = r.value.filter((x) => x.type !== 'capability' || x.id !== 'local' || isTauri())
        setResources(cap)
        const ids = new Set(cap.map((x) => x.id))
        setSelResources((prev) => new Set([...prev].filter((x) => ids.has(x))))
      }
      if (k.status === 'fulfilled') {
        const enabled = k.value.filter((x) => x.enabled !== false)
        setKbList(enabled)
        const ids = new Set(enabled.map((x) => x.id))
        setSelKbs((prev) => new Set([...prev].filter((x) => ids.has(x))))
      }
      if (m.status === 'fulfilled') {
        const enabled = m.value.filter((x) => x.enabled !== false)
        setMcpList(enabled)
        const ids = new Set(enabled.map((x) => x.name))
        setSelMcp((prev) => new Set([...prev].filter((x) => ids.has(x))))
      }
    })
  }, [id])

  useEffect(() => {
    if (tab === 'defaults') void loadDefaults()
  }, [tab, loadDefaults])

  // 默认大模型 combobox：点击外部关闭下拉
  useEffect(() => {
    if (!modelOpen) return
    const onDocClick = (e: MouseEvent) => {
      if (modelRef.current && !modelRef.current.contains(e.target as Node)) setModelOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [modelOpen])

  // 运行模式 combobox：点击外部关闭下拉
  useEffect(() => {
    if (!modeOpen) return
    const onDocClick = (e: MouseEvent) => {
      if (modeRef.current && !modeRef.current.contains(e.target as Node)) setModeOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [modeOpen])

  // 用量 Tab
  const loadUsage = useCallback(() => {
    if (id === '') return
    setUsageLoading(true)
    adminGetAgentUsage(id, usageDays)
      .then((u) => {
        setUsage(u)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setUsageLoading(false))
  }, [id, usageDays])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 切到用量 Tab 时拉取
    if (tab === 'usage') void loadUsage()
  }, [tab, loadUsage])

  // 页头「刷新」：重拉智能体信息 + 当前 Tab 数据（defaults 覆盖本地 / usage 重拉）。
  // 修复：此前刷新仅 loadAgent，停留在默认配置 Tab 时点刷新不会重新拉取
  // 管理端设置的默认配置，导致"怎么改保存、UI 都一个样"。
  const refreshAll = useCallback(() => {
    void loadAgent()
    if (tab === 'defaults') void loadDefaults()
    if (tab === 'usage') void loadUsage()
  }, [loadAgent, loadDefaults, loadUsage, tab])

  function markDefaultsDirty() {
    setDefaultsDirty(true)
  }

  /** 默认配置保存成功提示：3.2s 后自动消失（仅清除同文案的那条）。 */
  const flashDefaultsMsg = useCallback((text: string) => {
    setDefaultsMsg({ type: 'success', text })
    window.setTimeout(() => {
      setDefaultsMsg((m) => (m && m.text === text ? null : m))
    }, 3200)
  }, [])

  /** 能力/技能选择变更：只改本类别在共享联合数组（selResources）中的项，
   *  其它类别勾选不受影响；有选中项 = 该类别不再处于"显式全不选"。 */
  function mergeCategory(category: 'capability' | 'skill', next: Set<string>) {
    setSelResources((prev) => {
      const nextSet = new Set(prev)
      for (const r of category === 'capability' ? capList : skillList) nextSet.delete(r.id)
      for (const x of next) nextSet.add(x)
      return nextSet
    })
    if (next.size > 0) {
      if (category === 'capability') setCapExplicit(false)
      else setSkillExplicit(false)
    }
    markDefaultsDirty()
  }

  /** 能力"显式全不选"开关：勾选 = 清空该类别所有选中项 + 类别标记置 true
   *  （空能力白名单随快照固化，新会话不启用任何能力）。 */
  function toggleCapNone(v: boolean) {
    setCapExplicit(v)
    if (v) {
      setSelResources((prev) => new Set([...prev].filter((x) => !capList.some((r) => r.id === x))))
    }
    markDefaultsDirty()
  }

  /** 技能"显式全不选"开关（语义同 toggleCapNone）。 */
  function toggleSkillNone(v: boolean) {
    setSkillExplicit(v)
    if (v) {
      setSelResources((prev) => new Set([...prev].filter((x) => !skillList.some((r) => r.id === x))))
    }
    markDefaultsDirty()
  }

/** 运行限制数值校验：空 = 不设置；非法范围返回错误文案，合法返回 null。 */
function adminRangeError(rounds: string, messages: string, thinking: string): string | null {
  const n = (v: string) => (v.trim() === '' ? 0 : Number(v.trim()))
  const r = n(rounds)
  const m = n(messages)
  const t = n(thinking)
  if (r !== 0 && (r < 1 || r > 100)) return '工具调用轮次需在 1-100 之间（0 = 不设置）'
  if (m !== 0 && m < 2) return '对话历史窗口最小为 2 条消息（0 = 不设置）'
  if (t !== 0 && (t < 1 || t > 100)) return '思考轮次上限需在 1-100 之间（0 = 不设置）'
  return null
}

  async function saveDefaults() {
    if (id === '') return
    const rangeErr = adminRangeError(adminRounds, adminMessages, adminThinkingRounds)
    if (rangeErr) {
      setDefaultsMsg({ type: 'error', text: rangeErr })
      return
    }
    setBusy(true)
    setDefaultsMsg(null)
    // 三态域（资源/知识库/MCP）：有选中 → 白名单 + set=true（显式锁定）；
    // 空 + 显式全不选 → [] + set=true（全不选）；空 + 未显式 → 不下发（不设默认）。
    // 能力与技能是独立配置类别（P3 反馈）：某类别有选中项或勾选了全不选 →
    // 该类别 set=true（白名单 = enabled_resources 中该类别的项）；两个类别
    // 都未设置 → 不下发（跟随实例全量）。
    const payload: AgentDefaults = {
      thinking: { enabled: thinkingEnabled, reasoning_effort: reasoningEffort || defaultEffort },
      enabled_tools: splitList(toolsText),
    }
    const capSelected = selCaps.size > 0
    const skillSelected = selSkills.size > 0
    const capSet = capExplicit || capSelected
    const skillSet = skillExplicit || skillSelected
    if (capSet || skillSet) {
      payload.enabled_resources = [...selResources]
      payload.enabled_resources_set = true
      if (capSet) payload.enabled_capabilities_set = true
      if (skillSet) payload.enabled_skills_set = true
    }
    if (selKbs.size > 0) {
      payload.kb_ids = [...selKbs]
      payload.kb_ids_set = true
    } else if (selKbsExplicit) {
      payload.kb_ids = []
      payload.kb_ids_set = true
    }
    if (selMcp.size > 0) {
      payload.mcp_servers = [...selMcp]
      payload.mcp_servers_set = true
    } else if (selMcpExplicit) {
      payload.mcp_servers = []
      payload.mcp_servers_set = true
    }
    // 管理员级运行限制：0 = 不下发（装配时回退服务实例默认）。
    const rounds = adminRounds.trim() === '' ? 0 : Number(adminRounds.trim())
    const messages = adminMessages.trim() === '' ? 0 : Number(adminMessages.trim())
    const thinking = adminThinkingRounds.trim() === '' ? 0 : Number(adminThinkingRounds.trim())
    if (rounds) payload.max_rounds = rounds
    if (messages) payload.max_messages = messages
    if (thinking) payload.max_thinking_rounds = thinking
    // 默认大模型：主动改选才写入选中的模型——选中系统默认 = 不锁定
    //（跟随系统默认，与配置区 LLMDialog 语义一致）；未改动保留原值
    //（快照有值则原样写回，避免"无关保存"清掉已有配置）。
    const defaultModel = modelList.find((m) => m.is_default)?.name
    if (modelTouched) {
      if (selModel && selModel !== defaultModel) payload.model = selModel
    } else if (origModel) {
      payload.model = origModel
    }
    // 运行模式默认：显式下发 single / orchestrate（空 = single）。
    payload.mode = mode
    // 编排方案默认：仅 orchestrate 模式下有意义，显式下发 fixed / dynamic。
    if (mode === 'orchestrate') payload.orchestrate_plan = plan
    try {
      await adminPutAgentDefaults(id, payload)
      setDefaultsDirty(false)
      flashDefaultsMsg('默认配置已保存，仅新会话生效')
    } catch (e) {
      setDefaultsMsg({ type: 'error', text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  async function clearDefaults() {
    if (id === '') return
    setBusy(true)
    setDefaultsMsg(null)
    try {
      await adminPutAgentDefaults(id, {})
      setSelResources(new Set())
      setSelKbs(new Set())
      setSelMcp(new Set())
      setCapExplicit(false)
      setSkillExplicit(false)
      setSelKbsExplicit(false)
      setSelMcpExplicit(false)
      setThinkingEnabled(false)
      setReasoningEffort(defaultEffort)
      setToolsText('')
      setMode('single')
      setPlan('fixed')
      setAdminRounds('')
      setAdminMessages('')
      setAdminThinkingRounds('')
      // 清除默认模型配置：恢复"跟随系统默认"，下拉回显系统默认模型。
      setOrigModel('')
      setModelTouched(false)
      setSelModel(modelList.find((m) => m.is_default)?.name ?? '')
      setDefaultsDirty(false)
      flashDefaultsMsg('默认配置已清空（新会话沿用实例默认）')
    } catch (e) {
      setDefaultsMsg({ type: 'error', text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  async function saveInfo() {
    if (!agent || id === '') return
    if (!form.avatar || form.avatar.trim() === '') {
      // avatar 留空 = 清空（后端契约：空串清空，首字兜底），无需前端强制
    }
    setBusy(true)
    setSaveError('')
    try {
      const updated = await adminUpdateAgent(id, {
        name: agent.name, // name 必填非空，PATCH 需回传
        description: form.description.trim(),
        avatar: form.avatar.trim(),
        welcome: form.welcome.trim(),
        system_prompt: form.system_prompt.trim(),
      })
      setAgent(updated)
      setEditing(false)
      setNotice('智能体信息已更新')
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  async function toggleStatus() {
    if (!agent || id === '') return
    const next: 0 | 1 = agent.status === 1 ? 0 : 1
    setBusy(true)
    try {
      const updated = await adminSetAgentStatus(id, next)
      setAgent(updated)
      setNotice(next === 1 ? '智能体已启用' : '智能体已停用（该域暂停创建新会话）')
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  async function removeAgent() {
    if (!agent || id === '') return
    if (!window.confirm(`确认软删除智能体「${agent.name}」（${id}）？删除后该域停止服务，历史会话保留。`)) return
    setBusy(true)
    try {
      await adminDeleteAgent(id)
      navigate('/admin/agents')
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  function openOwnerDialog() {
    setOwnerId(agent?.owner_user_id ?? '')
    setOwnerError('')
    setOwnerOpen(true)
  }

  /** 绑定/更换超管（空串 = 解绑）。 */
  async function submitOwner() {
    if (!agent || id === '') return
    const target = ownerId.trim()
    setOwnerBusy(true)
    setOwnerError('')
    try {
      const updated = await adminBindAgentOwner(id, target)
      setAgent(updated)
      setOwnerOpen(false)
      setNotice(target ? `已绑定用户 ${target} 为「${agent.name}」超管` : `已解绑「${agent.name}」超管`)
    } catch (e) {
      setOwnerError((e as Error).message)
    } finally {
      setOwnerBusy(false)
    }
  }

  if (loading) {
    return (
      <div className="flex justify-center py-20">
        <Loader2 className="size-5 animate-spin text-muted-foreground" />
      </div>
    )
  }
  if (!agent) {
    return (
      <div className="mx-auto max-w-5xl p-6">
        <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error || '智能体不存在'}</div>
        <Button variant="outline" className="mt-4" onClick={() => navigate('/admin/agents')}>
          <ArrowLeft className="size-4" /> 返回列表
        </Button>
      </div>
    )
  }

  const st = STATUS_STYLE[agent.status] ?? { label: '未知', cls: 'bg-muted text-muted-foreground' }
  const isTutor = agent.id === 'tutor'
  // 默认大模型 combobox 当前选中项（列表为空/未选中 = null）
  const selectedModelObj = modelList.find((m) => m.name === selModel) ?? null

  // 能力/技能是独立配置类别（P3 反馈）：UI 选择集从共享联合数组 selResources
  // （enabled_resources = 能力 id ∪ 技能名）派生，避免两套 state 失同步。
  const capList = resources.filter((r) => r.type === 'capability')
  const skillList = resources.filter((r) => r.type === 'skill')
  const selCaps = new Set([...selResources].filter((x) => capList.some((r) => r.id === x)))
  const selSkills = new Set([...selResources].filter((x) => skillList.some((r) => r.id === x)))
  // 全不选开关的实际勾选态：类别标记为 true 且该类别无选中项。若类别有选中项
  // （标记通常已随选择被清除），即便残留标记也展示为未勾选，保存仍按标记+选中判定。
  const capNone = capExplicit && selCaps.size === 0
  const skillNone = skillExplicit && selSkills.size === 0

  return (
    <div className="mx-auto max-w-6xl p-6">
      {/* 页头 */}
      <div className="mb-5">
        <Button variant="ghost" size="sm" className="-ml-2 mb-2 text-muted-foreground" onClick={() => navigate('/admin/agents')}>
          <ArrowLeft className="size-4" /> 返回列表
        </Button>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex items-center gap-3.5">
            <div
              className="flex size-14 shrink-0 items-center justify-center rounded-2xl border bg-gradient-to-br from-blue-500/15 via-card to-violet-500/10 text-2xl shadow-sm"
              aria-hidden
            >
              {agent.avatar || agent.name.charAt(0).toUpperCase()}
            </div>
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <h1 className="text-xl font-semibold tracking-tight">{agent.name}</h1>
                <Badge variant="outline" className={cn('text-[10px]', st.cls)}>
                  {st.label}
                </Badge>
              </div>
              {agent.description && (
                <p className="mt-0.5 max-w-lg truncate text-xs text-muted-foreground" title={agent.description}>
                  {agent.description}
                </p>
              )}
              <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[11px] text-muted-foreground">
                <span>{agent.id}</span>
                <span className="inline-flex items-center gap-1">
                  <User className="size-3" /> owner: {agent.owner_user_id || '未绑定'}
                </span>
                <span className="inline-flex items-center gap-1">
                  <CalendarDays className="size-3" /> 创建于 {fmtTime(agent.created_at)}
                </span>
              </div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={refreshAll} disabled={loading} title="重新拉取智能体信息与当前 Tab 数据">
              <RefreshCw className={cn('size-4', loading && 'animate-spin')} /> 刷新
            </Button>
            <Button variant="outline" size="sm" onClick={() => void toggleStatus()} disabled={busy}>
              {agent.status === 1 ? <Square className="size-4" /> : <Play className="size-4" />}
              {agent.status === 1 ? '停用' : '启用'}
            </Button>
            {!isTutor && (
              <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => void removeAgent()} disabled={busy}>
                <Trash2 className="size-4" /> 删除
              </Button>
            )}
          </div>
        </div>
      </div>

      {error && <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>}
      {notice && <div className="mb-3 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-sm text-primary">{notice}</div>}

      {/* Tabs */}
      <div className="mb-4 flex gap-1 border-b">
        {TABS.map((t) => {
          const Icon = t.icon
          return (
            <button
              key={t.key}
              onClick={() => {
                setTab(t.key)
                setError('')
                setNotice('')
              }}
              className={cn(
                '-mb-px flex items-center gap-1.5 border-b-2 px-3.5 py-2 text-sm transition-colors',
                tab === t.key ? 'border-primary font-medium text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground',
              )}
            >
              <Icon className="size-3.5" />
              {t.label}
            </button>
          )
        })}
      </div>

      {/* 信息 Tab */}
      {tab === 'info' &&
        (editing ? (
          <div className="max-w-2xl space-y-4 rounded-xl border bg-card p-5">
            {saveError && <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{saveError}</div>}
            <div className="mb-4 flex items-center gap-1.5">
              <Pencil className="size-4 text-blue-500" />
              <h2 className="text-sm font-semibold">编辑基本信息</h2>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ad-avatar">形象（emoji，可选；空 = 首字兜底）</Label>
              <Input id="ad-avatar" value={form.avatar} maxLength={8} placeholder="如：🦉" onChange={(e) => setForm((f) => ({ ...f, avatar: e.target.value }))} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ad-welcome">欢迎语（可选；空 = 实例默认）</Label>
              <Input id="ad-welcome" value={form.welcome} maxLength={200} placeholder="新会话首屏展示的问候语" onChange={(e) => setForm((f) => ({ ...f, welcome: e.target.value }))} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ad-desc">描述（可选，≤200 字）</Label>
              <Textarea id="ad-desc" value={form.description} maxLength={200} rows={2} placeholder="该智能体面向的用户与覆盖内容" onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ad-sp">系统提示词（可选；空 = 实例全局 prompt）</Label>
              <Textarea id="ad-sp" value={form.system_prompt} rows={6} placeholder="该智能体的专属系统提示词" onChange={(e) => setForm((f) => ({ ...f, system_prompt: e.target.value }))} />
            </div>
            <div className="flex items-center justify-end gap-2">
              <Button variant="outline" onClick={() => setEditing(false)} disabled={busy}>
                取消
              </Button>
              <Button onClick={() => void saveInfo()} disabled={busy}>
                {busy ? <Loader2 className="size-4 animate-spin" /> : '保存'}
              </Button>
            </div>
          </div>
        ) : (
          <div className="max-w-4xl space-y-3">
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-semibold">基本信息</h2>
              <Button variant="outline" size="sm" onClick={() => { setEditing(true); setSaveError('') }}>
                <Pencil className="size-3.5" /> 编辑
              </Button>
            </div>
            <div className="grid gap-4 md:grid-cols-2">
              <div className="rounded-xl border bg-card p-5">
                <h3 className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">身份</h3>
                <dl className="space-y-2.5 text-sm">
                  <Row label="ID" value={agent.id} mono />
                  <Row label="名称" value={agent.name} />
                  <Row label="描述" value={agent.description || '-'} />
                  <Row label="形象" value={agent.avatar || '首字兜底'} />
                  <Row label="欢迎语" value={agent.welcome || '实例默认'} />
                </dl>
              </div>
              <div className="rounded-xl border bg-card p-5">
                <h3 className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">运行配置</h3>
                <dl className="space-y-2.5 text-sm">
                  <div className="flex gap-4">
                    <dt className="w-28 shrink-0 text-muted-foreground">超管（owner）</dt>
                    <dd className="flex min-w-0 flex-1 items-center justify-between gap-2">
                      <span className="min-w-0 truncate font-mono text-xs text-foreground">{agent.owner_user_id || '未绑定'}</span>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-6 shrink-0 px-2 text-[11px]"
                        onClick={openOwnerDialog}
                        title="绑定/更换/解绑该智能体超管（仅最高超管）"
                      >
                        <UserRound className="size-3" /> 绑定/更换
                      </Button>
                    </dd>
                  </div>
                  <Row label="创建时间" value={fmtTime(agent.created_at)} mono />
                  <Row label="更新时间" value={fmtTime(agent.updated_at)} mono />
                </dl>
              </div>
            </div>
            <div className="rounded-xl border bg-card p-5">
              <h3 className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">系统提示词</h3>
              <p className="whitespace-pre-wrap break-words rounded-md bg-muted/30 px-3 py-2 text-xs leading-relaxed text-foreground/90">
                {agent.system_prompt || '实例默认'}
              </p>
            </div>
          </div>
        ))}

      {/* 资源 Tab：内嵌技能/MCP/知识库管理（固定该智能体域） */}
      {tab === 'resources' && (
        <div className="space-y-4">
          <ResourceTabs agentId={agent.id} />
        </div>
      )}

      {/* 默认配置 Tab */}
      {tab === 'defaults' && (
        <div className="max-w-2xl space-y-4">
          <div className="rounded-xl border bg-card p-5">
            <div className="mb-4">
              <h2 className="flex items-center gap-1.5 text-sm font-semibold">
                <Settings2 className="size-4 text-blue-500" /> 默认会话配置
              </h2>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                新会话在创建时固化（快照）当时的默认配置，此后普通用户在配置区对会话的实时修改即时覆盖。管理端再调整默认配置只影响后续新建会话，历史会话不受影响。
              </p>
            </div>

            <div className="space-y-4">
              <div className="space-y-3">
                <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">资源域默认</h3>
                {/* 能力与技能是独立配置类别（P3 反馈）：各自有选择集与"全不选"标记。
                    共享 enabled_resources 联合数组，但 UI 与语义按类别独立。 */}
                <MultiSelect
                  label="默认能力（全局能力）"
                  options={capList.map((r) => ({ value: r.id, label: r.name }))}
                  selected={selCaps}
                  onChange={(next) => mergeCategory('capability', next)}
                  placeholder="该域暂无能力"
                />
                <ExplicitNoneToggle
                  label="不启用任何能力（仅保留基础对话，无能力工具）"
                  hint="勾选即清空能力选择并随快照固化：新会话默认不启用任何能力。未勾选且未选能力 = 能力跟随实例全量。"
                  checked={capNone}
                  onChange={toggleCapNone}
                />
                <MultiSelect
                  label="默认技能（该域已启用的技能）"
                  options={skillList.map((r) => ({ value: r.id, label: r.name }))}
                  selected={selSkills}
                  onChange={(next) => mergeCategory('skill', next)}
                  placeholder="该域暂无技能（或技能均未启用）"
                />
                <ExplicitNoneToggle
                  label="不启用任何技能（仅保留基础对话，无技能工具）"
                  hint="勾选即清空技能选择并随快照固化：新会话默认不启用任何技能。未勾选且未选技能 = 技能跟随实例全量。"
                  checked={skillNone}
                  onChange={toggleSkillNone}
                />
                <MultiSelect
                  label="默认知识库（kb_search 限定范围，仅显示已启用）"
                  options={kbList.map((k) => ({ value: k.id, label: k.name }))}
                  selected={selKbs}
                  onChange={(next) => { setSelKbs(next); if (next.size > 0) setSelKbsExplicit(false); markDefaultsDirty() }}
                  placeholder="该域暂无已启用的知识库"
                />
                <ExplicitNoneToggle
                  label="新会话默认不使用知识库检索"
                  hint="勾选后随快照固化到新会话；需先清空上方选择再勾选。"
                  checked={selKbsExplicit}
                  disabled={selKbs.size > 0}
                  onChange={(v) => { setSelKbsExplicit(v); markDefaultsDirty() }}
                />
                <MultiSelect
                  label="默认 MCP Server（仅显示已启用的连接）"
                  options={mcpList.map((m) => ({ value: m.name, label: m.name }))}
                  selected={selMcp}
                  onChange={(next) => { setSelMcp(next); if (next.size > 0) setSelMcpExplicit(false); markDefaultsDirty() }}
                  placeholder="该域暂无已启用的 MCP 连接"
                />
                <ExplicitNoneToggle
                  label="新会话默认不装配任何 MCP 工具"
                  hint="勾选后随快照固化到新会话；需先清空上方选择再勾选。"
                  checked={selMcpExplicit}
                  disabled={selMcp.size > 0}
                  onChange={(v) => { setSelMcpExplicit(v); markDefaultsDirty() }}
                />
              </div>

              <hr className="border-border" />

              {/* 默认大模型：列表来自公开 /v1/models（管理端大模型管理接入的模型）。
                  combobox 交互对齐配置区 LLMDialog：选中系统默认 = 不锁定。 */}
              <div className="space-y-1.5">
                <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">默认大模型</h3>
                <div className="relative" ref={modelRef}>
                  <button
                    type="button"
                    onClick={() => setModelOpen((v) => !v)}
                    disabled={modelList.length === 0}
                    aria-haspopup="listbox"
                    aria-expanded={modelOpen}
                    className={cn(
                      'flex h-9 w-full items-center justify-between rounded-md border bg-background px-3 text-sm transition-colors focus:outline-none focus:ring-2 focus:ring-ring',
                      modelOpen ? 'border-ring' : 'border-border hover:bg-accent/40',
                      modelList.length === 0 && 'cursor-not-allowed opacity-60',
                    )}
                  >
                    {selectedModelObj ? (
                      <span className="flex min-w-0 items-center gap-2">
                        <span
                          className={cn(
                            'flex size-5 shrink-0 items-center justify-center rounded',
                            selectedModelObj.is_default
                              ? 'bg-amber-500/15 text-amber-600 dark:text-amber-300'
                              : 'bg-muted text-muted-foreground',
                          )}
                        >
                          <Cpu className="size-3" />
                        </span>
                        <span className="truncate font-medium">{selectedModelObj.name}</span>
                        {selectedModelObj.is_default && (
                          <span className="flex shrink-0 items-center gap-0.5 text-[10px] text-amber-600 dark:text-amber-300">
                            <Star className="size-2.5 fill-current" /> 系统默认
                          </span>
                        )}
                      </span>
                    ) : (
                      <span className="text-muted-foreground">暂无可用模型</span>
                    )}
                    <ChevronDown
                      className={cn('size-4 shrink-0 text-muted-foreground transition-transform', modelOpen && 'rotate-180')}
                    />
                  </button>
                  {modelOpen && (
                    <div className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-md border bg-background shadow-lg">
                      <ul role="listbox" className="max-h-56 overflow-y-auto p-1">
                        {modelList.map((m) => {
                          const active = selModel === m.name
                          return (
                            <li key={m.name}>
                              <button
                                type="button"
                                role="option"
                                aria-selected={active}
                                onClick={() => {
                                  setSelModel(m.name)
                                  setModelTouched(true)
                                  setModelOpen(false)
                                  markDefaultsDirty()
                                }}
                                className={cn(
                                  'flex w-full items-center justify-between gap-2 rounded px-2.5 py-1.5 text-left text-sm transition-colors',
                                  active ? 'bg-accent' : 'hover:bg-accent/50',
                                )}
                              >
                                <span className="flex min-w-0 items-center gap-2">
                                  <span
                                    className={cn(
                                      'flex size-5 shrink-0 items-center justify-center rounded',
                                      m.is_default
                                        ? 'bg-amber-500/15 text-amber-600 dark:text-amber-300'
                                        : 'bg-muted text-muted-foreground',
                                    )}
                                  >
                                    <Cpu className="size-3" />
                                  </span>
                                  <span className="min-w-0">
                                    <span className="flex items-center gap-1.5 font-medium">
                                      <span className="truncate">{m.name}</span>
                                      {m.is_default && (
                                        <span className="shrink-0 text-[10px] text-amber-600 dark:text-amber-300">系统默认</span>
                                      )}
                                    </span>
                                    {m.provider_name && (
                                      <span className="block truncate text-xs text-muted-foreground">{m.provider_name}</span>
                                    )}
                                  </span>
                                </span>
                                {active && <Check className="size-4 shrink-0 text-primary" />}
                              </button>
                            </li>
                          )
                        })}
                      </ul>
                    </div>
                  )}
                </div>
                <p className="text-[11px] leading-relaxed text-muted-foreground">
                  新会话未显式选模型时使用该模型；不在这里设置（或清空默认配置）则跟随当前系统默认模型。
                  {modelList.length === 0 && ' 当前无可用模型，请先到「大模型管理」接入并启用。'}
                </p>
              </div>

              <hr className="border-border" />

              <div className="space-y-3">
                <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">思考模式默认</h3>
                <div className="flex items-center justify-between rounded-md border px-3 py-2.5">
                  <div>
                    <div className="text-sm font-medium">深度思考</div>
                    <div className="text-xs text-muted-foreground">关闭后模型直接回答，不产生思考过程</div>
                  </div>
                  <input
                    id="ad-think"
                    type="checkbox"
                    checked={thinkingEnabled}
                    onChange={(e) => { setThinkingEnabled(e.target.checked); markDefaultsDirty() }}
                    className="size-4"
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="ad-def-effort">默认推理强度</Label>
                  <select
                    id="ad-def-effort"
                    value={reasoningEffort}
                    onChange={(e) => { setReasoningEffort(e.target.value); markDefaultsDirty() }}
                    className="h-9 w-full rounded-md border bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                  >
                    {DEFAULTS_REASONING_OPTIONS.map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                  </select>
                  <p className="text-[11px] text-muted-foreground">
                    未显式配置强度时新会话默认 {defaultEffort}
                  </p>
                </div>
              </div>

              <hr className="border-border" />

              {/* 运行模式默认（P4-F）：single（单智能体）↔ orchestrate（多智能体编排）
                  combobox 交互对齐配置区 ModeDialog。 */}
              <div className="space-y-3">
                <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">运行模式默认</h3>
                <div className="relative" ref={modeRef}>
                  <button
                    type="button"
                    onClick={() => setModeOpen((v) => !v)}
                    aria-haspopup="listbox"
                    aria-expanded={modeOpen}
                    className={cn(
                      'flex h-9 w-full items-center justify-between rounded-md border bg-background px-3 text-sm transition-colors focus:outline-none focus:ring-2 focus:ring-ring',
                      modeOpen ? 'border-ring' : 'border-border hover:bg-accent/40',
                    )}
                  >
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="flex size-5 shrink-0 items-center justify-center rounded bg-muted text-muted-foreground">
                        {mode === 'single' ? <UserRound className="size-3" /> : <Network className="size-3" />}
                      </span>
                      <span className="truncate font-medium">{mode === 'single' ? '单智能体' : '多智能体编排'}</span>
                    </span>
                    <ChevronDown
                      className={cn('size-4 shrink-0 text-muted-foreground transition-transform', modeOpen && 'rotate-180')}
                    />
                  </button>
                  {modeOpen && (
                    <div className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-md border bg-background shadow-lg">
                      <ul role="listbox" className="max-h-56 overflow-y-auto p-1">
                        {[
                          { value: 'single' as const, label: '单智能体', desc: '一个智能体直接对话（可调用工具），响应快、成本低' },
                          { value: 'orchestrate' as const, label: '多智能体编排', desc: '内置教研角色池拆解协作（研究→大纲→正文→审核），质量更高、耗时更长' },
                        ].map((m) => {
                          const active = mode === m.value
                          return (
                            <li key={m.value}>
                              <button
                                type="button"
                                role="option"
                                aria-selected={active}
                                onClick={() => {
                                  setMode(m.value)
                                  setModeOpen(false)
                                  markDefaultsDirty()
                                }}
                                className={cn(
                                  'flex w-full items-center justify-between gap-2 rounded px-2.5 py-1.5 text-left text-sm transition-colors',
                                  active ? 'bg-accent' : 'hover:bg-accent/50',
                                )}
                              >
                                <span className="flex min-w-0 items-center gap-2">
                                  <span className="flex size-5 shrink-0 items-center justify-center rounded bg-muted text-muted-foreground">
                                    {m.value === 'single' ? <UserRound className="size-3" /> : <Network className="size-3" />}
                                  </span>
                                  <span className="min-w-0">
                                    <span className="block truncate font-medium">{m.label}</span>
                                    <span className="block truncate text-xs text-muted-foreground">{m.desc}</span>
                                  </span>
                                </span>
                                {active && <Check className="size-4 shrink-0 text-primary" />}
                              </button>
                            </li>
                          )
                        })}
                      </ul>
                    </div>
                  )}
                </div>
                <p className="text-[11px] leading-relaxed text-muted-foreground">
                  新会话未显式选择模式时使用该默认；用户仍可在会话配置区「运行模式」中改选，只影响后续新建会话。
                </p>
              </div>

              {/* 编排方案默认（P4-J）：fixed（固定教研流水线）↔ dynamic（LLM 动态分解）。
                  仅多智能体编排默认下展示；新会话按快照固化，用户可在配置区改选。 */}
              {mode === 'orchestrate' && (
                <div className="space-y-3">
                  <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">编排方案默认</h3>
                  <div className="grid gap-1.5">
                    {[
                      { value: 'fixed' as const, label: '固定教研流水线', desc: '研究 → 大纲 → 正文 → 审核，流程可控、成本低' },
                      { value: 'dynamic' as const, label: '动态分解', desc: 'LLM 按目标实时拆解子任务 DAG，更灵活（多一次模型调用）' },
                    ].map((p) => {
                      const active = plan === p.value
                      return (
                        <button
                          key={p.value}
                          type="button"
                          role="radio"
                          aria-checked={active}
                          onClick={() => {
                            setPlan(p.value)
                            markDefaultsDirty()
                          }}
                          className={cn(
                            'flex items-start justify-between gap-2 rounded-md border border-border px-3 py-2 text-left transition-colors hover:bg-accent/40 focus:outline-none focus:ring-2 focus:ring-ring',
                          )}
                        >
                          <span className="min-w-0">
                            <span className="block text-sm font-medium">{p.label}</span>
                            <span className="block text-[11px] text-muted-foreground">{p.desc}</span>
                          </span>
                          {active && (
                            <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground">
                              <Check className="size-3.5" strokeWidth={3} />
                            </span>
                          )}
                        </button>
                      )
                    })}
                  </div>
                  <p className="text-[11px] leading-relaxed text-muted-foreground">
                    决定新会话编排模式下子任务如何拆解协作；只影响后续新建会话。
                  </p>
                </div>
              )}

              <hr className="border-border" />

              {/* 管理员级运行限制：随快照固化到新会话，普通用户配置区不可见/不可改。
                  0 = 不设置该项默认，装配时回退服务实例默认（工具轮次 8 / 记忆窗口 20）。 */}
              <div className="space-y-3">
                <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">运行限制（管理员级）</h3>
                <div className="space-y-3 rounded-md border bg-muted/20 p-3">
                  <div className="grid gap-2.5 sm:grid-cols-3">
                    <div className="space-y-1">
                      <Label htmlFor="ad-max-rounds">工具调用轮次</Label>
                      <Input
                        id="ad-max-rounds"
                        type="number"
                        min={0}
                        max={100}
                        step={1}
                        value={adminRounds}
                        placeholder="0 = 实例默认 8"
                        onChange={(e) => { setAdminRounds(e.target.value); markDefaultsDirty() }}
                      />
                      <p className="text-[11px] leading-relaxed text-muted-foreground">
                        单次对话最大推理轮数，防止工具循环不收敛
                      </p>
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor="ad-max-messages">对话历史窗口</Label>
                      <Input
                        id="ad-max-messages"
                        type="number"
                        min={0}
                        step={1}
                        value={adminMessages}
                        placeholder="0 = 实例默认 20"
                        onChange={(e) => { setAdminMessages(e.target.value); markDefaultsDirty() }}
                      />
                      <p className="text-[11px] leading-relaxed text-muted-foreground">
                        短期记忆保留的消息数上限（一轮对话含工具调用可产生多条；溢出时按 1/3 批量压缩，而非逐条）
                      </p>
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor="ad-max-think">思考轮次上限</Label>
                      <Input
                        id="ad-max-think"
                        type="number"
                        min={0}
                        max={100}
                        step={1}
                        value={adminThinkingRounds}
                        placeholder="0 = 不单独限制"
                        onChange={(e) => { setAdminThinkingRounds(e.target.value); markDefaultsDirty() }}
                      />
                      <p className="text-[11px] leading-relaxed text-muted-foreground">
                        只统计调用工具的思考轮；0 = 仅受总轮次保护
                      </p>
                    </div>
                  </div>
                  <p className="text-[11px] leading-relaxed text-muted-foreground">
                    管理员级配置：随快照固化到新建会话，普通用户配置区不可见、不可改；修改只影响后续新建会话，旧会话不受影响。
                  </p>
                </div>
              </div>

              <hr className="border-border" />

              <div className="space-y-1.5">
                <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">工具白名单（高级）</h3>
                <Input
                  id="ad-tools"
                  value={toolsText}
                  placeholder="如：calculator, web_search；留空 = 全部"
                  onChange={(e) => { setToolsText(e.target.value); markDefaultsDirty() }}
                />
                <p className="text-[11px] text-muted-foreground">逗号分隔；留空 = 全部工具启用</p>
              </div>
            </div>

            {defaultsMsg && (
              <div
                role="status"
                aria-live="polite"
                className={cn(
                  'mt-4 flex items-start gap-2 rounded-md border px-3 py-2 text-sm',
                  defaultsMsg.type === 'success'
                    ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300'
                    : 'border-destructive/40 bg-destructive/10 text-destructive',
                )}
              >
                {defaultsMsg.type === 'success' ? (
                  <CheckCircle2 className="mt-0.5 size-4 shrink-0" />
                ) : (
                  <AlertCircle className="mt-0.5 size-4 shrink-0" />
                )}
                <span>{defaultsMsg.text}</span>
              </div>
            )}

            <div className="mt-4 flex items-center justify-end gap-2 border-t pt-4">
              <Button variant="outline" onClick={() => void clearDefaults()} disabled={busy || !defaultsDirty}>
                清空默认
              </Button>
              <Button onClick={() => void saveDefaults()} disabled={busy}>
                {busy ? <Loader2 className="size-4 animate-spin" /> : '保存默认配置'}
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* 用量 Tab */}
      {tab === 'usage' && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border bg-card px-4 py-3 shadow-sm">
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Gauge className="size-3.5" /> 最近成功调用的用量聚合（来自 llm-gateway 用量日志）
            </p>
            <div className="flex items-center gap-2">
              <select
                value={usageDays}
                onChange={(e) => setUsageDays(Number(e.target.value))}
                className="h-8 rounded-md border bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                aria-label="统计窗口"
              >
                <option value={7}>近 7 天</option>
                <option value={30}>近 30 天</option>
                <option value={90}>近 90 天</option>
              </select>
              <Button variant="outline" size="sm" onClick={() => void loadUsage()} disabled={usageLoading}>
                <RefreshCw className={cn('size-3.5', usageLoading && 'animate-spin')} /> 刷新
              </Button>
            </div>
          </div>
          {usageLoading ? (
            <div className="flex justify-center py-12">
              <Loader2 className="size-5 animate-spin text-muted-foreground" />
            </div>
          ) : usage ? (
            <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
              <StatCard icon={<Hash className="size-3.5" />} label="调用次数" value={String(usage.calls)} tone="text-blue-500" />
              <StatCard icon={<Coins className="size-3.5" />} label="输入 tokens" value={fmtNum(usage.prompt_tokens)} tone="text-sky-500" />
              <StatCard icon={<Coins className="size-3.5" />} label="输出 tokens" value={fmtNum(usage.completion_tokens)} tone="text-violet-500" />
              <StatCard icon={<CircleDollarSign className="size-3.5" />} label="总成本（USD）" value={`$${usage.cost_usd.toFixed(4)}`} tone="text-amber-500" />
              <StatCard icon={<Coins className="size-3.5" />} label="总 tokens" value={fmtNum(usage.total_tokens)} tone="text-emerald-500" />
              <StatCard icon={<Clock className="size-3.5" />} label="最近使用" value={usage.last_used_at ? fmtTime(usage.last_used_at) : '无调用'} tone="text-muted-foreground" />
            </div>
          ) : (
            <div className="rounded-xl border border-dashed bg-card/50 py-14 text-center">
              <Gauge className="mx-auto size-8 text-muted-foreground/50" />
              <p className="mt-2 text-sm text-muted-foreground">暂无用量数据（服务未配置或该域近期无调用）</p>
            </div>
          )}
        </div>
      )}

      {/* 绑定/更换超管弹窗（仅最高超管） */}
      {ownerOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="flex w-full max-w-md flex-col overflow-hidden rounded-xl border bg-background shadow-2xl" role="dialog" aria-modal="true">
            <div className="border-b px-5 py-3.5">
              <div className="text-sm font-semibold">绑定 / 更换超管</div>
              <div className="mt-0.5 text-xs text-muted-foreground">
                绑定后该用户被授予 agent_admin 并归属此智能体；原超管的 agent_admin 与归属被自动回收。
              </div>
            </div>
            <div className="flex-1 overflow-y-auto p-5">
              {ownerError && <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{ownerError}</div>}
              <div className="space-y-1.5">
                <Label htmlFor="ad-owner">用户 ID（空 = 解绑当前超管）</Label>
                <Input
                  id="ad-owner"
                  value={ownerId}
                  placeholder="输入用户 ID，如 5"
                  onChange={(e) => setOwnerId(e.target.value)}
                  autoFocus
                />
              </div>
            </div>
            <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
              <Button variant="outline" onClick={() => setOwnerOpen(false)} disabled={ownerBusy}>
                取消
              </Button>
              <Button onClick={() => void submitOwner()} disabled={ownerBusy}>
                {ownerBusy ? <Loader2 className="size-4 animate-spin" /> : ownerId.trim() ? '绑定 / 更换' : '解绑'}
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/** 信息只读行。 */
function Row({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-4">
      <dt className="w-28 shrink-0 text-muted-foreground">{label}</dt>
      <dd className={cn('min-w-0 flex-1 text-foreground', mono && 'font-mono text-xs')}>{value}</dd>
    </div>
  )
}

/** 用量统计卡。tone 控制图标配色（text-* 类）。 */
function StatCard({ icon, label, value, tone = 'text-foreground' }: { icon: React.ReactNode; label: string; value: string; tone?: string }) {
  return (
    <div className="rounded-xl border bg-card p-4 shadow-sm">
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <span className={cn('flex size-6 shrink-0 items-center justify-center rounded-md border bg-muted/40', tone)}>{icon}</span>
        {label}
      </div>
      <div className="mt-2 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  )
}

/** 资源子 Tab：内嵌技能 / MCP / 知识库管理（固定 agentId）。 */
function ResourceTabs({ agentId }: { agentId: string }) {
  const [sub, setSub] = useState<'skills' | 'mcp' | 'kb'>('skills')
  const SUB_TABS: { key: 'skills' | 'mcp' | 'kb'; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
    { key: 'skills', label: '技能', icon: Wrench },
    { key: 'mcp', label: 'MCP', icon: Server },
    { key: 'kb', label: '知识库', icon: BookOpen },
  ]
  return (
    <div>
      <div className="mb-3 flex gap-1 border-b">
        {SUB_TABS.map((t) => {
          const Icon = t.icon
          return (
            <button
              key={t.key}
              onClick={() => setSub(t.key)}
              className={cn(
                '-mb-px flex items-center gap-1.5 border-b-2 px-3 py-1.5 text-xs transition-colors',
                sub === t.key ? 'border-primary font-medium text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground',
              )}
            >
              <Icon className="size-3.5" />
              {t.label}
            </button>
          )
        })}
      </div>
      {/* 仅挂载当前子 Tab：切换时重新拉取，避免三个重型组件同时驻留 */}
      {sub === 'skills' && <SkillsPage fixedAgentId={agentId} />}
      {sub === 'mcp' && <McpPage fixedAgentId={agentId} />}
      {sub === 'kb' && <KnowledgeBasePage fixedAgentId={agentId} />}
    </div>
  )
}

/** RFC3339 → 本地可读时间。 */
function fmtTime(v?: string): string {
  if (!v) return '-'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 大数字千分位。 */
function fmtNum(n: number): string {
  return n.toLocaleString('en-US')
}
