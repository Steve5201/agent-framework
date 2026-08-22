// ---------------------------------------------------------------------------
// token 安全存储（双后端，统一异步接口）
//
// 浏览器环境（web 网页 / vitest）：localStorage
//   —— 有 XSS 读取风险，但 access 15 分钟短过期 + refresh 可吊销，风险面可控。
// Tauri 桌面环境（WebView2）：自定义 Rust 命令（desktop/src-tauri/src/commands.rs）
//   —— token 持久化到系统应用配置目录（%APPDATA%/com.nebula.agent/session.json），
//      与 WebView2 数据目录隔离，浏览器 JS/其他站点无法访问（P2-86）。
//      明文落盘，威胁模型与 tauri-plugin-store 等价；防本机进程读取需升级
//      Windows Credential Manager（keyring），列入后续硬化项。
//
// 调用方统一 await；非 Tauri 环境内部走同步的 localStorage，行为与之前一致。
// ---------------------------------------------------------------------------

const ACCESS_KEY = 'agent.access_token'
const REFRESH_KEY = 'agent.refresh_token'

/** 是否运行在 Tauri（WebView2）环境。Tauri 会向 window 注入内部标记。 */
export function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
}

type TokenPair = { access_token: string; refresh_token: string }

/** Tauri 后端命令封装（对应 Rust 侧 tokens_get / tokens_set / tokens_clear）。 */
type TauriTokenBackend = {
  get: () => Promise<TokenPair | null>
  set: (tokens: TokenPair) => Promise<void>
  clear: () => Promise<void>
}

let backendPromise: Promise<TauriTokenBackend | null> | null = null

/**
 * 惰性初始化 Tauri 后端（首次调用才动态 import @tauri-apps/api，浏览器包不臃肿）。
 * 初始化失败（如未在 Tauri 环境 / IPC 异常）时返回 null，调用方回退 localStorage。
 */
function getBackend(): Promise<TauriTokenBackend | null> {
  if (!isTauri()) return Promise.resolve(null)
  backendPromise ??= import('@tauri-apps/api/core')
    .then(({ invoke }) => ({
      get: async (): Promise<TokenPair | null> => invoke('tokens_get'),
      set: async (tokens: TokenPair): Promise<void> =>
        invoke('tokens_set', { tokens }),
      clear: async (): Promise<void> => invoke('tokens_clear'),
    }))
    .catch((err) => {
      console.error('[storage] Tauri 后端初始化失败，回退 localStorage：', err)
      return null
    })
  return backendPromise
}

export async function getAccessToken(): Promise<string | null> {
  const backend = await getBackend()
  if (backend) return (await backend.get())?.access_token ?? null
  return localStorage.getItem(ACCESS_KEY)
}

export async function getRefreshToken(): Promise<string | null> {
  const backend = await getBackend()
  if (backend) return (await backend.get())?.refresh_token ?? null
  return localStorage.getItem(REFRESH_KEY)
}

export async function setTokens(access: string, refresh: string): Promise<void> {
  const backend = await getBackend()
  if (backend) {
    await backend.set({ access_token: access, refresh_token: refresh })
    return
  }
  localStorage.setItem(ACCESS_KEY, access)
  localStorage.setItem(REFRESH_KEY, refresh)
}

export async function clearTokens(): Promise<void> {
  const backend = await getBackend()
  if (backend) {
    await backend.clear()
    return
  }
  localStorage.removeItem(ACCESS_KEY)
  localStorage.removeItem(REFRESH_KEY)
}
