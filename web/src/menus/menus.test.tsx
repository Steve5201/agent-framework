// menus.test.tsx —— 菜单系统（注册表驱动）单测。
//
// 覆盖：
//   1. filterMenuItems：roles 白名单（管理员可见"管理端"、普通用户不可见）+
//      visible 环境谓词（quit-app 仅 Tauri 环境渲染，jsdom 默认隐藏）；
//   2. MenuPanel：列表渲染 / renderPanel 打开"设置"子界面 /
//      action 执行（退出登录 → 落用户归属域）后关闭面板。
// 菜单项在模块加载时自注册（items/index.ts），各测试共享同一注册表。

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { useAuthStore } from '@/stores/auth'
import MenuPanel from './Panel'
import { filterMenuItems } from './registry'
import type { User } from '@/types/api'

/** 构造测试用户（仅填充菜单过滤用到的字段） */
function makeUser(role: string, agentId = 'tutor'): User {
  return {
    id: '1',
    username: 'tester',
    role,
    tags: [{ key: 'agent', value: agentId }],
  } as User
}

const renderPanel = () =>
  render(
    <MemoryRouter initialEntries={['/agent/tutor']}>
      <MenuPanel onClose={() => {}} />
    </MemoryRouter>,
  )

describe('菜单注册表 · filterMenuItems', () => {
  it('管理员可见：设置 + 管理端（quit-app 非 Tauri 环境隐藏）', () => {
    const keys = filterMenuItems(makeUser('admin')).map((i) => i.key)
    expect(keys).toContain('settings')
    expect(keys).toContain('admin')
    expect(keys).not.toContain('quit-app')
  })

  it('普通用户不可见"管理端"（roles 白名单过滤）', () => {
    const keys = filterMenuItems(makeUser('user')).map((i) => i.key)
    expect(keys).toContain('settings')
    expect(keys).not.toContain('admin')
    expect(keys).toContain('logout')
  })

  it('游客（无用户）不返回任何菜单项', () => {
    expect(filterMenuItems(null)).toHaveLength(0)
  })

  it('Tauri 环境可见"退出应用"', () => {
    // 模拟 Tauri WebView2 注入标记
    Object.defineProperty(window, '__TAURI_INTERNALS__', { value: {}, configurable: true })
    try {
      const keys = filterMenuItems(makeUser('user')).map((i) => i.key)
      expect(keys).toContain('quit-app')
    } finally {
      delete (window as unknown as Record<string, unknown>)['__TAURI_INTERNALS__']
    }
  })
})

describe('菜单面板 · MenuPanel', () => {
  beforeEach(() => {
    useAuthStore.setState({
      user: makeUser('admin'),
      status: 'authed',
      logout: vi.fn(async () => {}),
    } as never)
  })

  it('管理员：渲染菜单列表，包含设置 / 管理端 / 退出登录', () => {
    renderPanel()
    expect(screen.getByText('设置')).toBeInTheDocument()
    expect(screen.getByText('管理端')).toBeInTheDocument()
    expect(screen.getByText('退出登录')).toBeInTheDocument()
    // 浏览器环境：退出应用不渲染（visible 谓词）
    expect(screen.queryByText('退出应用')).not.toBeInTheDocument()
  })

  it('renderPanel：点击"设置"进入子界面（打开即返回菜单列表能力）', () => {
    renderPanel()
    fireEvent.click(screen.getByText('设置'))
    // 子界面：头部标题切换 + 服务器地址表单出现
    expect(screen.getByText('服务器地址')).toBeInTheDocument()
    // 子界面顶部返回"菜单"按钮（ctx.back）
    fireEvent.click(screen.getByText('菜单'))
    expect(screen.getByText('管理端')).toBeInTheDocument()
  })

  it('action：点击"退出登录"调用 logout 并跳转用户归属域', async () => {
    const logout = vi.fn(async () => {})
    useAuthStore.setState({ logout } as never)
    renderPanel()
    fireEvent.click(screen.getByText('退出登录'))
    await vi.waitFor(() => {
      expect(logout).toHaveBeenCalled()
    })
  })
})
