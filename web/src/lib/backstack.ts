import { useEffect } from 'react'

/**
 * 安卓返回键 / 边缘滑动 返回处理（可关闭覆盖层栈）。
 *
 * 背景：安卓 WebView 物理返回键默认——有历史则回退历史页，否则退出应用。
 * 应用内的覆盖层（配置弹窗 / 菜单面板 / 会话列表抽屉）不是独立历史页，
 * 用户按返回键会直接退回桌面而非关闭当前层，体验割裂。
 *
 * 方案：维护一个"可关闭覆盖层"栈。每个覆盖层打开时 registerBackHandler(close)，
 * 关闭时 unregister。全局监听 popstate（由 pushState 制造的历史触发）与安卓返回键，
 * 命中时调用栈顶的 close 回调（而非退出应用）；栈空时才允许退出。
 */

/** 覆盖层关闭回调栈（后进先出，栈顶 = 最上层覆盖层）。 */
const handlerStack: Array<() => void> = []

let globalBound = false

/** 全局 popstate 处理：关闭栈顶覆盖层。 */
function onGlobalPopState(): void {
  const top = handlerStack.pop()
  if (top) top()
}

/** 全局绑定（模块级单例，随首个组件挂载注册）。 */
function ensureGlobalBound(): void {
  if (globalBound) return
  globalBound = true
  window.addEventListener('popstate', onGlobalPopState)
}

/** 覆盖层打开：注册关闭回调 + 压入虚拟历史（制造返回键可触发的 popstate）。 */
export function registerBackHandler(close: () => void): void {
  ensureGlobalBound()
  handlerStack.push(close)
  try {
    window.history.pushState({ nebulaBack: true }, '')
  } catch {
    /* 忽略 */
  }
}

/** 覆盖层关闭：移除回调 + 回退本次虚拟历史。 */
export function unregisterBackHandler(): void {
  handlerStack.pop()
  try {
    if (window.history.state?.nebulaBack) window.history.back()
  } catch {
    /* 忽略 */
  }
}

/**
 * React hook：覆盖层在"打开期间"调用。
 * 返回 guard，供组件在打开覆盖层时 push、关闭时 pop。
 */
export function useBackGuard(onClose: () => void) {
  useEffect(() => {
    // 仅作为 hook 生命周期挂载的规范化（真正 push/pop 由调用方在开/关时显式触发）
    return () => {
      /* 卸载时无需清理（handlerStack 由 unregisterBackHandler 管理） */
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  return {
    push: () => registerBackHandler(onClose),
    pop: () => unregisterBackHandler(),
  }
}