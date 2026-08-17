import type { SessionConfig } from '@/types/api'

/**
 * 合并会话配置：保留 base 的全部既有字段，patch 覆盖指定字段（undefined 不覆盖）。
 *
 * 各配置弹窗保存时统一用它构造请求体——后端 `UpdateSessionConfig` 是全量替换，
 * 若只回传本弹窗负责的字段，其它配置（enabled_resources / kb_ids / mcp_servers /
 * thinking / enabled_tools）会被一并清空。显式传入的 `[]` 会保留（例如
 * kb_ids=[] 是"本会话不启用知识库"的合法语义，不可当作未传处理）。
 */
export function mergeSessionConfig(
  base: SessionConfig | undefined,
  patch: Partial<SessionConfig>,
): SessionConfig {
  return { ...(base ?? {}), ...omitUndefined(patch) }
}

/** 剔除值为 undefined 的键：调用方未提供的字段不覆盖 base 中的既有值。 */
function omitUndefined<T extends Record<string, unknown>>(obj: T): T {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(obj)) {
    if (v !== undefined) out[k] = v
  }
  return out as T
}
