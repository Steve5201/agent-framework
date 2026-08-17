import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { User } from '@/types/api'
import { clearTokens, getRefreshToken, setTokens } from '@/lib/storage'
import { fetchMe, logout as apiLogout } from '@/lib/api'

export type AuthStatus = 'loading' | 'authed' | 'guest'

interface AuthState {
  user: User | null
  status: AuthStatus
  /** 登录/刷新成功后写入 token 与用户信息 */
  applySession: (access: string, refresh: string, user: User) => Promise<void>
  setUser: (user: User) => void
  /** 登出：尽力吊销 refresh，随后清空本地 */
  logout: () => Promise<void>
  /** 应用启动时恢复登录态：有 refresh → 拉 /me 校验 */
  hydrate: () => Promise<void>
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      user: null,
      status: 'loading',

      applySession: async (access, refresh, user) => {
        await setTokens(access, refresh)
        set({ user, status: 'authed' })
      },

      setUser: (user) => set({ user }),

      logout: async () => {
        try {
          const refresh = await getRefreshToken()
          if (refresh) {
            try {
              await apiLogout(refresh) // 吊销整个 refresh 族
            } catch {
              // 吊销失败也继续清本地，避免卡死在登出
            }
          }
        } catch {
          // 存储读取失败也继续清理本地
        }
        await clearTokens().catch(() => {})
        set({ user: null, status: 'guest' })
      },

      hydrate: async () => {
        set({ status: 'loading' })
        try {
          const refresh = await getRefreshToken()
          if (!refresh) {
            set({ user: null, status: 'guest' })
            return
          }
          const me = await fetchMe()
          set({ user: me, status: 'authed' })
        } catch {
          await clearTokens().catch(() => {}) // token 已失效
          set({ user: null, status: 'guest' })
        }
      },
    }),
    {
      name: 'agent.auth',
      // token 由 lib/storage 单独管理，这里只持久化 user 与登录态
      partialize: (s) => ({ user: s.user, status: s.status === 'authed' ? 'authed' : 'guest' }),
    },
  ),
)
