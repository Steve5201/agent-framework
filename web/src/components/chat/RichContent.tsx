import { lazy, Suspense, useState, type ReactElement, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import rehypeRaw from 'rehype-raw'
import rehypeSanitize, { defaultSchema, type Options as SanitizeOptions } from 'rehype-sanitize'
import { Check, Copy, Download } from 'lucide-react'
import { downloadUrl, isVideoUrl } from '@/lib/rich'
import { isExternalLink, openExternal } from '@/lib/external'
import { normalizeLang } from '@/lib/codeLang'
import { cn } from '@/lib/utils'
import InlineSVG from './InlineSVG'
import DocDownloadCard from './DocDownloadCard'
import 'katex/dist/katex.min.css'

// ECharts 体积较大，懒加载：只有消息里出现 ```echarts 块时才拉取对应 chunk，
// 避免污染主包首屏体积（ECharts 及其注册代码随 chunk 按需加载）。
const EChartLazy = lazy(() => import('./EChart'))

// 代码块语法高亮：随"有语言标签的普通代码块"按需加载，不进主包首屏。
const CodeHighlightLazy = lazy(() => import('./CodeHighlight'))

// KaTeX 输出包含 MathML（供屏幕阅读器/无障碍）与 HTML 两个部分；默认白名单
// 没有 MathML 标签，需一并放行，否则公式的数学结构会被剥掉只剩视觉部分。
const KATEX_TAGS = [
  'math', 'semantics', 'annotation', 'annotation-xml',
  'mrow', 'mi', 'mn', 'mo', 'mtext', 'msup', 'msub', 'msubsup', 'mfrac',
  'msqrt', 'mroot', 'mspace', 'mtable', 'mtr', 'mtd', 'mover', 'munder',
  'munderover', 'mpadded', 'mphantom', 'menclose', 'mstyle',
]

// 受限 HTML 白名单：只放行"对齐 / 字体样式 / 媒体 / 公式"所需的最小集合，
// 其余标签一律被 rehype-sanitize 剥掉（防 XSS）。与 backend 渲染协议一致。
const sanitizeSchema: SanitizeOptions = {
  ...defaultSchema,
  tagNames: [
    ...(defaultSchema.tagNames ?? []),
    'center', 'video', 'source', 'audio', 'figure', 'figcaption',
    ...KATEX_TAGS,
  ],
  attributes: {
    ...defaultSchema.attributes,
    '*': [...((defaultSchema.attributes ?? {})['*'] ?? []), 'style', 'className'],
    p: ['align'],
    div: ['align'],
    h1: ['align'],
    h2: ['align'],
    h3: ['align'],
    h4: ['align'],
    h5: ['align'],
    h6: ['align'],
    th: ['align'],
    td: ['align'],
    video: ['src', 'controls', 'width', 'height', 'poster', 'loop', 'muted', 'preload'],
    source: ['src', 'type'],
    audio: ['src', 'controls', 'loop', 'preload'],
    math: ['xmlns', 'display'],
    // 代码块对齐参数（```echarts align=center）：由 rehypeMoveCodeMeta 从
    // data.meta 移到 properties.meta，sanitize 白名单放行才能保留。
    code: ['meta'],
  },
}

/** rehype 插件：把代码块节点的 data.meta（```lang align=center 的对齐参数）移到
 *  properties.meta。mdast 的 meta 存在节点 data 上，但后续 rehype 插件
 *  （raw/katex/sanitize）会重建节点并丢弃 data；properties 会被 sanitize 保留
 *  （白名单过滤后仍是属性）。必须排在 rehypeRaw 之前。 */
function rehypeMoveCodeMeta() {
  return (tree: unknown) => {
    const walk = (node: unknown): void => {
      if (!node || typeof node !== 'object') return
      const el = node as {
        type?: string
        tagName?: string
        properties?: Record<string, unknown>
        data?: { meta?: string }
        children?: unknown[]
      }
      if (el.type === 'element' && el.tagName === 'code' && typeof el.data?.meta === 'string') {
        el.properties = el.properties ?? {}
        el.properties.meta = el.data.meta
      }
      el.children?.forEach(walk)
    }
    walk(tree)
  }
}

/** 解析代码块语言标签与对齐参数：
 *  如 ```echarts align=center → { lang: 'echarts', align: 'center' }。
 *  lang 取自 className（language-xxx）；对齐参数取自 mdast meta（如 align=center）。 */
function parseLangSpec(className: string | undefined, meta: string | null | undefined): { lang: string; align?: string } {
  const lang = /language-(\w+)/.exec(className ?? '')?.[1] ?? ''
  let align: string | undefined
  const metaStr = typeof meta === 'string' ? meta : ''
  const m = /align\s*=\s*(\w+)/.exec(metaStr)
  if (m) align = m[1]
  return { lang, align }
}

/** 媒体下载按钮：悬浮在图片/视频右上角，下载到本地。
 *  导出供图片气泡（ImageMessageCard）复用——用户上传图片与 AI 媒体渲染同一套逻辑。 */
export function MediaDownloadButton({ src, kind }: { src: string; kind: 'image' | 'video' }) {
  const title = kind === 'video' ? '下载视频' : '下载图片'
  return (
    <button
      type="button"
      title={title}
      aria-label={title}
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        downloadUrl(src, kind === 'video' ? 'video.mp4' : 'image.png')
      }}
      className="absolute right-1.5 top-1.5 z-10 rounded-md border bg-background/90 p-1 text-muted-foreground shadow-sm hover:bg-accent hover:text-accent-foreground"
    >
      <Download className="h-3.5 w-3.5" />
    </button>
  )
}

