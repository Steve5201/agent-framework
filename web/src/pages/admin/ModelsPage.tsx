import { useCallback, useEffect, useState } from 'react'
import {
  adminCreateModel,
  adminDeleteModel,
  adminListModels,
  adminSetModelDefault,
  adminSetModelEnabled,
  adminUpdateModel,
} from '@/lib/api'
import type { Model, ModelInput } from '@/types/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  AlertTriangle,
  Bot,
  CheckCircle2,
  Coins,
  Cpu,
  KeyRound,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  ShieldCheck,
  Star,
  Trash2,
} from 'lucide-react'
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
        className="flex max-h-[85vh] w-full max-w-2xl flex-col overflow-hidden rounded-xl border bg-background shadow-2xl"
        role="dialog"
        aria-modal="true"
      >
        <div className="border-b px-5 py-3.5">
          <div className="text-sm font-semibold">{title}</div>
          {subtitle && <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>}
        </div>
        <div className="flex-1 overflow-y-auto p-5">{children}</div>
        <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">{footer}</div>
      </div>
    </div>
  )
}

/** 空表单态。 */
function blankForm(): ModelInput {
  return {
    name: '',
    provider_name: '',
    base_url: '',
    api_key: '',
    upstream_model: '',
    timeout_sec: 60,
    max_retries: 0,
    prompt_price_per_1m: 0,
    completion_price_per_1m: 0,
    is_default: false,
    no_thinking: false,
    max_tokens: 0,
  }
}

/** 校验表单并构造提交体（错误抛 string）。 */
function buildPayload(form: ModelInput, editing: boolean): ModelInput {
  const name = form.name.trim()
  if (!editing && !name) throw new Error('模型名不能为空')
  if (name && !/^[A-Za-z0-9._:+-]{1,64}$/.test(name)) {
    throw new Error('模型名只能包含字母/数字/._:+-（1~64 位），不能含斜杠或空白')
  }
  const baseUrl = (form.base_url ?? '').trim()
  if (!baseUrl) throw new Error('上游端点 BaseURL 不能为空（本地模型同样要填 OpenAI 兼容地址）')
  const timeout = form.timeout_sec ?? 60
  if (timeout < 0 || timeout > 600) throw new Error('超时需在 0~600 秒之间（0 = 上游默认 60）')
  const retries = form.max_retries ?? 0
  if (retries < 0 || retries > 10) throw new Error('重试次数需在 0~10 之间')
  const maxTokens = form.max_tokens ?? 0
  if (maxTokens < 0 || maxTokens > 131072) throw new Error('max_tokens 需在 0~131072 之间（0 = 不设置，交上游默认）')
  const promptPrice = Number(form.prompt_price_per_1m ?? 0)
  const completionPrice = Number(form.completion_price_per_1m ?? 0)
  if (promptPrice < 0 || completionPrice < 0) throw new Error('模型价格不能为负数')
  const payload: ModelInput = {
    name,
    provider_name: form.provider_name?.trim() || undefined,
    base_url: baseUrl,
    api_key: form.api_key?.trim() || undefined, // 更新时空 = 保留原密钥
    upstream_model: form.upstream_model?.trim() || undefined,
    timeout_sec: timeout,
    max_retries: retries,
    prompt_price_per_1m: promptPrice,
    completion_price_per_1m: completionPrice,
    no_thinking: !!form.no_thinking,
    max_tokens: maxTokens,
    // 默认位只能经"设为默认"操作修改，提交体不携带
  }
  return payload
}

/** 模型名 -> 展示用图标主题色（按名散列，简单区分）。 */
function nameColor(name: string): string {
  const palette = [
    'bg-blue-500/15 text-blue-600 dark:text-blue-300',
    'bg-sky-500/15 text-sky-600 dark:text-sky-300',
    'bg-emerald-500/15 text-emerald-600 dark:text-emerald-300',
    'bg-amber-500/15 text-amber-600 dark:text-amber-300',
    'bg-rose-500/15 text-rose-600 dark:text-rose-300',
  ]
  let h = 0
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return palette[h % palette.length]
}

