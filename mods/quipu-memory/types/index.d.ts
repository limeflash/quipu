/** What `engram autocapture stats --json` prints (quipu, cmd/engram/autocapture_stats.go). */
export type MemoryStats = {
  generated_at: string
  db_bytes: number
  totals: { records: number; captured: number; agent: number; imported: number; projects: number }
  project?: MemoryProject
  projects: MemoryProjectRow[]
  days: MemoryDay[]
  models: MemoryModel[]
  spool: number
}

export type MemoryProject = {
  name: string
  records: number
  captured: number
  profile_id?: number
  profile?: string
  summary?: { id: number; session_id: string; updated_at: string; digest: string }
  recent: MemoryRecord[]
  types: Record<string, number>
}

export type MemoryRecord = {
  id: number
  type: string
  title: string
  source: 'captured' | 'agent' | 'imported'
  created_at: string
  preview: string
}

export type MemoryProjectRow = { name: string; records: number; captured: number; last_at: string }

export type MemoryDay = {
  date: string
  records: number
  by_type: Record<string, number>
  calls: number
  errors: number
  tokens_in: number
  tokens_out: number
}

export type MemoryModel = {
  name: string
  state: 'ready' | 'backoff' | 'disabled'
  blocked_until?: string
  last_error?: string
  calls_hour: number
  last_call?: string
}

declare module 'claude-code' {
  interface PluginState {
    'quipu-memory': {
      /** The last stats read, null until the first refresh lands. */
      stats: MemoryStats | null
      /** Why the last refresh failed, null when it worked. */
      error: string | null
      /** When the last refresh landed, ms since the epoch. */
      refreshedAt: number
      /** The record whose text is open in the list, null for none. */
      expanded: number | null
    }
  }
}
