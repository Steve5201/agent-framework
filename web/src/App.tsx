import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import LoginPage from '@/pages/LoginPage'
import PortalConfigPage from '@/pages/PortalConfigPage'
import ChatPage from '@/pages/ChatPage'
import AdminGuard, { RoleGuard } from '@/components/AdminGuard'
import AdminLayout from '@/pages/admin/AdminLayout'
import SkillsPage from '@/pages/admin/SkillsPage'
import McpPage from '@/pages/admin/McpPage'
import KnowledgeBasePage from '@/pages/admin/KnowledgeBasePage'
import LogsPage from '@/pages/admin/LogsPage'
import AgentsPage from '@/pages/admin/AgentsPage'
import AgentDetailPage from '@/pages/admin/AgentDetailPage'
import UsersPage from '@/pages/admin/UsersPage'
import ModelsPage from '@/pages/admin/ModelsPage'
import DataPage from '@/pages/admin/DataPage'
import { isAdminRole, isSuperAdmin, canManageUsers, getHomeScope } from '@/lib/roles'
import { getPortalAgentId } from '@/lib/portal'
import { isTauri } from '@/lib/storage'
import { useAuthStore } from '@/stores/auth'

/** 默认智能体域 ID（未指定时落地到该智能体；智能体注册表见阶段3）。 */
export const DEFAULT_AGENT_ID = 'tutor'

/**
 * 根路径兜底：按登录态分流。
 *  - 管理员 → 角色归属会话域（超管 /agent/*、其它管理员 /agent/{绑定域}，
 *    而不是 /admin/chat——后者会话列表只含管理端域，与管理员实际会话归属
 *    脱节，曾导致"登录后首屏空列表，需手动切 * 域才出现"）
 *  - 桌面端首次运行（Tauri 且未配置门户）→ 门户配置页 /portal
 *  - 其它（普通用户/游客）→ 默认智能体 /agent/tutor
 */
function HomeRedirect() {
  const status = useAuthStore((s) => s.status)
  const user = useAuthStore((s) => s.user)
  if (status === 'loading') {
    return <div className="flex h-full items-center justify-center text-sm text-muted-foreground">加载中…</div>
  }
  if (status === 'authed' && isAdminRole(user?.role)) {
    return <Navigate to={`/agent/${getHomeScope(user)}`} replace />
  }
  // 桌面端没有地址栏：未配置门户时强制先到门户配置页，配置后进对应门户
  if (isTauri()) {
    const portal = getPortalAgentId()
    if (!portal) return <Navigate to="/portal" replace />
    return <Navigate to={`/agent/${portal}`} replace />
  }
  return <Navigate to={`/agent/${DEFAULT_AGENT_ID}`} replace />
}

/** 无门户参数登录页兜底：重定向到默认智能体门户登录页（门户化后登录页必须带 agentId）。 */
function LoginRedirect() {
  return <Navigate to={`/login/${DEFAULT_AGENT_ID}`} replace />
}

/**
 * 应用外壳：
 *  - 启动时恢复登录态（hydrate）
 *  - 路由（阶段2·独立地址 + 游客模式）：
 *      /login、/login/:agentId（公开，登录页）
 *      /agent/:agentId（公开，智能体聊天域，未登录为游客）
 *      /admin/chat（仅管理员，管理端对话域）
 *      /admin/*（仅管理员，管理端模块）
 *      /（兜底分流：管理员 → /admin/chat；其它 → /agent/tutor）
 *  - 管理端模块页：skills/mcp/kb 已实现，agents/users/data 走占位页
 */
export default function App() {
  const hydrate = useAuthStore((s) => s.hydrate)

  useEffect(() => {
    void hydrate().finally(() => {
      // 登录态最终确认后通知页面兜底刷新会话列表：桌面端重开时 persist
      // 恢复的 user/status 早于 Tauri 异步 token 就绪，挂载时的初始列表
      // 可能不准确（游客视角/空列表），需在 hydrate 完成后重拉一次。
      window.dispatchEvent(new Event('agent:auth-hydrated'))
    })
  }, [hydrate])

  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginRedirect />} />
        <Route path="/login/:agentId" element={<LoginPage />} />

        {/* 门户配置页：桌面端首次运行落地页 + 侧栏常驻切换按钮入口（浏览器不隐藏路由） */}
        <Route path="/portal" element={<PortalConfigPage />} />

        {/* 智能体聊天域：公开，未登录为游客模式 */}
        <Route path="/agent/:agentId" element={<ChatPage mode="agent" />} />

        {/* 管理端对话域：仅管理员 */}
        <Route
          path="/admin/chat"
          element={
            <AdminGuard>
              <ChatPage mode="admin" />
            </AdminGuard>
          }
        />

        {/* 管理端模块 */}
        <Route path="/admin" element={<AdminGuard />}>
          <Route element={<AdminLayout />}>
            <Route path="skills" element={<SkillsPage />} />
            <Route path="mcp" element={<McpPage />} />
            <Route path="kb" element={<KnowledgeBasePage />} />
            <Route path="logs" element={<LogsPage />} />
            {/* 智能体管理：仅最高超管 */}
            <Route
              path="agents"
              element={
                <RoleGuard allow={isSuperAdmin}>
                  <AgentsPage />
                </RoleGuard>
              }
            />
            <Route
              path="agents/:id"
              element={
                <RoleGuard allow={isSuperAdmin}>
                  <AgentDetailPage />
                </RoleGuard>
              }
            />
            {/* 用户管理：super_admin + agent_admin */}
            <Route
              path="users"
              element={
                <RoleGuard allow={canManageUsers}>
                  <UsersPage />
                </RoleGuard>
              }
            />
            {/* 大模型管理：super_admin + agent_admin（API Key 只存 llm-gateway） */}
            <Route
              path="models"
              element={
                <RoleGuard allow={canManageUsers}>
                  <ModelsPage />
                </RoleGuard>
              }
            />
            {/* 数据管理：仅最高超管（后端模块清单亦对其它角色隐藏） */}
            <Route
              path="data"
              element={
                <RoleGuard allow={isSuperAdmin}>
                  <DataPage />
                </RoleGuard>
              }
            />
            <Route index element={<Navigate to="agents" replace />} />
          </Route>
        </Route>

        <Route path="*" element={<HomeRedirect />} />
      </Routes>
    </BrowserRouter>
  )
}
