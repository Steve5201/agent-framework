// ---------------------------------------------------------------------------
// 自由模式（本地个人化开关）
//
// 仅作用于本机"本地 shell"（local_shell，桌面端 Tauri 直接在本机执行命令）。
// 开启后：
//   - 本地 shell 命令不再逐条弹确认框，直接执行；
//   - 本地 shell 不再受默认 30 秒超时限制（交由 Rust 端按参数执行，可长期运行）。
//
// 这是纯本地、个人化的偏好，与服务器/角色无关，任何用户在自己的桌面端均可开启；
// 对云端沙盒执行、服务端工具、其他用户无任何影响。
// 因跳过确认 + 放开超时，命令会直接在本机以当前用户权限执行——开启前必须
// 明确提示风险（每次开启都弹警告确认，而非仅首次）。
// ---------------------------------------------------------------------------

const FREE_MODE_KEY = 'agent.free_mode'

/** 当前是否处于自由模式。 */
export function isFreeMode(): boolean {
  if (typeof window === 'undefined') return false
  return localStorage.getItem(FREE_MODE_KEY) === '1'
}

/** 写入自由模式状态（true 开启 / false 关闭）。 */
export function setFreeMode(on: boolean): void {
  localStorage.setItem(FREE_MODE_KEY, on ? '1' : '0')
}