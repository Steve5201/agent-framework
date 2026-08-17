// ---------------------------------------------------------------------------
// 资源域状态钩子（阶段3·多租户）
//
// 独立成文件的原因：AgentScopeSelect.tsx 只导出组件，否则触发
// react-refresh/only-export-components（Fast Refresh 要求组件文件只导出组件）。
// 由资源页面（Skills / Mcp / KB）与用户页共用：
//   * super_admin（最高超管，agent 标签为 '*' 全门户标识）：资源域回退默认域
//     （tutor，可切换任意域）——'*' 是身份标识/专属门户，不是可管理的资源域，
//     直接传给后端会被 agentID 白名单拒绝（曾导致 skill/mcp/kb 报"非法的智能体 ID"）；
//   * agent_admin / admin：固定自身归属（JWT 标签），后端亦强制锁定，双保险。
// ---------------------------------------------------------------------------
import { useEffect, useState } from 'react'
import { useAuthStore } from '@/stores/auth'
import { adminListAgents } from '@/lib/api'
import { DEFAULT_AGENT_ID, isSuperAdmin, getUserAgentId, isAllAgentScope } from '@/lib/roles'
import type { Agent } from '@/types/api'

export interface AgentScope {
  /** 当前资源域（super_admin 可切换，其它管理员固定） */
  agentId: string
  /** 是否允许切换（仅最高超管） */
  canScope: boolean
  setAgentId: (id: string) => void
  /** 智能体候选列表（仅超管拉取；失败时为空但功能不受影响） */
  agents: Agent[]
}

/** 超管的全门户标识 '*' 仅用于聊天域切换；管理端资源必须落在具体智能体域。 */
function scopeOf(user: ReturnType<typeof useAuthStore.getState>['user']): string {
  const id = getUserAgentId(user)
  return isAllAgentScope(id) ? DEFAULT_AGENT_ID : id
}

/** 资源域状态钩子：解析 + 管理当前资源域。
 *  传入 fixedAgentId 时锁定该域（用于智能体详情页内嵌资源管理，
 *  不渲染切换下拉、不可切换）；缺省按登录身份解析。 */
export function useAgentScope(fixedAgentId?: string): AgentScope {
  const user = useAuthStore((s) => s.user)
  const canScope = isSuperAdmin(user?.role) && !fixedAgentId

  const [agentId, setAgentId] = useState(() => fixedAgentId ?? scopeOf(user))
  const [agents, setAgents] = useState<Agent[]>([])

  // 登录态就绪后（hydrate 完成），非超管固定自身归属；超管回退默认域；
  // 固定域模式下不随用户变化覆盖（详情页内嵌场景）。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 需随 user 变化同步资源域
    if (user && !fixedAgentId) setAgentId(scopeOf(user))
  }, [user, fixedAgentId])

  // 仅超管需要智能体候选列表（下拉）。非超管清空，防止跨会话残留。
  useEffect(() => {
    if (!canScope) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- 角色降级时清空候选列表
      setAgents([])
      return
    }
    let cancelled = false
    adminListAgents()
      .then((list) => {
        if (cancelled) return
        setAgents(list)
      })
      .catch(() => {
        /* 下拉留空：超管仍可用默认域操作，不阻塞页面 */
      })
    return () => {
      cancelled = true
    }
  }, [canScope])

  return { agentId, canScope, setAgentId, agents }
}
