// ---------------------------------------------------------------------------
// 门户配置（阶段3·多租户桌面端）
// ---------------------------------------------------------------------------
// 桌面端没有地址栏，需要显式配置要连接的智能体门户（localStorage 持久化）。
//  - 未配置：桌面端首次运行落在门户配置页；
//  - 已配置：启动后直接进入对应门户，与浏览器端访问 /agent/:agentId 行为一致；
//  - 浏览器端无此配置入口（地址即门户），但接口不隐藏，浏览器手动访问
//    /portal 仍可使用（保证程序路径统一）。
// ---------------------------------------------------------------------------

const PORTAL_KEY = 'agent.portal_agent'

/** 读取已配置的门户 ID；未配置返回空串（桌面端应引导去配置页）。 */
export function getPortalAgentId(): string {
  try {
    return localStorage.getItem(PORTAL_KEY) ?? ''
  } catch {
    return ''
  }
}

/** 保存门户配置。agentId 为空视为清除配置（回到门户配置页）。 */
export function setPortalAgentId(agentId: string): void {
  const id = agentId.trim()
  try {
    if (id) {
      localStorage.setItem(PORTAL_KEY, id)
    } else {
      localStorage.removeItem(PORTAL_KEY)
    }
  } catch {
    /* 隐私模式等场景忽略 */
  }
}

/** 是否已配置门户（桌面端首次运行判定用）。 */
export function hasPortalAgentId(): boolean {
  return getPortalAgentId() !== ''
}
