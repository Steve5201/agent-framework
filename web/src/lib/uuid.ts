/**
 * 生成请求级 UUID（request_id / 游客 ID 等）。
 *
 * 优先使用 Web Crypto API 的 crypto.randomUUID()（安全上下文
 * HTTPS/localhost 下可用）；非安全上下文（纯 HTTP 部署）降级为
 * Math.random 组合，保证页面在 http:// 下也能正常发请求。
 */
export function genUuid(): string {
  try {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
      return crypto.randomUUID()
    }
  } catch {
    // crypto 不可用或方法缺失，走降级路径
  }
  // 降级：时间戳 + 随机数，足够满足"请求链路唯一标识"的用途。
  return `gen-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}-${Math.random().toString(36).slice(2, 6)}`
}
