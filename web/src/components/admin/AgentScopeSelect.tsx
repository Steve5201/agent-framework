// ---------------------------------------------------------------------------
// 资源域下拉选择器（阶段3·多租户）
//
// 仅最高超管（super_admin）渲染：从 /v1/admin/agents 取智能体候选列表，
// 切换管理的资源域（技能 / MCP / 知识库按智能体隔离）。
// agent_admin / admin 的资源域由账号归属固定，页面展示提示条即可。
// 注意：本文件只导出组件（Fast Refresh 约束）；资源域状态钩子见
// ./useAgentScope.ts，由资源页面调用。
// ---------------------------------------------------------------------------
import { useAuthStore } from '@/stores/auth'
import { isSuperAdmin } from '@/lib/roles'
import type { Agent } from '@/types/api'

/** 资源域下拉选择器：仅最高超管渲染；agent_admin/admin 由页面展示固定归属。 */
export function AgentScopeSelect({
  agentId,
  agents,
  onChange,
}: {
  agentId: string
  agents: Agent[]
  onChange: (id: string) => void
}) {
  const role = useAuthStore((s) => s.user?.role)
  if (!isSuperAdmin(role)) return null

  return (
    <label className="flex items-center gap-2 text-xs text-muted-foreground">
      <span className="font-medium">智能体域</span>
      <select
        value={agentId}
        onChange={(e) => onChange(e.target.value)}
        aria-label="智能体域"
        title="切换管理的智能体资源域"
        className="h-8 max-w-56 rounded-md border bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
      >
        {agents.length === 0 && <option value={agentId}>{agentId}</option>}
        {agents.map((a) => (
          <option key={a.id} value={a.id}>
            {a.name}（{a.id}）
          </option>
        ))}
      </select>
    </label>
  )
}

export default AgentScopeSelect
