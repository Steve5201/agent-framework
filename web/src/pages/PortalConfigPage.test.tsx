import { describe, it, expect, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import PortalConfigPage from './PortalConfigPage'

const renderPage = () =>
  render(
    <MemoryRouter initialEntries={['/portal']}>
      <Routes>
        <Route path="/portal" element={<PortalConfigPage />} />
        <Route path="*" element={<div />} />
      </Routes>
    </MemoryRouter>,
  )

describe('PortalConfigPage', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('渲染门户配置表单', () => {
    renderPage()
    expect(screen.getByLabelText('智能体 ID')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '进入门户' })).toBeInTheDocument()
  })

  it('空 ID 提交给出具体错误提示', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: '进入门户' }))
    expect(screen.getByRole('alert')).toHaveTextContent('请输入智能体 ID')
  })

  it('非法 ID（含特殊字符）给出具体错误提示', () => {
    renderPage()
    fireEvent.change(screen.getByLabelText('智能体 ID'), { target: { value: 'bad id!' } })
    fireEvent.click(screen.getByRole('button', { name: '进入门户' }))
    expect(screen.getByRole('alert')).toHaveTextContent('非法的智能体 ID')
  })

  it('合法 ID 保存到 localStorage 并跳转对应门户', () => {
    renderPage()
    fireEvent.change(screen.getByLabelText('智能体 ID'), { target: { value: 'math' } })
    fireEvent.click(screen.getByRole('button', { name: '进入门户' }))
    expect(localStorage.getItem('agent.portal_agent')).toBe('math')
  })

  it('超管可配置 * 全门户标识', () => {
    renderPage()
    fireEvent.change(screen.getByLabelText('智能体 ID'), { target: { value: '*' } })
    fireEvent.click(screen.getByRole('button', { name: '进入门户' }))
    expect(localStorage.getItem('agent.portal_agent')).toBe('*')
  })

  it('服务器地址设置区可见且可保存', () => {
    renderPage()
    const input = screen.getByLabelText('服务器地址')
    fireEvent.change(input, { target: { value: 'http://10.0.0.2:8080' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(screen.getByText('已保存，立即生效')).toBeInTheDocument()
    expect(localStorage.getItem('agent.server_url')).toBe('http://10.0.0.2:8080')
  })
})
