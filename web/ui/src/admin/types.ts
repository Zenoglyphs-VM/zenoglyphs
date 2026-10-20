// Shapes /admin/api/ returns, mirroring internal/api/admin.go — the same
// hand-written-rather-than-generated convention as @/types for the public API.
import type {Asset, Info} from '@/types'

export interface AdminSession {
  enabled: boolean
  authenticated: boolean
}

export interface AdminSnapshot {
  height: number
  depth: string
  events: number
  createdAt: number
}

export interface AdminStatus {
  info: Info
  tipHeight: number
  moderatedCount: number
  snapshots: AdminSnapshot[]
  purgeConfigured: boolean
}

/** The three-way policy the panel and `zcribe moderate` both offer. */
export type ModerationStatus = 'visible' | 'redacted' | 'hidden'

export interface AdminModerationRow {
  assetId: string
  status: ModerationStatus
  reason?: string
  found: boolean
  name?: string
}

export interface AdminModerationLookup {
  assetId: string
  found: boolean
  status: ModerationStatus
  reason?: string
  asset?: Asset
  galleryUrl?: string
}

export interface PurgeResult {
  attempted: boolean
  ok: boolean
  backend?: string
  error?: string
  urls: string[]
}

export interface SetModerationResult {
  ok: boolean
  assetId: string
  status: ModerationStatus
  purge?: PurgeResult
}

export interface ImportResult {
  ok: boolean
  blocked: number
  ids: string[]
  purge?: PurgeResult
}

export interface PurgeAllResult {
  purge: PurgeResult
}
