export function NexusBrand({ compact = false, className = '' }: { compact?: boolean; className?: string }) {
  return <span className={`nexus-brand ${className}`}>
    <img src="/brand/nexus-mark.svg" width="30" height="30" alt={compact ? 'Nexus' : ''} />
    {!compact && <span>Nexus</span>}
  </span>
}
