import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import ChatDocCard from './ChatDocCard'
import { isDocMarker, parseDocMarker } from './docMarker'

// 与后端注入消息格式一致的样例（[文档] 前缀 + 提示词单行，无正文注入）。
const SAMPLE = '[文档] 课程简介.md（已保存至工作区 users/9/chat-files/3/课程简介.md）'
// 空文件注入格式（需求 4）：无正文、无工作区路径。
const SAMPLE_EMPTY = '[文档] 空.txt（文件内容为空，无解析内容）'
// 旧版历史消息（解析 N 段 + 注入正文 + file_ops 说明），解析器需兼容。
const SAMPLE_LEGACY =
  '[文档] 课程简介.md（解析 2 段，全文已保存至工作区 users/9/chat-files/3/课程简介.md，读全文用 file_ops read 该相对路径）\n\n这是第一段内容。\n\n第二段内容。'

describe('parseDocMarker（模块二·[文档] 注入消息解析）', () => {
  it('识别新版提示词消息并提取文件名/路径，无正文', () => {
    const doc = parseDocMarker(SAMPLE)
    expect(doc).not.toBeNull()
    expect(doc!.fileName).toBe('课程简介.md')
    expect(doc!.relPath).toBe('users/9/chat-files/3/课程简介.md')
    expect(doc!.body).toBeUndefined()
  })

  it('兼容旧版历史消息：提取文件名/路径与注入正文', () => {
    const doc = parseDocMarker(SAMPLE_LEGACY)
    expect(doc).not.toBeNull()
    expect(doc!.fileName).toBe('课程简介.md')
    // 路径必须在中文逗号处截止，不能把「读全文用 file_ops…」吃进路径
    expect(doc!.relPath).toBe('users/9/chat-files/3/课程简介.md')
    expect(doc!.body).toBe('这是第一段内容。\n\n第二段内容。')
  })

  it('文件名/路径含空格也能提取', () => {
    const doc = parseDocMarker('[文档] my report.pdf（已保存至工作区 users/1/chat-files/2/my report.pdf）')
    expect(doc).not.toBeNull()
    expect(doc!.fileName).toBe('my report.pdf')
    expect(doc!.relPath).toBe('users/1/chat-files/2/my report.pdf')
  })

  it('空文件注入：无路径无正文，relPath 留空（需求 4）', () => {
    const doc = parseDocMarker(SAMPLE_EMPTY)
    expect(doc).not.toBeNull()
    expect(doc!.fileName).toBe('空.txt')
    expect(doc!.relPath).toBe('')
    expect(doc!.body).toBeUndefined()
  })

  it('非 [文档] 前缀返回 null（普通消息按气泡渲染）', () => {
    expect(parseDocMarker('普通问题')).toBeNull()
    expect(parseDocMarker('[文档]xxx')).toBeNull() // 缺空格前缀
  })

  it('isDocMarker 判别消息类型', () => {
    expect(isDocMarker(SAMPLE)).toBe(true)
    expect(isDocMarker('普通问题')).toBe(false)
  })
})

describe('ChatDocCard 组件渲染', () => {
  it('展示文件名 / 工作区路径 / 已保存至工作区角标', () => {
    render(<ChatDocCard content={SAMPLE} />)
    expect(screen.getByText('课程简介.md')).toBeTruthy()
    expect(screen.getByText('users/9/chat-files/3/课程简介.md')).toBeTruthy()
    expect(screen.getByText('已保存至工作区')).toBeTruthy()
  })

  it('空文件：渲染文件名与空文件角标，不显示路径（需求 4）', () => {
    render(<ChatDocCard content={SAMPLE_EMPTY} />)
    expect(screen.getByText('空.txt')).toBeTruthy()
    expect(screen.getByText('空文件')).toBeTruthy()
    expect(screen.getByText(/文件内容为空/)).toBeTruthy()
  })

  it('卡片无正文展开入口（系统不再注入解析正文）', () => {
    render(<ChatDocCard content={SAMPLE} />)
    expect(screen.queryByText('展开解析内容')).toBeNull()
    expect(screen.queryByText('收起解析内容')).toBeNull()
  })

  it('无法解析的内容不渲染卡片', () => {
    const { container } = render(<ChatDocCard content="普通文本" />)
    expect(container.firstChild).toBeNull()
  })
})
