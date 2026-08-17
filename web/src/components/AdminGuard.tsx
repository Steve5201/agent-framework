import type { ReactNode } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { useAuthStore } from '@/stores/auth'
import { isAdminRole } from '@/lib/roles'

/**
 * 管理端路由守卫：仅管理员角色可进入 /admin/*；
 * 非管理员一律重定向回对话页（防误入 + 深链保护）。
 * 同时支持两种用法：
 *  - 布局守卫：<Route element={<AdminGuard />}>（渲染 Outlet）
 *  - 单页守卫：<AdminGuard><ChatPage /></AdminGuard>（渲染 children）
 */
export default function AdminGuard({ children }: { children?: ReactNode }) {
  const user = useAuthStore((s) => s.user)
  const status = useAuthStore((s) => s.status)
  const location = useLocation()

  if (status === 'loading') {
    // 登录态恢复中，先空渲染（很快）
    return null
  }
  if (!isAdminRole(user?.role)) {
    return <Navigate to="/" replace state={{ from: location }} />
  }
  return children ?? <Outlet />
}

/**
 * 细分角色守卫：在 AdminGuard 之上追加角色要求（阶段3·管理员分层）。
 *  - agents / data：仅 super_admin（isSuperAdmin）
 *  - users：super_admin / agent_admin（canManageUsers）
 * 不满足时重定向到管理端默认模块（skills）。
 */
export function RoleGuard({
  allow,
  children,
}: {
  allow: (role?: string) => boolean
  children?: ReactNode
}) {
  const user = useAuthStore((s) => s.user)
  const status = useAuthStore((s) => s.status)
  const location = useLocation()

  if (status === 'loading') {
    return null
  }
  if (!isAdminRole(user?.role)) {
    return <Navigate to="/" replace state={{ from: location }} />
  }
  if (!allow(user?.role)) {
    return <Navigate to="/admin/skills" replace state={{ from: location }} />
  }
  return children ?? <Outlet />
}
