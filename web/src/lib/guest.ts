/**
 * 游客身份（阶段2·游客模式）。
 *
 * 未登录访问智能体聊天页时，前端生成一个 UUID 作为"游客 ID"（仅存
 * localStorage），随后所有无访问令牌的请求都带上 `X-Guest-ID` 头。
 * 服务端据此派生出稳定的负整数 user_id（见 backend auth.GuestUserID），
 * 游客的会话/消息即归属该命名空间，同一次浏览器会话内身份稳定。
 *
 * 登录成功后由登录页调用 mergeGuestSessions 把游客会话合并到账号，
 * 随后清除本 ID（避免后续以游客身份混入账号数据）。
 */
import { genUuid } from './uuid'

const GUEST_KEY = 'agent.guest_id'

/** 读取本地游客 ID；不存在则生成并持久化。存储不可用时返回 null。 */
export function getGuestId(): string | null {
  try {
    let id = localStorage.getItem(GUEST_KEY)
    if (!id) {
      id = genUuid()
      localStorage.setItem(GUEST_KEY, id)
    }
    return id
  } catch {
    return null
  }
}

/** 是否存在本地游客 ID（登录合并前判断是否有游客会话可迁移）。 */
export function hasGuestId(): boolean {
  try {
    return localStorage.getItem(GUEST_KEY) !== null
  } catch {
    return false
  }
}

/** 登录合并完成后清除游客 ID，避免后续以游客身份访问。 */
export function clearGuestId(): void {
  try {
    localStorage.removeItem(GUEST_KEY)
  } catch {
    // 存储不可用时忽略
  }
}
