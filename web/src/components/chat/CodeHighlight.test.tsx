import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import CodeHighlight from './CodeHighlight'

/**
 * 代码块高亮渲染契约（真实组件，不 mock）。
 *
 * 关键回归点：prism-react-renderer 的 normalizeTokens 会消费换行符，若渲染时不
 * 注入 '\n' 行分隔，多行代码会被拼到同一行（曾出现的真实 bug）。
 */
describe('CodeHighlight（prism 语法高亮渲染）', () => {
  it('多行代码换行完整保留：textContent 与源码逐字一致', () => {
    const src = 'def hello():\n    print("Hello")\n    return 42'
    const { container } = render(<CodeHighlight code={src} lang="python" />)
    const pre = container.querySelector('pre.msg-code')!
    expect(pre.textContent).toBe(src)
    expect(container.querySelectorAll('.token-line').length).toBe(3)
    expect(container.querySelectorAll('.token.keyword').length).toBeGreaterThan(0)
  })

  it('空行不丢失（空行 token 自带 \\n，不重复注入）', () => {
    const src = '{\n  "a": 1,\n\n  "b": 2\n}'
    const { container } = render(<CodeHighlight code={src} lang="json" />)
    expect(container.querySelector('pre.msg-code')!.textContent).toBe(src)
    expect(container.querySelectorAll('.token-line').length).toBe(5)
  })

  it('Java 经 prismjs 扩展后正常渲染且关键字着色', () => {
    const src =
      'public class Test {\n' +
      '    public static void main(String[] args) {\n' +
      '        System.out.println("hi");\n' +
      '    }\n' +
      '}'
    const { container } = render(<CodeHighlight code={src} lang="java" />)
    const pre = container.querySelector('pre.msg-code')!
    expect(pre.textContent).toBe(src)
    expect(container.querySelectorAll('.token-line').length).toBe(5)
    expect(container.querySelectorAll('.token.keyword').length).toBeGreaterThan(0)
  })

  it('白名单外语言回退纯文本 pre（不丢换行）', () => {
    const src = 'line one\nline two'
    const { container } = render(<CodeHighlight code={src} lang="unknown-lang" />)
    const pre = container.querySelector('pre.msg-code')!
    expect(pre.textContent).toBe(src)
    expect(container.querySelectorAll('.token-line').length).toBe(0)
  })
})
