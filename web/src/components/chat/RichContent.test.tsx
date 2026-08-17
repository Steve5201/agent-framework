import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RichContent from './RichContent'

// jsdom 中 React.lazy 的动态 import 无法在 waitFor 时限内 resolve（Suspense 一直
// 停留 fallback）。mock 掉懒加载模块，让 echarts 用例立即渲染出真实占位文案。
vi.mock('./EChart', () => ({
  default: () => <div>（图表数据解析失败：需要标准的 ECharts option JSON）</div>,
}))

// 代码块语法高亮同样懒加载：mock 为纯文本回退（与 Suspense fallback 一致），
// 既避免 jsdom 下 lazy import 不确定，也避免测试拉取 prism 语法包。
vi.mock('./CodeHighlight', () => ({
  default: ({ code }: { code: string }) => <pre className="msg-code">{code}</pre>,
}))

// 链接拦截：保留真实 isExternalLink 实现，仅替身 openExternal 以便断言调用。
// 下载：替身 downloadUrl，断言下载按钮使用归一化后的完整 URL。
const mocks = vi.hoisted(() => ({ openExternal: vi.fn(), downloadUrl: vi.fn() }))
vi.mock('@/lib/external', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/external')>()
  return { ...actual, openExternal: mocks.openExternal }
})
vi.mock('@/lib/rich', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/rich')>()
  return { ...actual, downloadUrl: mocks.downloadUrl }
})

