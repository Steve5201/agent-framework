import { describe, expect, it } from 'vitest'
import { normalizeLang } from './codeLang'

describe('normalizeLang（代码块语言标签 → prism 语言 id）', () => {
  it('常见语言与其别名归一化', () => {
    expect(normalizeLang('go')).toBe('go')
    expect(normalizeLang('golang')).toBe('go')
    expect(normalizeLang('py')).toBe('python')
    expect(normalizeLang('ts')).toBe('typescript')
    expect(normalizeLang('js')).toBe('javascript')
    expect(normalizeLang('sh')).toBe('bash')
    expect(normalizeLang('yml')).toBe('yaml')
    expect(normalizeLang('md')).toBe('markdown')
    expect(normalizeLang('cpp')).toBe('cpp')
    expect(normalizeLang('rs')).toBe('rust')
    expect(normalizeLang('java')).toBe('java')
  })

  it('大小写与首尾空白容忍', () => {
    expect(normalizeLang(' Go ')).toBe('go')
    expect(normalizeLang('TSX')).toBe('tsx')
  })

  it('空标签 / 白名单外语言返回 null（走纯文本回退）', () => {
    expect(normalizeLang('')).toBeNull()
    expect(normalizeLang('   ')).toBeNull()
    expect(normalizeLang('kotlin')).toBeNull()
    expect(normalizeLang('unknown-lang')).toBeNull()
  })
})
