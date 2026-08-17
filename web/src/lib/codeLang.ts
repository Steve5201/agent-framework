/**
 * 代码块语言标签 → Prism 语言 id 映射（语法高亮白名单）。
 *
 * react-markdown 把围栏语言写入 code 元素的 language-<lang> 类名；前端据此
 * 决定是否用 prism-react-renderer 做高亮。白名单外（或空标签）返回 null，
 * 渲染为纯文本代码块——不为边缘语言引入额外语法包。
 *
 * 与 backend agentsvc/prompt.go「内容渲染协议」第 1 条保持一致：
 * 模型输出代码块须标注语言标签，前端才能做语法高亮。
 */

/** 围栏语言标签（小写，容忍空白）→ prism 语言 id。内置语言覆盖 prism-react-renderer
 * 捆绑集 + CodeHighlight 里经 prismjs 额外注册的语法（如 Java），新增时两处同步。 */
const LANG_ALIAS: Record<string, string> = {
  // Go / React 技术栈常用
  go: 'go',
  golang: 'go',
  typescript: 'typescript',
  ts: 'typescript',
  jsx: 'jsx',
  tsx: 'tsx',
  javascript: 'javascript',
  js: 'javascript',
  html: 'markup',
  xml: 'markup',
  css: 'css',
  scss: 'scss',
  sass: 'sass',
  json: 'json',
  // 其他常见编程语言
  python: 'python',
  py: 'python',
  java: 'java',
  c: 'c',
  cpp: 'cpp',
  rust: 'rust',
  rs: 'rust',
  sql: 'sql',
  // 脚本 / 标记 / 配置
  bash: 'bash',
  sh: 'bash',
  shell: 'bash',
  zsh: 'bash',
  markdown: 'markdown',
  md: 'markdown',
  yaml: 'yaml',
  yml: 'yaml',
  diff: 'diff',
  patch: 'diff',
}

/** 规范化围栏语言标签 → prism 语言 id；空标签 / 白名单外返回 null（走纯文本渲染）。 */
export function normalizeLang(lang: string): string | null {
  const key = lang.trim().toLowerCase()
  if (!key) return null
  return LANG_ALIAS[key] ?? null
}
