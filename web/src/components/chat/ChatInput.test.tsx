import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ChatInput from './ChatInput'

vi.mock('@/stores/chat', () => {
  const state = {
    sending: false,
    regeneratingId: null,
    sendMessage: vi.fn(),
    stopStreaming: vi.fn(),
    uploadDocument: vi.fn(),
    activeId: null,
    sessions: [],
    createSession: vi.fn(),
  }
  return {
    useChatStore: (selector: (s: unknown) => unknown) => selector(state),
  }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ user: null }),
}))

function buildPaste(files: File[]) {
  const data = {
    files: files as unknown as FileList,
    getData: () => '',
  }
  return {
    clipboardData: data,
    preventDefault: vi.fn(),
  }
}

describe('ChatInput 粘贴上传文件', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('粘贴含文件的剪贴板时接管并加入待发列表（不上传、不发送）', () => {
    render(<ChatInput />)
    const ta = screen.getByPlaceholderText(/输入消息/) as HTMLTextAreaElement
    const png = new File(['x'], 'screenshot.png', { type: 'image/png' })
    fireEvent.paste(ta, buildPaste([png]))
    // 粘贴后待发列表出现文件项，且未发送
    expect(screen.getByText('screenshot.png')).toBeTruthy()
  })

  it('粘贴无扩展名截图时自动补扩展名以通过类型白名单', () => {
    render(<ChatInput />)
    const ta = screen.getByPlaceholderText(/输入消息/) as HTMLTextAreaElement
    const img = new File(['x'], '', { type: 'image/png' }) // 剪贴板截图常无文件名
    fireEvent.paste(ta, buildPaste([img]))
    expect(screen.getByText('pasted.png')).toBeTruthy()
  })
})