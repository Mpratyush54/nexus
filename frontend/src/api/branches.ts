import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type MemoryBranch = {
  id: string
  project_id: string
  name: string
  owner_id?: string
  parent_branch_id?: string
  visibility: string
  created_at: string
  archived_at?: string | null
  potentially_stale?: boolean
}

export type BranchEntry = {
  Key?: string
  key?: string
  Content?: string
  content?: string
}

export type BranchChange = {
  Key?: string
  key?: string
  OldContent?: string
  old_content?: string
  NewContent?: string
  new_content?: string
}

export type BranchDiff = {
  project_id: string
  source: string
  source_id: string
  target: string
  target_id: string
  added: BranchEntry[]
  removed: BranchEntry[]
  modified: BranchChange[]
  unchanged: BranchEntry[]
}

function entryKey(e: BranchEntry | BranchChange) {
  return (e as BranchEntry).key ?? (e as BranchEntry).Key ?? ''
}

function entryContent(e: BranchEntry) {
  return e.content ?? e.Content ?? ''
}

export function normalizeDiff(diff: BranchDiff) {
  return {
    ...diff,
    added: (diff.added ?? []).map((e) => ({ key: entryKey(e), content: entryContent(e) })),
    removed: (diff.removed ?? []).map((e) => ({ key: entryKey(e), content: entryContent(e) })),
    modified: (diff.modified ?? []).map((c) => ({
      key: entryKey(c),
      oldContent: c.old_content ?? c.OldContent ?? '',
      newContent: c.new_content ?? c.NewContent ?? '',
    })),
    unchanged: (diff.unchanged ?? []).map((e) => ({ key: entryKey(e), content: entryContent(e) })),
  }
}

export const branchesApi = {
  list(projectId: string) {
    return apiRequest<ListResponse<MemoryBranch>>(
      `/branches?project_id=${encodeURIComponent(projectId)}`,
    )
  },
  create(input: { name: string; project_id: string; from?: string; visibility?: string }) {
    return apiRequest<MemoryBranch>('/branches', { method: 'POST', body: input })
  },
  checkout(name: string, projectId: string) {
    return apiRequest(`/branches/${encodeURIComponent(name)}/checkout?project_id=${encodeURIComponent(projectId)}`, {
      method: 'POST',
      body: {},
    })
  },
  diff(projectId: string, target: string, source = 'main') {
    const q = new URLSearchParams({ project_id: projectId, target, source })
    return apiRequest<BranchDiff>(`/branches/diff?${q}`)
  },
  merge(input: { project_id: string; source: string; target: string }) {
    return apiRequest('/branches/merge', { method: 'POST', body: input })
  },
}
