/**
 * The Nexus app icon, drawn inline: the same dark tile and mark as the desktop
 * app's Nexus.ico and public/brand/nexus-app.svg, in both themes.
 */
export function NexusMark({ size = 30, title }: { size?: number; title?: string }) {
  return (
    <svg
      viewBox="0 0 64 64"
      width={size}
      height={size}
      fill="none"
      role={title ? 'img' : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      <rect width="64" height="64" rx="15" fill="#171918" />
      <g transform="translate(5 5) scale(.84375)">
        <path d="M14 47V20a6 6 0 0 1 6-6h5l25 33" stroke="#F4F1E9" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round" />
        <path d="M50 17v27a6 6 0 0 1-6 6h-5L14 17" stroke="#E8BB67" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round" />
        <path d="m25 32 8 10" stroke="#F4F1E9" strokeWidth="8" strokeLinecap="round" />
      </g>
    </svg>
  )
}

export function NexusBrand({ compact = false, className = '' }: { compact?: boolean; className?: string }) {
  return (
    <span className={`nexus-brand ${className}`}>
      <NexusMark title={compact ? 'Nexus' : undefined} />
      {!compact && <span>Nexus</span>}
    </span>
  )
}
