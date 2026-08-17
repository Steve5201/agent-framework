// chat.fromHistory.test.ts —— 历史消息 → 前端消息模型的转换单测。
//
// 重点覆盖 P4-I 新增行为：
//  1. 编排过程摘要（system 消息，__orch_v1__ 前缀）解析为编排任务块，
//     附到同轮 assistant 消息上渲染（历史回放编排过程）；
//  2. 非编排 system 消息仍被跳过；
//  3. 工具过程合并与空气泡过滤保持原有行为。
import { describe, it, expect } from 'vitest'
import { fromHistory } from '@/stores/chat'
import type { HistoryMessage } from '@/types/api'

/** 构造一条历史消息的便捷函数（缺省字段补空）。 */
function h(partial: Partial<HistoryMessage> & { role: string; content: string }): HistoryMessage {
  return {
    id: '',
    reasoning: '',
    tool_call_id: '',
    tool_calls: '',
    round_no: 0,
    version: 0,
    total_versions: 1,
    ...partial,
  }
}

const ORCH_SUMMARY = `__orch_v1__${JSON.stringify({
  v: 1,
  tasks: [
    { id: 'research', role: 'research', status: 'completed', tokens: 2208, duration_ms: 1000 },
    { id: 'outline', role: 'outline', status: 'completed', tokens: 5628, duration_ms: 2000 },
    { id: 'content', role: 'content', status: 'failed', error: 'agent: LLM 调用失败: llm: HTTP 504, 上游模型服务响应超时', tokens: 0, duration_ms: 3000 },
    { id: 'review', role: 'review', status: 'skipped', tokens: 0, duration_ms: 0 },
  ],
})}`

/** 含子任务输出（output）的编排摘要：历史回看渲染用（P4-M）。 */
const ORCH_SUMMARY_WITH_OUTPUT = `__orch_v1__${JSON.stringify({
  v: 1,
  tasks: [
    { id: 'research', role: 'research', status: 'completed', output: '要点式摘要…', tokens: 10, duration_ms: 100 },
    { id: 'content', role: 'content', status: 'completed', output: '教案正文…', tokens: 20, duration_ms: 200 },
  ],
})}`

