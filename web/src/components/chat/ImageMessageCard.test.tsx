import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import ImageMessageCard from './ImageMessageCard'

// 与后端 uploadChatImage 注入格式一致的样例（图片无正文，模型自主调用工具解析）。
const IMAGE_SAMPLE = '[图片] photo.png（已保存至工作区 users/1/chat-files/9/photo.png）'

describe('ImageMessageCard（图片消息渲染·直接渲染原图）', () => {
  it('渲染原图（拼 /files 地址）+ 文件名 + 下载按钮，不展示解析文本', () => {
    render(<ImageMessageCard content={IMAGE_SAMPLE} />)
    const img = screen.getByAltText('photo.png') as HTMLImageElement
    expect(img.src).toContain('/files/users/1/chat-files/9/photo.png')
    expect(screen.getByText('photo.png')).toBeTruthy()
    // 下载按钮存在（复用 AI 气泡媒体渲染逻辑）。
    expect(screen.getByTitle('下载图片')).toBeTruthy()
  })

  it('图片加载失败：降级为文件记录卡片，不裂图', () => {
    render(<ImageMessageCard content={IMAGE_SAMPLE} />)
    fireEvent.error(screen.getByAltText('photo.png'))
    expect(screen.getByText('users/1/chat-files/9/photo.png')).toBeTruthy()
    expect(screen.queryByTitle('下载图片')).toBeNull()
  })

  it('无法解析的内容不渲染卡片', () => {
    const { container } = render(<ImageMessageCard content="普通文本" />)
    expect(container.firstChild).toBeNull()
  })
})
