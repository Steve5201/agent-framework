import { useId } from 'react'
import { cn } from '@/lib/utils'
import { useIsMobile } from '@/lib/useIsMobile'

/**
 * 星云 Nebula 品牌 Logo（内联 SVG）。
 * 设计：紫→蓝渐变核心 + 环绕轨道与节点——无数智能体/工具如星云般汇聚协作。
 *
 * 可见性：默认仅移动端（安卓）显示。网页端/桌面端内部 UI 不渲染本图标
 * （品牌图标只用于安装包/桌面快捷方式/网站 favicon，见 public/nebula-icon.svg
 * 与 tauri 生成的桌面/安卓图标），避免与界面视觉重复。
 *
 * 注意：页面会同时渲染多个实例（顶栏/侧栏/登录页），若共用同一组
 * gradient/filter id 会导致 DOM 内 id 冲突——浏览器 url(#...) 解析到首个实例的
 * defs，showBg=false 的实例缺 nebBg 渐变时后续实例背景会消失。故用 useId 为
 * 每个实例生成唯一 id，保证各实例独立渲染。
 */
export default function NebulaLogo({
  className,
  showBg = true,
  forceShow = false,
}: {
  className?: string
  showBg?: boolean
  /** 强制显示（默认仅移动端；个别场景如需在桌面端展示可传 true）。 */
  forceShow?: boolean
}) {
  const isMobile = useIsMobile()
  const uid = useId()
  const core = `nebCore${uid}`
  const bg = `nebBg${uid}`
  const halo = `nebHalo${uid}`
  const glow = `nebGlow${uid}`

  if (!isMobile && !forceShow) return null

  return (
    <svg
      viewBox="0 0 512 512"
      className={cn('shrink-0', className)}
      aria-hidden="true"
      role="img"
    >
      <defs>
        <linearGradient id={core} x1="128" y1="96" x2="384" y2="416" gradientUnits="userSpaceOnUse">
          <stop stopColor="#7C3AED" />
          <stop offset="0.55" stopColor="#4F46E5" />
          <stop offset="1" stopColor="#0EA5E9" />
        </linearGradient>
        <linearGradient id={bg} x1="120" y1="96" x2="392" y2="416" gradientUnits="userSpaceOnUse">
          <stop stopColor="#E0E7FF" />
          <stop offset="0.5" stopColor="#EDE9FE" />
          <stop offset="1" stopColor="#DBEAFE" />
        </linearGradient>
        <linearGradient id={halo} x1="96" y1="80" x2="416" y2="432" gradientUnits="userSpaceOnUse">
          <stop stopColor="#7C3AED" stopOpacity="0.35" />
          <stop offset="1" stopColor="#0EA5E9" stopOpacity="0.25" />
        </linearGradient>
        <filter id={glow} x="-40%" y="-40%" width="180%" height="180%">
          <feGaussianBlur stdDeviation="18" result="b" />
          <feMerge>
            <feMergeNode in="b" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
      </defs>

      {showBg && <rect width="512" height="512" rx="112" fill={`url(#${bg})`} />}

      <ellipse cx="256" cy="256" rx="170" ry="170" fill={`url(#${halo})`} filter={`url(#${glow})`} />
      <ellipse cx="256" cy="256" rx="188" ry="96" stroke="#0EA5E9" strokeOpacity="0.4" strokeWidth="4" fill="none" transform="rotate(-24 256 256)" />
      <ellipse cx="256" cy="256" rx="188" ry="96" stroke="#7C3AED" strokeOpacity="0.35" strokeWidth="4" fill="none" transform="rotate(48 256 256)" />

      <circle cx="428" cy="206" r="14" fill="#0EA5E9" filter={`url(#${glow})`} />
      <circle cx="352" cy="414" r="12" fill="#6366F1" />
      <circle cx="138" cy="130" r="12" fill="#8B5CF6" />
      <circle cx="86" cy="342" r="14" fill="#0EA5E9" filter={`url(#${glow})`} />
      <circle cx="408" cy="340" r="9" fill="#3B82F6" />
      <circle cx="112" cy="196" r="9" fill="#A78BFA" />

      <circle cx="256" cy="256" r="82" fill={`url(#${core})`} filter={`url(#${glow})`} />
      <circle cx="256" cy="256" r="82" fill={`url(#${core})`} />
      <circle cx="232" cy="230" r="30" fill="#FFFFFF" fillOpacity="0.35" />
      <circle cx="256" cy="256" r="46" fill="#DBEAFE" fillOpacity="0.22" />
      <circle cx="298" cy="282" r="10" fill="#FFFFFF" fillOpacity="0.9" />
      <circle cx="222" cy="292" r="7" fill="#FFFFFF" fillOpacity="0.75" />
      <circle cx="276" cy="218" r="8" fill="#FFFFFF" fillOpacity="0.85" />
      <circle cx="238" cy="214" r="5" fill="#FFFFFF" fillOpacity="0.65" />
      <circle cx="300" cy="230" r="5" fill="#FFFFFF" fillOpacity="0.65" />
    </svg>
  )
}