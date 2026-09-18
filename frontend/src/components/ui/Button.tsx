import { type ButtonHTMLAttributes, type ReactNode } from 'react'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger'
type Size = 'sm' | 'md' | 'lg'

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant
  size?: Size
  children: ReactNode
}

const variants: Record<Variant, string> = {
  primary: 'bg-accent text-ink hover:bg-white',
  secondary:
    'bg-transparent text-fg border border-border hover:border-border-strong hover:bg-raised',
  ghost: 'bg-transparent text-fg-dim hover:text-fg hover:bg-raised',
  danger: 'bg-danger-soft text-danger border border-danger/25 hover:bg-danger/20',
}

const sizes: Record<Size, string> = {
  sm: 'h-8 px-3 text-xs gap-1.5',
  md: 'h-10 px-4 text-sm gap-2',
  lg: 'h-11 px-5 text-sm gap-2',
}

export function Button({
  variant = 'primary',
  size = 'md',
  className = '',
  children,
  ...props
}: Props) {
  return (
    <button
      type="button"
      className={[
        'inline-flex items-center justify-center rounded-lg font-medium',
        'transition-colors duration-150 active:scale-[0.98]',
        'disabled:opacity-40 disabled:pointer-events-none',
        variants[variant],
        sizes[size],
        className,
      ].join(' ')}
      {...props}
    >
      {children}
    </button>
  )
}
