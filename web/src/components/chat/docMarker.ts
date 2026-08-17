// docMarker.ts —— 聊天上传文件（模块二）· [文档]/[图片] 注入消息的解析工具（纯函数，无组件）。
// 与 ChatDocCard 组件拆分到独立文件：react-refresh 要求单文件只导出组件，
// 非组件导出会破坏 HMR 的 fast-refresh 语义。

/** 解析 [文档] 注入消息 → 元信息 + 可选正文；无法识别返回 null（调用方按普通文本渲染）。
 *  后端注入格式（chatdoc.go·模型自主调用工具）：
 *    [文档] <文件名>（已保存至工作区 <路径>）
 *  空文件注入无工作区路径：
 *    [文档] <文件名>（文件内容为空，无解析内容）
 *  兼容旧版历史消息（含「解析 N 段，…读全文用 file_ops read…」与注入正文）。 */
export function parseDocMarker(content: string): { fileName: string; relPath: string; body?: string } | null {
  const prefix = '[文档] '
  if (!content.startsWith(prefix)) return null
  // 旧版注入含正文（\n\n 分隔）；新版提示词单行无正文。
  const sep = content.indexOf('\n\n')
  const summary = (sep < 0 ? content.slice(prefix.length) : content.slice(prefix.length, sep)).trim()
  const body = sep < 0 ? '' : content.slice(sep + 2)

  const nameMatch = summary.match(/^(.*?)[（(]/)
  // 路径截至中文/英文逗号或右括号（兼容旧版「，读全文用 file_ops …」后缀）。
  // 用 . 而非 \S：文件名/路径可能含空格（如 my report.pdf）。空文件注入
  // 无「工作区」字样 → pathMatch 为 null（relPath 留空）。
  const pathMatch = summary.match(/工作区\s+(.+?)[，,）)]/)
  if (!nameMatch) return null
  return {
    fileName: nameMatch[1].trim(),
    relPath: pathMatch ? pathMatch[1].trim() : '',
    body: body || undefined,
  }
}

/** 消息是否为 [文档] 注入消息（供消息列表分流渲染）。 */
export function isDocMarker(content: string): boolean {
  return content.startsWith('[文档] ')
}

/** 解析 [图片] 注入消息 → 元信息 + 可选描述正文；无法识别返回 null。
 *  图片消息格式（模型自主调用工具）：
 *    [图片] <文件名>（已保存至工作区 <路径>）
 *  兼容旧版历史消息（含【图片内容】描述正文）。 */
export function parseImageMarker(content: string): { fileName: string; relPath: string; body?: string } | null {
  const prefix = '[图片] '
  if (!content.startsWith(prefix)) return null
  const rest = content.slice(prefix.length).trim()
  const sep = rest.indexOf('\n')
  const summary = sep >= 0 ? rest.slice(0, sep).trim() : rest
  const body = sep >= 0 ? rest.slice(sep).trim() : ''
  const nameMatch = summary.match(/^(.*?)[（(]/)
  // 用 . 而非 \S：文件名/路径可能含空格（如 my photo.png）。
  const pathMatch = summary.match(/工作区\s+(.+?)[）)]/)
  if (!nameMatch || !pathMatch) return null
  return { fileName: nameMatch[1].trim(), relPath: pathMatch[1].trim(), body: body || undefined }
}

/** 消息是否为 [图片] 注入消息（供消息列表分流渲染）。 */
export function isImageMarker(content: string): boolean {
  return content.startsWith('[图片] ')
}
