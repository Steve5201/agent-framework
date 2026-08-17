/**
 * Toggle 配置区统一的圆角滑动开关（role="switch"）。
 *
 * 约定：能力开关 / 思考模式 / 技能 / 知识库 / MCP 的启用项全部使用本组件，
 * 保证配置区交互样式一致（圆角矩形滑动开关，而非 checkbox）。
 */
interface ToggleProps {
  checked: boolean
  onChange: (v: boolean) => void
  /** 只读态（如普通用户的 MCP 配置区）：置灰且不可点 */
  disabled?: boolean
  /** 开关旁的可访问性标签 */
  'aria-label'?: string
}

export default function Toggle({ checked, onChange, disabled, 'aria-label': ariaLabel }: ToggleProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative h-5 w-9 shrink-0 rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
        checked ? 'bg-primary' : 'bg-muted'
      }`}
    >
      <span
        className={`absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white shadow transition-transform ${
          checked ? 'translate-x-4' : 'translate-x-0'
        }`}
      />
    </button>
  )
}