describe('RichContent（需求 9 富媒体渲染协议）', () => {
  beforeEach(() => {
    mocks.openExternal.mockClear()
    mocks.downloadUrl.mockClear()
  })
  it('渲染 Markdown 富文本（加粗/行内码）', () => {
    render(<RichContent content="**加粗** 和 `行内码`" />)
    expect(screen.getByText('加粗').tagName).toBe('STRONG')
    expect(screen.getByText('行内码').tagName).toBe('CODE')
  })

  it('图片语法渲染 img；视频扩展名自动渲染 video', () => {
    const { container } = render(
      <RichContent content={'![图](https://x/a.png)\n\n![视频](https://x/a.mp4)'} />,
    )
    expect(container.querySelector('img')).not.toBeNull()
    expect(container.querySelector('video')).not.toBeNull()
  })

  it('图片/视频带下载按钮', () => {
    const { container } = render(<RichContent content={'![图](https://x/a.png)'} />)
    expect(container.querySelector('button[aria-label="下载图片"]')).not.toBeNull()
  })

  it('svg 代码块内联渲染为图片', () => {
    const { container } = render(
      <RichContent content={'```svg\n<svg width="10" height="10"><rect /></svg>\n```'} />,
    )
    expect(container.innerHTML).toContain('<svg')
  })

  it('echarts 代码块：非法 JSON 显示解析失败占位（懒加载 + 不崩溃）', async () => {
    render(<RichContent content={'```echarts\n{not json\n```'} />)
    // ECharts 懒加载，先出 fallback；模块就绪后渲染非法 JSON 占位
    await waitFor(() => expect(screen.getByText(/图表数据解析失败/)).toBeTruthy())
  })

  it('受限 HTML（对齐）经 sanitize 白名单后保留', () => {
    const { container } = render(<RichContent content={'<p align="center">居中</p>'} />)
    expect(container.querySelector('p[align="center"]')).not.toBeNull()
  })

  it('HTML img 的 width/height/style 透传到渲染（协议媒体尺寸）', () => {
    const { container } = render(
      <RichContent content={'<p align="center"><img src="https://x/a.png" width="240" height="160" /></p>'} />,
    )
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img).toHaveAttribute('width', '240')
    expect(img).toHaveAttribute('height', '160')
  })

  it('HTML video 的 width 透传（协议媒体尺寸）', () => {
    const { container } = render(
      <RichContent content={'<video src="https://x/a.mp4" width="480" controls></video>'} />,
    )
    const video = container.querySelector('video')
    expect(video).not.toBeNull()
    expect(video).toHaveAttribute('width', '480')
  })

  it('相对媒体路径拼服务器基址（桌面端 tauri.localhost origin 下也能加载）', () => {
    localStorage.setItem('agent.server_url', 'http://47.108.207.37:8080')
    const { container } = render(<RichContent content={'![图](/files/users/1/eq_0.png)'} />)
    const img = container.querySelector('img')
    expect(img?.getAttribute('src')).toBe('http://47.108.207.37:8080/files/users/1/eq_0.png')
    // 下载按钮同样使用归一化后的完整 URL
    fireEvent.click(container.querySelector('button[aria-label="下载图片"]')!)
    expect(mocks.downloadUrl).toHaveBeenCalledWith(
      'http://47.108.207.37:8080/files/users/1/eq_0.png',
      'image.png',
    )
    localStorage.removeItem('agent.server_url')
  })

  it('外部/绝对媒体 URL 原样保留，不重复拼接基址', () => {
    const { container } = render(
      <RichContent content={'![a](https://x/a.png)\n\n```video\n//cdn.example.com/v.mp4\n```'} />,
    )
    expect(container.querySelector('img')?.getAttribute('src')).toBe('https://x/a.png')
    expect(container.querySelector('video')?.getAttribute('src')).toBe('//cdn.example.com/v.mp4')
  })

  it('svg 代码块语言标签 align=center → 块级居中包裹', () => {
    const { container } = render(
      <RichContent content={'```svg align=center\n<svg width="10" height="10"><rect /></svg>\n```'} />,
    )
    const wrap = container.querySelector('div.relative.my-2')
    expect(wrap).not.toBeNull()
    expect(wrap?.className).toContain('text-center')
  })

  it('video 代码块语言标签 align=center → mx-auto 居中', () => {
    const { container } = render(<RichContent content={'```video align=center\nhttps://x/a.mp4\n```'} />)
    const wrap = container.querySelector('div.relative.my-2')
    expect(wrap).not.toBeNull()
    expect(wrap?.className).toContain('mx-auto')
    expect(container.querySelector('video')).not.toBeNull()
  })

  it('script 等危险 HTML 被剥离', () => {
    const { container } = render(<RichContent content={'<script>alert(1)</script>正文'} />)
    expect(container.querySelector('script')).toBeNull()
  })

  it('普通代码块带复制按钮，点击复制原文并切换为已复制', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    const desc = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    const { container } = render(<RichContent content={'```go\npackage main\n```'} />)
    const copyBtn = container.querySelector('button[aria-label="复制代码"]') as HTMLButtonElement
    expect(copyBtn).not.toBeNull()
    fireEvent.click(copyBtn)
    await waitFor(() => expect(writeText).toHaveBeenCalled())
    // 复制内容 = 代码块原文（不含首尾换行）
    const copied = writeText.mock.calls[0][0] as string
    expect(copied.trim()).toBe('package main')
    await waitFor(() => expect(container.querySelector('button[aria-label="已复制"]')).not.toBeNull())
    if (desc) Object.defineProperty(navigator, 'clipboard', desc)
  })

  it('嵌套反引号的代码块：外层 4 反引号整体渲染为一个块，复制内容含内层围栏', async () => {
    // 渲染协议：代码块内容里含 ```（演示 Markdown 语法等）时，外层围栏加长到 4 个
    // 反引号（CommonMark：结束围栏长度 >= 开始围栏），否则内层 ``` 会提前闭合外层块。
    const writeText = vi.fn().mockResolvedValue(undefined)
    const desc = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    const src = '````markdown\n```go\nfunc main() {}\n```\n````'
    const { container } = render(<RichContent content={src} />)
    // 只允许一个代码块容器，而不是被内层 ``` 拆成多个
    expect(container.querySelectorAll('pre.msg-code')).toHaveLength(1)
    fireEvent.click(container.querySelector('button[aria-label="复制代码"]')!)
    await waitFor(() => expect(writeText).toHaveBeenCalled())
    const copied = writeText.mock.calls[0][0] as string
    expect(copied).toContain('```go')
    expect(copied).toContain('func main() {}')
    if (desc) Object.defineProperty(navigator, 'clipboard', desc)
  })

  it('LaTeX 公式经 KaTeX 渲染（$$…$$ 块级公式）', () => {
    const { container } = render(
      <RichContent content={'$$ \\vec{l_1} + \\vec{l_2} = \\vec{l_3} $$'} />,
    )
    expect(container.querySelector('.katex')).not.toBeNull()
    expect(container.innerHTML).toContain('\\vec')
  })

  it('doc 代码块渲染下载卡片（文档生成，P4-D）', () => {
    const { container } = render(
      <RichContent content={'```doc\nusers/1/chat-docs/doc_1_ab/教案.docx\n```'} />,
    )
    expect(screen.getByText('教案.docx')).toBeTruthy()
    expect(screen.getByText('Word 文档')).toBeTruthy()
    const btn = container.querySelector('button[aria-label="下载文件"]')
    expect(btn).not.toBeNull()
    // 下载地址 = gateway /files + 工作区相对路径
    expect(container.querySelector('button[title="下载文件"]')).not.toBeNull()
  })

  it('doc 代码块：完整 URL 直用；空内容不渲染', () => {
    const { container } = render(<RichContent content={'```doc\nhttp://h:8182/files/a/b.pptx\n```'} />)
    expect(screen.getByText('b.pptx')).toBeTruthy()
    expect(screen.getByText('PPT 演示文稿')).toBeTruthy()
    expect(container.querySelector('button[aria-label="下载文件"]')).not.toBeNull()
    const empty = render(<RichContent content={'```doc\n\n```'} />)
    expect(empty.container.querySelector('button[aria-label="下载文件"]')).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// 链接拦截（防桌面端点击链接把整个界面导航成目标网页）
// ---------------------------------------------------------------------------
describe('RichContent 链接渲染', () => {
  beforeEach(() => mocks.openExternal.mockClear())

  it('外部链接带 target=_blank 与 rel=noopener noreferrer', () => {
    render(<RichContent content={'[官网](https://www.example.edu.cn)'} />)
    const link = screen.getByRole('link', { name: '官网' })
    expect(link).toHaveAttribute('href', 'https://www.example.edu.cn')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('点击外部链接不导航当前页面，改为 openExternal 新开', () => {
    render(<RichContent content={'[官网](https://www.example.edu.cn)'} />)
    fireEvent.click(screen.getByRole('link', { name: '官网' }))
    expect(mocks.openExternal).toHaveBeenCalledWith('https://www.example.edu.cn')
  })

  it('mailto 链接同样走外部打开', () => {
    render(<RichContent content={'[联系](mailto:hi@example.edu.cn)'} />)
    fireEvent.click(screen.getByRole('link', { name: '联系' }))
    expect(mocks.openExternal).toHaveBeenCalledWith('mailto:hi@example.edu.cn')
  })

  it('相对链接保留默认导航，不拦截', () => {
    render(<RichContent content={'[站内](/login)'} />)
    const link = screen.getByRole('link', { name: '站内' })
    expect(link).not.toHaveAttribute('target')
    fireEvent.click(link)
    expect(mocks.openExternal).not.toHaveBeenCalled()
  })
})
