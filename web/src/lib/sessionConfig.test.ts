// sessionConfig.test.ts —— mergeSessionConfig 回归测试。
//
// 背景：后端 UpdateSessionConfig 是全量替换，各配置弹窗若只回传本弹窗
// 负责的字段，会清空其它配置（enabled_resources/kb_ids/mcp_servers/thinking）。
// 本测试锁定"保留全部既有字段、只覆盖指定字段"的合并语义。
import { describe, it, expect } from 'vitest'
import { mergeSessionConfig } from './sessionConfig'

describe('mergeSessionConfig', () => {
  const fullBase = {
    enabled_resources: ['search', 'kb_search'],
    enabled_tools: ['web_search'],
    thinking: { enabled: true, reasoning_effort: 'high' },
    kb_ids: ['kb_1'],
    mcp_servers: ['fs'],
  }

  it('undefined base 时只返回 patch', () => {
    expect(mergeSessionConfig(undefined, { thinking: { enabled: false } })).toEqual({
      thinking: { enabled: false },
    })
  })

  it('只覆盖 patch 字段，其余字段原样保留', () => {
    expect(
      mergeSessionConfig(fullBase, {
        enabled_resources: ['search'],
      }),
    ).toEqual({
      enabled_resources: ['search'],
      enabled_tools: ['web_search'],
      thinking: { enabled: true, reasoning_effort: 'high' },
      kb_ids: ['kb_1'],
      mcp_servers: ['fs'],
    })
  })

  it('显式空数组不会被当作"未传"而丢失（kb_ids=[] 是合法语义）', () => {
    expect(
      mergeSessionConfig(fullBase, { kb_ids: [] }),
    ).toEqual({
      enabled_resources: ['search', 'kb_search'],
      enabled_tools: ['web_search'],
      thinking: { enabled: true, reasoning_effort: 'high' },
      kb_ids: [],
      mcp_servers: ['fs'],
    })
  })

  it('patch 中 undefined 字段不覆盖 base 既有值', () => {
    const patch: { mcp_servers?: string[] } = { mcp_servers: undefined }
    expect(mergeSessionConfig(fullBase, patch).mcp_servers).toEqual(['fs'])
  })

  it('一次覆盖多个字段', () => {
    expect(
      mergeSessionConfig(fullBase, {
        kb_ids: [],
        mcp_servers: [],
        thinking: { enabled: false },
      }),
    ).toEqual({
      enabled_resources: ['search', 'kb_search'],
      enabled_tools: ['web_search'],
      thinking: { enabled: false },
      kb_ids: [],
      mcp_servers: [],
    })
  })
})
