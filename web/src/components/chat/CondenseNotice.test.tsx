// CondenseNotice.test.tsx —— 上下文压缩提示条渲染单测。
//
// 覆盖：收纳条数/累计次数文案、图标存在、悬浮提示 title 完整信息。
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import CondenseNotice from './CondenseNotice'

describe('CondenseNotice', () => {
  it('渲染收纳条数与累计次数', () => {
    render(<CondenseNotice info={{ dropped: 5, count: 2 }} />)
    const notice = screen.getByTestId('condense-notice')
    expect(notice).toBeTruthy()
    expect(notice.textContent).toContain('收纳 5 条早期消息')
    expect(notice.textContent).toContain('累计第 2 次')
  })

  it('首轮压缩（count=1）文案一致', () => {
    render(<CondenseNotice info={{ dropped: 3, count: 1 }} />)
    expect(screen.getByTestId('condense-notice').textContent).toContain('收纳 3 条早期消息')
    expect(screen.getByTestId('condense-notice').textContent).toContain('累计第 1 次')
  })

  it('提供完整悬浮说明（title）', () => {
    render(<CondenseNotice info={{ dropped: 10, count: 4 }} />)
    const notice = screen.getByTestId('condense-notice')
    expect(notice.getAttribute('title')).toBe('上下文压缩记录：收纳 10 条早期消息，会话累计第 4 次压缩')
  })
})
