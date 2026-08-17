// ---------------------------------------------------------------------------
// 服务器地址设置（P2-I 补充）
//
// 默认连本机 gateway（http://localhost:8080），可在登录页修改并持久化到
// localStorage（Tauri 的 WebView2 同样持久）。构建部署时可用
// VITE_API_BASE_URL 覆盖"初始默认值"。
// 保存后立即生效：api.ts / sse.ts 每次请求动态读取，无需刷新页面。
// ---------------------------------------------------------------------------

const SERVER_URL_KEY = 'agent.server_url'

/** 初始默认值：构建变量 > localhost:8080（后期部署改构建变量即可换默认）。 */
export const DEFAULT_SERVER_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'

/** 当前生效的服务器地址（用户自填优先，其次默认值）。 */
export function getServerUrl(): string {
  if (typeof window === 'undefined') return DEFAULT_SERVER_URL
  return localStorage.getItem(SERVER_URL_KEY) || DEFAULT_SERVER_URL
}

/** 保存用户自填的服务器地址。需以 http:// 或 https:// 开头（自动去尾部斜杠），非法抛错。 */
export function setServerUrl(url: string): void {
  const trimmed = url.trim().replace(/\/+$/, '')
  if (!/^https?:\/\//i.test(trimmed)) {
    throw new Error('服务器地址需以 http:// 或 https:// 开头')
  }
  localStorage.setItem(SERVER_URL_KEY, trimmed)
}
