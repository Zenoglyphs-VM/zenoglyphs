// The admin API client. Same shape as @/core/api.ts (this page is also the
// origin serving it, so paths are relative) with two differences: it sends
// mutating requests, and a 401 means the session ended — which flips the
// whole app back to the login view rather than being a per-call error every
// view has to check for.
import type {
  AdminModerationLookup,
  AdminModerationRow,
  AdminSession,
  AdminStatus,
  ImportResult,
  ModerationStatus,
  PurgeAllResult,
  PurgeResult,
  SetModerationResult,
} from './types'

const API = ''

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

let unauthorizedHandler: (() => void) | null = null

/** AdminApp registers this once on mount, so any 401 anywhere — an expired
 * session, a logout in another tab — flips the app back to the login view
 * without every call site having to check for it. */
export function onUnauthorized(fn: () => void): void {
  unauthorizedHandler = fn
}

async function parseError(res: Response): Promise<never> {
  const body = (await res.json().catch(() => ({}))) as {error?: string}
  throw new ApiError(body.error || `${res.status} ${res.statusText}`, res.status)
}

async function handle<T>(res: Response): Promise<T> {
  if (res.status === 401) unauthorizedHandler?.()
  if (!res.ok) return parseError(res)
  return (await res.json()) as T
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(API + path, {
    method,
    credentials: 'same-origin',
    headers: body !== undefined ? {'Content-Type': 'application/json'} : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  return handle<T>(res)
}

/** POSTs a raw text body — the shared blocklist format — for bulk import. */
async function requestText<T>(path: string, text: string): Promise<T> {
  const res = await fetch(API + path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: {'Content-Type': 'text/plain'},
    body: text,
  })
  return handle<T>(res)
}

export const adminApi = {
  session: () => request<AdminSession>('GET', '/admin/api/session'),
  login: (password: string) => request<{ok: boolean}>('POST', '/admin/api/login', {password}),
  logout: () => request<{ok: boolean}>('POST', '/admin/api/logout'),

  status: () => request<AdminStatus>('GET', '/admin/api/status'),

  moderationList: () =>
    request<{count: number; moderation: AdminModerationRow[]}>('GET', '/admin/api/moderation'),

  moderationGet: (id: string) =>
    request<AdminModerationLookup>('GET', `/admin/api/moderation/${encodeURIComponent(id)}`),

  moderationSet: (
    id: string,
    body: {status: ModerationStatus; reason: string; unseen?: boolean; purge?: boolean},
  ) =>
    request<SetModerationResult>('POST', `/admin/api/moderation/${encodeURIComponent(id)}`, body),

  moderationImport: (text: string, purge: boolean) =>
    requestText<ImportResult>(`/admin/api/moderation/import${purge ? '?purge=1' : ''}`, text),

  /** A plain download link, not fetched: the response is
   * Content-Disposition: attachment, and a same-origin <a> click already
   * carries the session cookie. */
  moderationExportURL: '/admin/api/moderation/export',

  purge: (ids: string[]) => request<{purge: PurgeResult}>('POST', '/admin/api/purge', {ids}),

  /** Flushes the whole CDN cache, not specific ids — see the Settings tab. */
  purgeAll: () => request<PurgeAllResult>('POST', '/admin/api/purge-all'),

  changePassword: (currentPassword: string, newPassword: string) =>
    request<{ok: boolean}>('POST', '/admin/api/password', {currentPassword, newPassword}),
}
