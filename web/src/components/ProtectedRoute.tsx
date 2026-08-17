import { Navigate, Outlet } from 'react-router-dom'
import { useEffect } from 'react'
import { AUTH_EXPIRED_EVENT } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'

/**
 * 路由守卫：未登录 → /login；加载中 → 占位。
 * 同时监听"refresh 彻底失效"事件，强制登出回登录页。
 */
export default function ProtectedRoute() {
  const status = useAuthStore((s) => s.status)
  const logout = useAuthStore((s) => s.logout)

  useEffect(() => {
    const onExpired = () => void logout()
    window.addEventListener(AUTH_EXPIRED_EVENT, onExpired)
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, onExpired)
  }, [logout])

  if (status === 'loading') {
    return (
      <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
        正在恢复登录态…
      </div>
    )
  }
  if (status !== 'authed') {
    return <Navigate to="/login" replace />
  }
  return <Outlet />
}
