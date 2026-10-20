// Display helpers. Every one of these formats DATA, which the design system
// renders mono + tabular-nums — so these return strings for a mono context and
// never decide colour or weight themselves.

/** `start…end`, the house truncation for hashes and addresses. */
export function short(hash: string | undefined, n = 8): string {
  if (!hash) return ''
  if (hash.length <= n * 2 + 1) return hash
  return `${hash.slice(0, n)}…${hash.slice(-4)}`
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1048576).toFixed(2)} MB`
  return `${(n / 1073741824).toFixed(2)} GB`
}

/** Seconds → the coarsest unit that keeps one decimal place meaningful. */
export function duration(seconds: number | undefined): string {
  if (!seconds || seconds < 0) return '—'
  if (seconds < 60) return `${seconds.toFixed(0)}s`
  if (seconds < 3600) return `${(seconds / 60).toFixed(1)}m`
  if (seconds < 86400) return `${(seconds / 3600).toFixed(1)}h`
  return `${(seconds / 86400).toFixed(1)}d`
}

/** Unix seconds → local date-time, or an em dash when there is nothing. */
export function when(ts: number | undefined): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString()
}

/** Thousands separators, for heights and counts. */
export function count(n: number | undefined): string {
  return typeof n === 'number' ? n.toLocaleString('en-US') : '—'
}

/** "image/svg+xml; charset=utf-8" → "image/svg+xml". */
export function baseMime(mime: string): string {
  return mime.split(';')[0]
}