/**
 * 代码块复制按钮（悬浮在 pre 右上角）。
 * 点击复制代码文本，成功后图标切换为对勾并带 pop 动画（1.2s 后复位），
 * 给用户明确"已复制"反馈，避免盲点。
 */
function CodeCopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      type="button"
      title={copied ? '已复制' : '复制代码'}
      aria-label={copied ? '已复制' : '复制代码'}
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        if (copied) return
        void navigator.clipboard
          .writeText(text)
          .then(() => {
            setCopied(true)
            window.setTimeout(() => setCopied(false), 1200)
          })
          .catch(() => {
            // 剪贴板权限被拒等：不弹窗打断，按钮静默无变化。
          })
      }}
      className="msg-code-copy"
    >
      {copied ? <Check className="size-3.5 msg-copy-pop" /> : <Copy className="size-3.5" />}
    </button>
  )
}

/**
 * 富文本 + 富媒体渲染器（需求 9，协议见 backend agentsvc/prompt.go）。
 *
 * 渲染协议：
 *   1. 富文本：标准 Markdown（GFM：标题/加粗/斜体/列表/表格/引用/链接/图片），
 *      允许受限 HTML（对齐 align、字体样式 style、video/audio）。
 *   2. 图表：```echarts 代码块 → ECharts 渲染（标准 option JSON）；
 *   3. 内联矢量图：```svg 代码块 → 净化后内联渲染；
 *   4. 视频：```video + url 代码块，或 ![描述](xxx.mp4) 图片语法（URL 以
 *      .mp4/.webm/.ogg/.mov 结尾自动转 <video>）。
 */
