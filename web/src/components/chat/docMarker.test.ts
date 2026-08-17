import { describe, expect, it } from 'vitest'
import { isImageMarker, parseImageMarker } from './docMarker'

// 与后端 uploadChatImage 注入格式一致的样例（[图片] 前缀 + 摘要行，无正文）。
// 图片注入路径含 users/<uid>/ 前缀（全局相对），前端据此拼 /files 渲染地址。
const IMAGE_SAMPLE = '[图片] photo.png（已保存至工作区 users/1/chat-files/9/photo.png）'

describe('parseImageMarker（[图片] 注入消息解析）', () => {
  it('识别合法消息并提取文件名/路径', () => {
    const img = parseImageMarker(IMAGE_SAMPLE)
    expect(img).not.toBeNull()
    expect(img!.fileName).toBe('photo.png')
    expect(img!.relPath).toBe('users/1/chat-files/9/photo.png')
  })

  it('非 [图片] 前缀返回 null（普通消息按气泡渲染）', () => {
    expect(parseImageMarker('普通问题')).toBeNull()
    expect(parseImageMarker('[图片]xxx')).toBeNull() // 缺空格前缀
  })

  it('未启用视觉解析时无描述正文', () => {
    const img = parseImageMarker(IMAGE_SAMPLE)
    expect(img!.body).toBeUndefined()
  })

  it('启用视觉解析：提取【图片内容】描述正文', () => {
    const img = parseImageMarker(
      '[图片] photo.png（已保存至工作区 users/1/chat-files/9/photo.png）\n\n【图片内容】这是一张课程表，包含周一至周五的课程安排',
    )
    expect(img).not.toBeNull()
    expect(img!.fileName).toBe('photo.png')
    expect(img!.body).toBe('【图片内容】这是一张课程表，包含周一至周五的课程安排')
  })

  it('isImageMarker 判别消息类型（与文档标记互斥）', () => {
    expect(isImageMarker(IMAGE_SAMPLE)).toBe(true)
    expect(isImageMarker('[文档] 课程简介.md（解析 2 段）\n\n正文')).toBe(false)
  })
})
