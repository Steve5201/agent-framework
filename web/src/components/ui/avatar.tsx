import * as React from 'react'
import { cn } from '@/lib/utils'

interface AvatarProps extends React.HTMLAttributes<HTMLSpanElement> {
  /** 展示的首字母（无则显示占位符） */
  fallback?: string
}

/** 极简圆形头像：显示用户名首字符。 */
function Avatar({ className, fallback, ...props }: AvatarProps) {
  return (
    <span
      className={cn(
        'inline-flex h-8 w-8 shrink-0 select-none items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground',
        className,
      )}
      {...props}
    >
      {fallback ? fallback.slice(0, 1).toUpperCase() : '?'}
    </span>
  )
}

export { Avatar }
