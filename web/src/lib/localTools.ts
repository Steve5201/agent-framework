// localTools.ts —— 阶段3·本地工具（External=true）前端适配层。
//
// 职责：识别"需桌面客户端执行"的本地工具，执行或降级：
//   - Tauri 桌面端：弹确认后调用 Rust 命令在本地执行，结果回填；
//   - 浏览器：无本地能力，立即回填"请使用桌面客户端"的失败结果，
//     让 agent 降级答复（避免服务端长时间挂起等待）。
//
// 后端 External 工具集见 backend/internal/tools/builtin/local_shell.go。

/** 与后端 External=true 的工具集保持一致的本地工具名单。
 *  新增本地工具时需在此同步登记（后端加工具 + 此处加名字）。 */
export const LOCAL_TOOL_NAMES: ReadonlySet<string> = new Set(['local_shell'])

/** 是否运行在 Tauri 桌面端（webview 注入 __TAURI_INTERNALS__，浏览器无）。 */
export function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
}

/** 本地 shell 执行结果（对应 Rust 端 LocalExecResult）。 */
export interface LocalExecResult {
  content: string
  isError: boolean
}

/** 调用 Tauri Rust 端在本地执行 shell 命令（默认超时由 Rust 端控制）。
 *  仅桌面端可用；浏览器调用会因 invoke 不存在而抛错。
 *  timeoutSecs 语义：>0 强制该秒数超时；0 = 采用 Rust 端默认（30s）；
 *  -1 = 不限超时（自由模式专用）。 */
export async function runLocalShell(command: string, cwd?: string, timeoutSecs = 0): Promise<LocalExecResult> {
  const { invoke } = await import('@tauri-apps/api/core')
  return (await invoke<LocalExecResult>('local_shell_execute', { command, cwd, timeoutSecs })) as LocalExecResult
}
