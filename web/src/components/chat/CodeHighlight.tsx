import { Highlight, themes, Prism } from 'prism-react-renderer'
import { cn } from '@/lib/utils'
import { normalizeLang } from '@/lib/codeLang'

/**
 * 代码块语法高亮（prism-react-renderer，独立 chunk 按需加载）。
 *
 * 协议：模型输出 ```<语言> 代码块，前端据此用 Prism 做 token 级高亮；
 * 未知/白名单外语言由 normalizeLang 归一化为 null，走纯文本回退。
 * 固定浅色主题（oneLight）——代码块作为独立容器，不随全局亮/暗色切换。
 * 高亮为纯 token 着色（React 组件渲染，非 innerHTML），与 rehype-sanitize
 * 的 XSS 净化体系不冲突。
 *
 * ⚠️ 换行保真（关键坑）：prism-react-renderer 的 normalizeTokens 把换行符当
 * 分隔符消费——普通行内容里没有 \n（只有空行会保留为 content="\n" 的 empty
 * token）。若每行只渲染成内联 <span> 而不注入换行，所有行会拼在一行上。
 * 因此每行 <span> 后必须补 '\n'（空行 token 自带 \n，避免重复注入）。
 */
function renderHighlighted(
  code: string,
  prismLang: string,
  className?: string,
) {
  return (
    <Highlight code={code} language={prismLang} theme={themes.oneLight}>
      {({ style, tokens, getLineProps, getTokenProps }) => (
        <pre className={cn('msg-code', className)} style={style}>
          <code>
            {tokens.map((line, i) => {
              const isLast = i === tokens.length - 1
              const endsWithNl = line[line.length - 1]?.empty === true
              return (
                <span key={i} {...getLineProps({ line })}>
                  {line.map((token, key) => (
                    <span key={key} {...getTokenProps({ token })} />
                  ))}
                  {!isLast && !endsWithNl ? '\n' : null}
                </span>
              )
            })}
          </code>
        </pre>
      )}
    </Highlight>
  )
}

// prism-react-renderer 内置 Prism 只捆绑了常见语言（go/python/js/ts/...）。
// 额外语法（Java 等教育常用语言）按官方扩展方式注册：先把该实例挂到
// globalThis.Prism，再动态导入 prismjs 对应组件——组件会注册到全局实例上，
// Highlight 内部用的正是这个实例（浏览器端标准做法，本模块是懒加载 chunk）。
// 静态列出 import 供 bundler 分析；需要新语言时在此加一行即可。
async function ensureExtraGrammars() {
  const prismAny = Prism as unknown as { languages: Record<string, unknown> }
  if (prismAny.languages.java) return
  ;(globalThis as Record<string, unknown>).Prism = Prism
  await import('prismjs/components/prism-java')
}

await ensureExtraGrammars()

export default function CodeHighlight({
  code,
  lang,
  className,
}: {
  code: string
  lang: string
  className?: string
}) {
  const prismLang = normalizeLang(lang)
  if (!prismLang) {
    // 白名单外 / 无语言标签：纯文本代码块（保持 .msg-code 样式，与改动前一致）
    return <pre className={cn('msg-code', className)}>{code}</pre>
  }
  return renderHighlighted(code, prismLang, className)
}