/**
 * 大模型管理（super_admin / agent_admin）：
 * 模型增删改走 llm-gateway 管理端点（API Key 只存在于 llm-gateway，前端仅见打码值）。
 * 至多一个默认模型：会话未显式选模型时落到它；删除默认模型会自动提升最早剩余模型。
 */
export default function ModelsPage() {
  const [models, setModels] = useState<Model[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const [modal, setModal] = useState<'create' | 'edit' | null>(null)
  const [form, setForm] = useState<ModelInput>(blankForm())
  // 编辑态锁定的模型名（name 是 llm-gateway 路由键，创建后不可修改）
  const [editingName, setEditingName] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  /** 打码后的原密钥展示值（编辑弹窗回显用，判断是否有密钥） */
  const [maskedKey, setMaskedKey] = useState('')

  const load = useCallback(() => {
    adminListModels()
      .then((list) => {
        setModels(list)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  function openCreate() {
    setForm(blankForm())
    setEditingName('')
    setMaskedKey('')
    setSaveError('')
    setModal('create')
  }

  function openEdit(m: Model) {
    setForm({
      name: m.name,
      provider_name: m.provider_name ?? '',
      base_url: m.base_url ?? '',
      api_key: '', // 更新留空 = 保留原密钥；不回显打码值到输入框
      upstream_model: m.upstream_model ?? '',
      timeout_sec: m.timeout_sec ?? 60,
      max_retries: m.max_retries ?? 0,
      prompt_price_per_1m: m.prompt_price_per_1m ?? 0,
      completion_price_per_1m: m.completion_price_per_1m ?? 0,
      is_default: m.is_default ?? false,
      no_thinking: m.no_thinking ?? false,
      max_tokens: m.max_tokens ?? 0,
    })
    setEditingName(m.name)
    setMaskedKey(m.api_key ?? '')
    setSaveError('')
    setModal('edit')
  }

  async function save() {
    let payload: ModelInput
    try {
      payload = buildPayload(form, modal === 'edit')
    } catch (e) {
      setSaveError((e as Error).message)
      return
    }
    setSaving(true)
    setSaveError('')
    try {
      if (modal === 'create') {
        const m = await adminCreateModel(payload)
        setNotice(`模型「${m.name}」已接入。${m.is_default ? '已设为默认模型。' : ''}`)
      } else {
        const m = await adminUpdateModel(editingName, payload)
        setNotice(`模型「${m.name}」已更新`)
      }
      setModal(null)
      void load()
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  async function setDefault(m: Model) {
    try {
      await adminSetModelDefault(m.name)
      setNotice(`模型「${m.name}」已设为默认（原默认自动取消）`)
      void load()
    } catch (e) {
      alert(`设置默认失败：${(e as Error).message}`)
    }
  }

  async function toggleEnabled(m: Model) {
    const next = m.enabled !== false
    try {
      await adminSetModelEnabled(m.name, !next)
      setNotice(`模型「${m.name}」已${next ? '禁用（不再参与路由与配置区下拉，配置保留）' : '启用'}`)
      void load()
    } catch (e) {
      alert(`操作失败：${(e as Error).message}`)
    }
  }

  async function remove(m: Model) {
    if (!window.confirm(`删除模型「${m.name}」？该模型将不可再被会话选用。`)) return
    try {
      await adminDeleteModel(m.name)
      setNotice(`模型「${m.name}」已删除`)
      void load()
    } catch (e) {
      alert(`删除失败：${(e as Error).message}`)
    }
  }

  return (
    <div className="mx-auto max-w-6xl p-6">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="flex size-8 items-center justify-center rounded-lg bg-blue-500/15 text-blue-600 dark:text-blue-300">
              <Cpu className="size-4.5" />
            </div>
            <h1 className="text-lg font-semibold tracking-tight">大模型管理</h1>
          </div>
          <p className="mt-1.5 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            接入 OpenAI 兼容的大模型（云端 API / 本地推理服务），保存后 agent 请求按模型名路由。
            API Key 只存储在 llm-gateway，此处仅显示打码值；本地模型可不填密钥。
            默认模型唯一且不可删除/禁用（始终有且仅有一个兜底实例），可随时转移默认位。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => { setLoading(true); void load() }} disabled={loading}>
            <RefreshCw className={cn('size-3.5', loading && 'animate-spin')} /> 刷新
          </Button>
          <Button onClick={openCreate}>
            <Plus className="size-4" /> 接入模型
          </Button>
        </div>
      </div>

      {error && <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>}
      {notice && (
        <div className="mb-3 flex items-center gap-1.5 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-sm text-primary">
          <CheckCircle2 className="size-4 shrink-0" /> <span>{notice}</span>
        </div>
      )}

      {loading ? (
        <div className="flex justify-center py-16">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : models.length === 0 ? (
        <div className="rounded-xl border border-dashed bg-card/50 py-16 text-center">
          <div className="mx-auto mb-2 flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <Bot className="size-5" />
          </div>
          <p className="text-sm text-muted-foreground">
            暂无接入的模型。点击右上角「接入模型」配置第一个 OpenAI 兼容模型。
          </p>
          <p className="mt-1 text-xs text-muted-foreground/70">
            已配置 DEEPSEEK_API_KEY 时首次启动会自动播种默认 DeepSeek 模型（默认模型受保护，不可删除）；
            本地模型部署可保持空表自行添加——首个创建的模型会自动成为默认。
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">模型</th>
                <th className="px-3 py-2.5 font-medium">供应商</th>
                <th className="px-3 py-2.5 font-medium">上游端点 / 模型</th>
                <th className="px-3 py-2.5 font-medium">密钥</th>
                <th className="px-3 py-2.5 font-medium">价格（$/1M）</th>
                <th className="px-3 py-2.5 font-medium">默认</th>
                <th className="px-3 py-2.5 font-medium">状态</th>
                <th className="px-4 py-2.5 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {models.map((m) => (
                <tr key={m.name} className="group border-b transition-colors last:border-0 hover:bg-accent/40">
                  <td className="px-4 py-2.5">
                    <div className="flex items-center gap-2.5">
                      <span className={cn('flex size-8 shrink-0 items-center justify-center rounded-lg', nameColor(m.name))}>
                        <Cpu className="size-4" />
                      </span>
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5">
                          <span className="font-medium">{m.name}</span>
                          {m.upstream_model && m.upstream_model !== m.name && (
                            <Badge variant="outline" className="max-w-[140px] truncate font-mono text-[10px] text-muted-foreground" title={`实际上游模型：${m.upstream_model}`}>
                              → {m.upstream_model}
                            </Badge>
                          )}
                        </div>
                        {m.timeout_sec || m.max_retries || m.max_tokens ? (
                          <div className="text-[11px] text-muted-foreground">
                            超时 {m.timeout_sec ?? 60}s{m.max_retries ? ` · 重试 ${m.max_retries}` : ''}
                            {m.max_tokens ? ` · 上限 ${m.max_tokens}` : ''}
                          </div>
                        ) : null}
                      </div>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground">{m.provider_name || '-'}</td>
                  <td className="max-w-[240px] truncate px-3 py-2.5 font-mono text-xs text-muted-foreground" title={m.base_url}>
                    {m.base_url || '-'}
                  </td>
                  <td className="px-3 py-2.5">
                    {m.has_api_key ? (
                      <span className="flex items-center gap-1 font-mono text-xs text-emerald-600 dark:text-emerald-400">
                        <KeyRound className="size-3" /> {m.api_key}
                      </span>
                    ) : (
                      <Badge variant="outline" className="text-[10px]">
                        本地 / 无密钥
                      </Badge>
                    )}
                  </td>
                  <td className="px-3 py-2.5">
                    {(m.prompt_price_per_1m || m.completion_price_per_1m) ? (
                      <span className="flex items-center gap-1 font-mono text-xs text-muted-foreground">
                        <Coins className="size-3" />
                        入 {m.prompt_price_per_1m ?? 0} / 出 {m.completion_price_per_1m ?? 0}
                      </span>
                    ) : (
                      <span className="text-xs text-muted-foreground/50">未配置</span>
                    )}
                  </td>
                  <td className="px-3 py-2.5">
                    {m.is_default ? (
                      <Badge className="gap-1 bg-amber-500/15 text-[10px] text-amber-600 dark:text-amber-300">
                        <Star className="size-3 fill-current" /> 默认
                      </Badge>
                    ) : (
                      <span className="text-xs text-muted-foreground/50">-</span>
                    )}
                  </td>
                  <td className="px-3 py-2.5">
                    {m.is_default ? (
                      <span className="text-[11px] text-muted-foreground/70" title="默认模型受保护，始终可用">
                        受保护
                      </span>
                    ) : (
                      <button
                        type="button"
                        role="switch"
                        aria-checked={m.enabled !== false}
                        title={m.enabled !== false ? '点击禁用（不再参与路由与下拉）' : '点击启用'}
                        onClick={() => void toggleEnabled(m)}
                        className={cn(
                          'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                          m.enabled !== false ? 'bg-primary' : 'bg-muted',
                        )}
                      >
                        <span
                          className={cn(
                            'inline-block size-4 rounded-full bg-background shadow transition-transform',
                            m.enabled !== false ? 'translate-x-[18px]' : 'translate-x-0.5',
                          )}
                        />
                      </button>
                    )}
                  </td>
                  <td className="px-4 py-2.5 text-right">
                    <div className="flex justify-end gap-0.5 opacity-70 transition-opacity group-hover:opacity-100">
                      {!m.is_default && m.enabled !== false && (
                        <Button variant="ghost" size="icon" title="设为默认模型" onClick={() => void setDefault(m)}>
                          <Star className="size-4" />
                        </Button>
                      )}
                      <Button variant="ghost" size="icon" title="编辑接入参数" onClick={() => openEdit(m)}>
                        <Pencil className="size-4" />
                      </Button>
                      {!m.is_default ? (
                        <Button variant="ghost" size="icon" title="删除模型" className="text-destructive" onClick={() => void remove(m)}>
                          <Trash2 className="size-4" />
                        </Button>
                      ) : (
                        <span className="flex size-9 items-center justify-center" title="默认模型不可删除">
                          <ShieldCheck className="size-4 text-muted-foreground/50" />
                        </span>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* 接入 / 编辑模型弹窗 */}
      {modal && (
        <Modal
          title={modal === 'create' ? '接入大模型' : `编辑模型：${editingName}`}
          subtitle="OpenAI 兼容端点；本地模型（Ollama / vLLM 等）可不填 API Key"
          footer={
            <>
              {saveError && <span className="mr-auto max-w-[50%] truncate text-xs text-destructive">{saveError}</span>}
              <Button variant="outline" onClick={() => setModal(null)} disabled={saving}>
                取消
              </Button>
              <Button onClick={() => void save()} disabled={saving}>
                {saving ? <Loader2 className="size-4 animate-spin" /> : null} 保存
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            <div className="space-y-1">
              <Label htmlFor="m-name">模型名</Label>
              <Input
                id="m-name"
                value={form.name ?? ''}
                maxLength={64}
                disabled={modal === 'edit'}
                placeholder="如 deepseek-v4 / qwen3-local（字母/数字/._:+-）"
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
              <p className="text-xs text-muted-foreground">
                对外路由名（会话配置区下拉展示），创建后不可修改；厂商模型名含斜杠时填在上游模型名。
              </p>
            </div>

            <div className="space-y-1">
              <Label htmlFor="m-provider">供应商（展示名）</Label>
              <Input
                id="m-provider"
                value={form.provider_name ?? ''}
                maxLength={64}
                placeholder="如 DeepSeek / Ollama / 本地"
                onChange={(e) => setForm({ ...form, provider_name: e.target.value })}
              />
            </div>

            <div className="space-y-1">
              <Label htmlFor="m-base">上游端点 BaseURL</Label>
              <Input
                id="m-base"
                value={form.base_url ?? ''}
                placeholder="如 https://api.deepseek.com/v1 或 http://localhost:11434/v1"
                onChange={(e) => setForm({ ...form, base_url: e.target.value })}
              />
            </div>

            <div className="space-y-1">
              <Label htmlFor="m-key">API Key</Label>
              <Input
                id="m-key"
                type="password"
                value={form.api_key ?? ''}
                autoComplete="new-password"
                placeholder={
                  modal === 'edit'
                    ? maskedKey
                      ? `当前密钥 ${maskedKey}，留空 = 保持不变`
                      : '当前无密钥，可留空保持（本地模型）'
                    : '上游密钥；本地模型可留空'
                }
                onChange={(e) => setForm({ ...form, api_key: e.target.value })}
              />
              <p className="text-xs text-muted-foreground">
                密钥只保存在 llm-gateway；编辑时留空 = 保留原密钥。
              </p>
            </div>

            <div className="space-y-1">
              <Label htmlFor="m-upstream">上游模型名（可选）</Label>
              <Input
                id="m-upstream"
                value={form.upstream_model ?? ''}
                maxLength={128}
                placeholder="留空 = 使用模型名；厂商模型如 deepseek-ai/DeepSeek-V3 填这里"
                onChange={(e) => setForm({ ...form, upstream_model: e.target.value })}
              />
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1">
                <Label htmlFor="m-timeout">超时（秒，0 = 上游默认 60）</Label>
                <Input
                  id="m-timeout"
                  type="number"
                  min={0}
                  max={600}
                  value={form.timeout_sec ?? 60}
                  onChange={(e) => setForm({ ...form, timeout_sec: e.target.value === '' ? 0 : Number(e.target.value) })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="m-retries">重试次数（0~10）</Label>
                <Input
                  id="m-retries"
                  type="number"
                  min={0}
                  max={10}
                  value={form.max_retries ?? 0}
                  onChange={(e) => setForm({ ...form, max_retries: e.target.value === '' ? 0 : Number(e.target.value) })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="m-pin">输入单价（美元/百万 token）</Label>
                <Input
                  id="m-pin"
                  type="number"
                  min={0}
                  step={0.01}
                  value={form.prompt_price_per_1m ?? 0}
                  onChange={(e) => setForm({ ...form, prompt_price_per_1m: Number(e.target.value) })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="m-pout">输出单价（美元/百万 token）</Label>
                <Input
                  id="m-pout"
                  type="number"
                  min={0}
                  step={0.01}
                  value={form.completion_price_per_1m ?? 0}
                  onChange={(e) => setForm({ ...form, completion_price_per_1m: Number(e.target.value) })}
                />
              </div>
            </div>

            <div className="space-y-1">
              <Label htmlFor="m-maxtokens">max_tokens（completion 输出上限，0 = 不设置）</Label>
              <Input
                id="m-maxtokens"
                type="number"
                min={0}
                step={1024}
                value={form.max_tokens ?? 0}
                onChange={(e) => setForm({ ...form, max_tokens: e.target.value === '' ? 0 : Number(e.target.value) })}
              />
              <p className="text-xs text-muted-foreground">
                0 = 不设置，交上游服务端默认。DeepSeek 等官方端点未设置时默认输出上限较低
                （实测 8192），大文档/长工具参数轮次会被截断，建议设为 16384 或更大；改动保存后实时生效。
              </p>
            </div>

            <label className="flex cursor-pointer items-start gap-2 rounded-lg border px-3 py-2 text-sm">
              <input
                type="checkbox"
                checked={!!form.no_thinking}
                onChange={(e) => setForm({ ...form, no_thinking: e.target.checked })}
                className="mt-0.5 size-4 shrink-0"
              />
              <span className="space-y-0.5">
                <span className="block font-medium">上游不支持思考参数（no_thinking）</span>
                <span className="block text-xs text-muted-foreground">
                  适用于 litellm custom_openai / Ollama 等标准 OpenAI 端点：转发前自动剥离
                  thinking / reasoning_effort，否则上游返回 400（UnsupportedParamsError）。
                </span>
              </span>
            </label>

            <div className="flex items-start gap-1.5 rounded-lg border border-sky-500/30 bg-sky-500/10 px-3 py-2 text-xs leading-relaxed text-sky-700 dark:text-sky-300">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>
                默认模型在列表页「设为默认」操作中转移（唯一且不可删除/禁用，始终有且仅有一个）。
                会话未显式选模型时请求落到默认模型；接入本地模型无需密钥。
              </span>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
