export function visibleMemoryStatus(status: string): string {
  switch (status.trim().toUpperCase()) {
    case '':
    case 'PROPOSED':
    case 'CONFIRMED':
    case 'ACTIVE':
      return 'active'
    case 'REJECTED':
    case 'FORGOTTEN':
      return 'forgotten'
    case 'SUPERSEDED':
      return 'superseded'
    default:
      return status.trim().toLowerCase()
  }
}

export function isActiveMemory(status: string): boolean {
  return visibleMemoryStatus(status) === 'active'
}

export function visibleEventLabel(eventType: string): string {
  switch (eventType) {
    case 'MEMORY_PROPOSED':
    case 'MEMORY_CONFIRMED':
      return 'memory saved'
    case 'MEMORY_REJECTED':
      return 'memory forgotten'
    default:
      return eventType.replaceAll('_', ' ').toLowerCase()
  }
}