export default function RichContent({
  content,
  streaming = false,
}: {
  content: string
  streaming?: boolean
}) {
  const components: Components = {
    // 链接：外部链接（http/https/mailto/tel）一律拦截默认跳转，交给
    // openExternal 处理（浏览器新标签页 / 桌面端系统浏览器），防止 Tauri
    // webview 内导航把整个界面替换成目标网页。
    a({ href, children }) {
      const external = isExternalLink(href)
      return (
        <a
          href={href}
          target={external ? '_blank' : undefined}
          rel={external ? 'noopener noreferrer' : undefined}
          onClick={(e) => {
            if (!external) return
            e.preventDefault()
            void openExternal(href ?? '')
          }}
        >
          {children}
        </a>
      )
    },
    // 块级代码：语言类在 className 上；特殊语言在此替换渲染，普通块代码由
    // pre 统一套容器样式。行内代码（无语言类）走内联样式。
    code({ className, children }) {
      if (className) {
        return <code className={className}>{children}</code>
      }
      return <code className="msg-code-inline">{children}</code>
    },
    // pre 包裹块级 code：识别协议语言（echarts/svg/video）替换为对应组件，
    // 其余保持代码块容器样式。语言标签可带对齐参数（如 ```echarts align=center）。
    pre({ node, children }) {
      const child = (Array.isArray(children) ? children[0] : children) as
        | ReactElement<{ className?: string; children?: ReactNode }>
        | undefined
      // react-markdown 不把 mdast meta 放进组件 props，且后续 rehype 插件会丢弃
      // data；rehypeMoveCodeMeta 已把 meta 移到 code 节点的 properties 上
      // （```svg align=center → properties.meta = 'align=center'）。
      const codeNode = node?.children?.[0] as { properties?: Record<string, unknown> } | undefined
      const codeMeta = typeof codeNode?.properties?.meta === 'string' ? codeNode.properties.meta : undefined
      const { lang, align } = parseLangSpec(child?.props?.className, codeMeta)
      // children 可能是单个字符串或字符串数组（不同插件输出不同），统一 join 成文本，
      // 避免 String(数组) 用逗号拼接破坏代码块/SVG/JSON 原文。
      const text = Array.isArray(child?.props?.children)
        ? (child.props.children as ReactNode[]).map(String).join('')
        : String(child?.props?.children ?? '')
      if (lang === 'echarts') {
        return (
          <Suspense
            fallback={
              <div className="my-2 rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
                （图表加载中…）
              </div>
            }
          >
            <EChartLazy source={text} streaming={streaming} align={align} />
          </Suspense>
        )
      }
      if (lang === 'svg') {
        return <InlineSVG source={text} align={align} />
      }
      if (lang === 'video') {
        const url = text.trim()
        return url ? (
          <div className={cn('relative my-2', align === 'center' ? 'mx-auto' : align === 'right' ? 'ml-auto' : '')}>
            <video src={url} controls className="max-w-full rounded-md" />
            <MediaDownloadButton src={url} kind="video" />
          </div>
        ) : null
      }
      if (lang === 'doc') {
        // 文档生成（render_document）下载卡片：内容为工作区下载路径
        // users/<uid>/chat-docs/<fileID>/<file>（或完整 URL）。
        const path = text.trim()
        return path ? <DocDownloadCard path={path} /> : null
      }
      // 普通代码块：有语言标签且属于高亮白名单 → 语法高亮（懒加载，fallback 保持
      // 纯文本代码块防闪烁）；无标签 / 白名单外 → 纯文本。两者统一套 Mac 风格
      // 容器：顶部交通灯栏（红黄绿）+ 语言标签 + 复制按钮（入栏右端）。
      if (lang && normalizeLang(lang)) {
        return (
          <div className="msg-code-mac my-2">
            <div className="msg-code-bar">
              <span className="msg-dot msg-dot-red" />
              <span className="msg-dot msg-dot-yellow" />
              <span className="msg-dot msg-dot-green" />
              <span className="msg-code-lang">{lang}</span>
              <span className="ms-auto" />
              <CodeCopyButton text={text} />
            </div>
            <Suspense fallback={<pre className="msg-code">{children}</pre>}>
              <CodeHighlightLazy code={text} lang={lang} />
            </Suspense>
          </div>
        )
      }
      return (
        <div className="msg-code-mac my-2">
          <div className="msg-code-bar">
            <span className="msg-dot msg-dot-red" />
            <span className="msg-dot msg-dot-yellow" />
            <span className="msg-dot msg-dot-green" />
            <span className="msg-code-lang">{lang || 'text'}</span>
            <span className="ms-auto" />
            <CodeCopyButton text={text} />
          </div>
          <pre className="msg-code">{children}</pre>
        </div>
      )
    },
    // 媒体：视频扩展名 → <video>；否则 <img>；均可下载到本地。
    // 尺寸（width/height）与对齐由协议控制：模型可用 HTML 属性或 <p align> 包裹。
    img({ src, alt, width, height, style, className: mediaClass }) {
      if (!src) return null
      if (isVideoUrl(src)) {
        return (
          <div className="relative my-2 inline-block">
            <video
              src={src}
              controls
              title={alt}
              width={width}
              height={height}
              style={style}
              className={cn('max-w-full rounded-md', mediaClass)}
            />
            <MediaDownloadButton src={src} kind="video" />
          </div>
        )
      }
      return (
        <div className="relative my-2 inline-block">
          <img
            src={src}
            alt={alt ?? ''}
            loading="lazy"
            width={width}
            height={height}
            style={style}
            className={cn('max-w-full rounded-md', mediaClass)}
          />
          <MediaDownloadButton src={src} kind="image" />
        </div>
      )
    },
  }

  return (
    <div className="rich-content">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[
          rehypeMoveCodeMeta,
          rehypeRaw,
          rehypeKatex,
          [rehypeSanitize, sanitizeSchema],
        ]}
        components={components}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}
