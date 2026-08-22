import { useEffect, useState } from 'react'

/** 移动端断点（与 Tailwind `md:` 一致：<768px 视为手机端）。
 *  用于需要 JS 判断平台的场景（如 Enter 换行 vs 发送、菜单隐藏）。 */
export function useIsMobile(): boolean {
  const [mobile, setMobile] = useState<boolean>(() =>
    typeof window !== 'undefined' ? window.innerWidth < 768 : false,
  )
  useEffect(() => {
    const onResize = () => setMobile(window.innerWidth < 768)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])
  return mobile
}