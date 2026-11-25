type ProjectLike = { display_name?: string; folder_name?: string; id: string }

// Older clients resolved every new account into a shared "nexus-default"
// project. Those rows still exist; show them as what they are to people
// instead of the internal placeholder. Display only: the folder name is unchanged.
const PLACEHOLDER_FOLDERS = new Set(['nexus-default'])
const PLACEHOLDER_LABEL = 'Default workspace'

function isPlaceholder(folder?: string) {
  return Boolean(folder && PLACEHOLDER_FOLDERS.has(folder.trim().toLowerCase()))
}

/** Human name for a project: display name, else folder name, else a short ID. */
export function projectLabel(p: ProjectLike, idChars = 8): string {
  if (p.display_name?.trim() && !isPlaceholder(p.display_name)) return p.display_name
  if (isPlaceholder(p.folder_name) || isPlaceholder(p.display_name)) return PLACEHOLDER_LABEL
  return p.folder_name || `${p.id.slice(0, idChars)}…`
}

/** Secondary line under the name: the folder name, or the short ID for placeholder/unnamed projects. */
export function projectSubtitle(p: ProjectLike, idChars = 12): string {
  if (p.folder_name && !isPlaceholder(p.folder_name)) return p.folder_name
  return p.id.slice(0, idChars)
}
