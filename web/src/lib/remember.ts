// ---------------------------------------------------------------------------
// "记住密码"跨端统一实现（localStorage，按智能体域隔离）
//
// 说明（2026-08-10 域隔离）：
//   - 原实现单一 key（agent.remembered_credentials），不同门户共享一份凭据，
//     切换智能体后要么复用错误域的账号密码、要么覆盖。现按域隔离：
//     key = `agent.remembered_credentials.<agentId>`，不同门户各自保存。
//   - 兼容迁移：load 时当前域无数据、但旧单域 key 有数据时，一次性迁移到
//     当前域并清理旧 key（此前用旧实现保存过的账号密码不丢失）。
//   - 存储用 base64 仅做混淆，**非加密**——localStorage 有 XSS 泄露风险，
//     仅建议在本机可信环境使用"记住密码"。
// ---------------------------------------------------------------------------

export interface RememberedCredentials {
  username: string
  password: string
}

const LS_KEY_PREFIX = 'agent.remembered_credentials'
/** 旧版单域 key：兼容迁移用（v1 无域隔离时写入） */
const LEGACY_KEY = LS_KEY_PREFIX

function lsKey(agentId: string): string {
  // agentId 允许包含 '*'（超管门户）；前缀固定，避免与其它数据冲突
  return `${LS_KEY_PREFIX}.${agentId}`
}

function parse(raw: string): RememberedCredentials | null {
  try {
    const v = JSON.parse(atob(raw)) as RememberedCredentials
    return v?.username && v.password ? v : null
  } catch {
    return null
  }
}

function lsSave(agentId: string, creds: RememberedCredentials): void {
  localStorage.setItem(lsKey(agentId), btoa(JSON.stringify(creds)))
}

function lsLoad(agentId: string): RememberedCredentials | null {
  try {
    const raw = localStorage.getItem(lsKey(agentId))
    if (raw) return parse(raw)
    // 兼容旧单域 key：有数据则迁移到当前域并清理（一次性）
    const legacy = localStorage.getItem(LEGACY_KEY)
    if (legacy) {
      const v = parse(legacy)
      if (v) {
        localStorage.setItem(lsKey(agentId), legacy)
        localStorage.removeItem(LEGACY_KEY)
        return v
      }
      localStorage.removeItem(LEGACY_KEY)
    }
    return null
  } catch {
    return null
  }
}

function lsClear(agentId: string): void {
  try {
    localStorage.removeItem(lsKey(agentId))
  } catch {
    /* 隐私模式等场景忽略 */
  }
}

/** 保存指定智能体域的记住凭据（空用户名/密码不落盘）。 */
export async function saveRemembered(agentId: string, username: string, password: string): Promise<void> {
  if (!username.trim() || !password) return
  lsSave(agentId, { username, password })
}

/** 读取指定智能体域记住的凭据；未保存 / 数据损坏返回 null（不阻塞登录）。 */
export async function loadRemembered(agentId: string): Promise<RememberedCredentials | null> {
  return lsLoad(agentId)
}

/** 清除指定智能体域记住的凭据（取消勾选 / 用户主动清除时调用）。 */
export async function clearRemembered(agentId: string): Promise<void> {
  lsClear(agentId)
}
