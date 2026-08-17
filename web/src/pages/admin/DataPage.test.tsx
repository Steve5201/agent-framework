import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import DataPage from './DataPage'
import type { DataOverview } from '@/types/api'

// mock 掉 API 与图表（jsdom 无 canvas，图表渲染不在本用例覆盖范围）
const mocks = vi.hoisted(() => ({ adminDataOverview: vi.fn() }))
vi.mock('@/lib/api', () => ({ adminDataOverview: mocks.adminDataOverview }))
vi.mock('@/components/charts/AdminChart', () => ({ default: () => null }))

/** 构造一份完整样本数据（字段对齐 /v1/admin/data/overview 契约） */
function makeOverview(): DataOverview {
  return {
    sessions: {
      days: [
        { date: '2026-08-01', sessions: 2 },
        { date: '2026-08-02', sessions: 5 },
        { date: '2026-08-03', sessions: 3 },
      ],
      agents: [
        { agent_id: 'tutor', sessions: 8 },
        { agent_id: '', sessions: 2 },
      ],
      total_sessions: 10,
    },
    usage: {
      summary: { calls: 100, success: 95, failed: 5, dau: 7, total_tokens: 5000, cost_usd: 1.23 },
      daily: [
        { date: '2026-08-01', calls: 20, success: 19, failed: 1, dau: 3, total_tokens: 1000, cost_usd: 0.2 },
        { date: '2026-08-02', calls: 50, success: 48, failed: 2, dau: 5, total_tokens: 2500, cost_usd: 0.6 },
        { date: '2026-08-03', calls: 30, success: 28, failed: 2, dau: 4, total_tokens: 1500, cost_usd: 0.43 },
      ],
      by_model: [{ key: 'deepseek', calls: 100, total_tokens: 5000, cost_usd: 1.23 }],
      by_agent: [{ key: 'tutor', calls: 90, total_tokens: 4500, cost_usd: 1.0 }],
      by_user: [{ user_id: 1, calls: 60, total_tokens: 3000, cost_usd: 0.8 }],
    },
    user_names: { '1': 'zhangsan' },
  }
}

beforeEach(() => {
  mocks.adminDataOverview.mockReset()
})

describe('DataPage 运营总览', () => {
  it('默认以 30 天窗口拉取数据，并渲染汇总卡与 Top 用户', async () => {
    mocks.adminDataOverview.mockResolvedValue(makeOverview())
    render(<DataPage />)

    expect(mocks.adminDataOverview).toHaveBeenCalledWith(30)
    // 汇总卡：今日新建会话=3、窗口内会话=10、活跃用户=7、总调用=100、成功率、成本
    expect(await screen.findByText('今日新建会话')).toBeTruthy()
    expect(screen.getByText('3')).toBeTruthy()
    expect(screen.getByText('10')).toBeTruthy()
    expect(screen.getByText('7')).toBeTruthy()
    expect(screen.getByText('100')).toBeTruthy()
    expect(screen.getByText(/成功率 95\.0%/)).toBeTruthy()
    expect(screen.getByText('$1.23')).toBeTruthy()
    // Top 用户用户名回填
    expect(screen.getByText('zhangsan')).toBeTruthy()
    expect(screen.getByText('#1')).toBeTruthy()
  })

  it('窗口切换（7/30/90）重新拉取数据', async () => {
    mocks.adminDataOverview.mockResolvedValue(makeOverview())
    const user = userEvent.setup()
    render(<DataPage />)
    await screen.findByText('今日新建会话')

    await user.click(screen.getByText('7天'))
    await waitFor(() => expect(mocks.adminDataOverview).toHaveBeenLastCalledWith(7))

    await user.click(screen.getByText('90天'))
    await waitFor(() => expect(mocks.adminDataOverview).toHaveBeenLastCalledWith(90))
  })

  it('拉取失败显示具体错误', async () => {
    mocks.adminDataOverview.mockRejectedValue(new Error('llm-gateway 用量总览失败（HTTP 500）'))
    render(<DataPage />)
    expect(await screen.findByText(/llm-gateway 用量总览失败/)).toBeTruthy()
  })
})

describe('DataPage 智能体分析', () => {
  it('切换 Tab 展示会话分布与合并排行', async () => {
    mocks.adminDataOverview.mockResolvedValue(makeOverview())
    const user = userEvent.setup()
    render(<DataPage />)
    await screen.findByText('今日新建会话')

    await user.click(screen.getByText('智能体分析'))
    expect(await screen.findByText('会话按智能体域分布')).toBeTruthy()
    // 合并排行：tutor（会话8 + 调用90）与管理端域（会话2）
    expect(screen.getByText('管理端域')).toBeTruthy()
    expect(screen.getByText('90')).toBeTruthy()
    // 排序切换到成本后 tutor 仍居首
    await user.click(screen.getByText('成本'))
    expect(screen.getByText('$1.00')).toBeTruthy()
  })
})

describe('DataPage 成本速览', () => {
  it('切换 Tab 展示成本曲线与模型占比', async () => {
    mocks.adminDataOverview.mockResolvedValue(makeOverview())
    const user = userEvent.setup()
    render(<DataPage />)
    await screen.findByText('今日新建会话')

    await user.click(screen.getByText('成本速览'))
    expect(await screen.findByText('每日成本')).toBeTruthy()
    expect(screen.getByText('成本按模型占比')).toBeTruthy()
    // 汇总卡复用：累计成本
    expect(screen.getByText('$1.23')).toBeTruthy()
    expect(screen.getByText('5.0k')).toBeTruthy() // 5000 tokens 紧凑显示
  })
})