describe('fromHistory', () => {
  it('普通对话：user + assistant 合并为一条消息', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: '你好', round_no: 1 }),
      h({ id: '2', role: 'assistant', content: '你好！', round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(2)
    expect(out[0]).toMatchObject({ role: 'user', content: '你好' })
    expect(out[1]).toMatchObject({ role: 'assistant', content: '你好！' })
    expect(out[1].tasks).toBeUndefined()
  })

  it('编排轮：system 摘要解析为任务块附到同轮 assistant', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: '生成教案', round_no: 1 }),
      h({ id: '2', role: 'system', content: ORCH_SUMMARY, round_no: 1 }),
      h({ id: '3', role: 'assistant', content: '教案完成', round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(2) // system 摘要不单独成条
    const bot = out[1]
    expect(bot.content).toBe('教案完成')
    expect(bot.tasks).toHaveLength(4)
    expect(bot.tasks![0]).toMatchObject({ id: 'research', status: 'completed', totalTokens: 2208 })
    expect(bot.tasks![2]).toMatchObject({
      id: 'content',
      status: 'failed',
      error: expect.stringContaining('HTTP 504'),
    })
    expect(bot.tasks![3]).toMatchObject({ id: 'review', status: 'skipped' })
  })

  it('编排轮：system 摘要含 output 时解析为子任务内容（历史回看，P4-M）', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: '生成教案', round_no: 1 }),
      h({ id: '2', role: 'system', content: ORCH_SUMMARY_WITH_OUTPUT, round_no: 1 }),
      h({ id: '3', role: 'assistant', content: '教案完成', round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(2)
    const bot = out[1]
    expect(bot.tasks).toHaveLength(2)
    expect(bot.tasks![0]).toMatchObject({ id: 'research', status: 'completed', content: '要点式摘要…' })
    expect(bot.tasks![1]).toMatchObject({ id: 'content', status: 'completed', content: '教案正文…' })
  })

  it('非编排 system 消息（无 __orch_v1__ 前缀）被跳过且不影响正文', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: 'q', round_no: 1 }),
      h({ id: '2', role: 'system', content: '其它系统消息', round_no: 1 }),
      h({ id: '3', role: 'assistant', content: 'a', round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(2)
    expect(out[1]).toMatchObject({ role: 'assistant', content: 'a' })
    expect(out[1].tasks).toBeUndefined()
  })

  it('压缩轮：__condense_v1__ system 记录解析为提示条附到该轮 assistant', () => {
    // 后端 persistRound 在轮末追加压缩记录 → 顺序为 user → assistant → 记录。
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: 'q1', round_no: 1 }),
      h({ id: '2', role: 'assistant', content: 'a1', round_no: 1 }),
      h({ id: '3', role: 'user', content: 'q2', round_no: 2 }),
      h({ id: '4', role: 'assistant', content: 'a2', round_no: 2 }),
      h({ id: '5', role: 'system', content: `__condense_v1__${JSON.stringify({ dropped: 3, count: 1 })}`, round_no: 2 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(4) // system 记录不单独成条
    expect(out[1].condensed).toBeUndefined()
    expect(out[3]).toMatchObject({ role: 'assistant', content: 'a2', condensed: { dropped: 3, count: 1 } })
  })

  it('压缩轮：同一轮多条记录合并（dropped 累计、count 取最新）', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: 'q', round_no: 1 }),
      h({ id: '2', role: 'assistant', content: 'a', round_no: 1 }),
      h({ id: '3', role: 'system', content: `__condense_v1__${JSON.stringify({ dropped: 3, count: 1 })}`, round_no: 1 }),
      h({ id: '4', role: 'system', content: `__condense_v1__${JSON.stringify({ dropped: 2, count: 2 })}`, round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out[1]).toMatchObject({ condensed: { dropped: 5, count: 2 } })
  })

  it('非压缩 system 消息（无 __condense_v1__ 前缀）不产生提示条', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: 'q', round_no: 1 }),
      h({ id: '2', role: 'system', content: '__condense_v1__{broken json', round_no: 1 }),
      h({ id: '3', role: 'system', content: '__condense_v1__{"dropped":"3","count":"1"}', round_no: 1 }),
      h({ id: '4', role: 'assistant', content: 'a', round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(2)
    expect(out[1].condensed).toBeUndefined()
  })

  it('压缩记录且无同轮 assistant（异常数据）时安全忽略', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'system', content: `__condense_v1__${JSON.stringify({ dropped: 2, count: 1 })}`, round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(0)
  })

  it('压缩与编排摘要跨轮共存：分别解析为提示条与任务块', () => {
    // 第 1 轮直答 + 压缩记录（轮末）；第 2 轮编排轮（摘要 system 在回答前）。
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: 'q1', round_no: 1 }),
      h({ id: '2', role: 'assistant', content: 'a1', round_no: 1 }),
      h({ id: '3', role: 'system', content: `__condense_v1__${JSON.stringify({ dropped: 4, count: 1 })}`, round_no: 1 }),
      h({ id: '4', role: 'user', content: 'q2', round_no: 2 }),
      h({ id: '5', role: 'system', content: ORCH_SUMMARY, round_no: 2 }),
      h({ id: '6', role: 'assistant', content: 'a2', round_no: 2 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(4)
    expect(out[1]).toMatchObject({ content: 'a1', condensed: { dropped: 4, count: 1 } })
    expect(out[3]).toMatchObject({ content: 'a2', tasks: expect.any(Array) })
    expect(out[3].condensed).toBeUndefined()
  })

  it('工具过程合并：中间 assistant + tool 消息合并进最终回答的 thinking', () => {
    const msgs: HistoryMessage[] = [
      h({ id: '1', role: 'user', content: '计算', round_no: 1 }),
      h({
        id: '2',
        role: 'assistant',
        content: '',
        reasoning: '先算',
        tool_calls: JSON.stringify([{ id: 'c1', name: 'calculator', arguments: { expr: '1+1' } }]),
        round_no: 1,
      }),
      h({ id: '3', role: 'tool', content: '2', tool_call_id: 'c1', round_no: 1 }),
      h({ id: '4', role: 'assistant', content: '结果 2', round_no: 1 }),
    ]
    const out = fromHistory(msgs)
    expect(out).toHaveLength(2)
    const bot = out[1]
    expect(bot.content).toBe('结果 2')
    expect(bot.thinking).toHaveLength(3)
    expect(bot.thinking![0]).toMatchObject({ kind: 'text', content: '先算' })
    expect(bot.thinking![1]).toMatchObject({ kind: 'tool-call', name: 'calculator' })
    expect(bot.thinking![2]).toMatchObject({ kind: 'tool-result', content: '2' })
  })
})
