// Shared helpers for the EVM explorer pages.

/**
 * Shortens an address or a 64-hex id for a table cell.
 *
 * The prefix is kept whole rather than counted against `head`, because the
 * prefix is the part that says what kind of thing this is: `0x` for the
 * Ethereum spelling, and `z1q` / `z1z` for a NoM address, where the third
 * character is the difference between somebody's account and a contract.
 */
export function shortHex(v: string | undefined, head = 6, tail = 4): string {
  if (!v) return ''
  const prefix = v.startsWith('0x') ? '0x' : v.startsWith('z1') ? 'z1' : ''
  const body = v.slice(prefix.length)
  if (body.length <= head + tail) return v
  return `${prefix}${body.slice(0, head)}…${body.slice(-tail)}`
}

/**
 * Renders a token amount at its declared decimals.
 *
 * The value arrives as a decimal string because a uint256 does not survive a
 * JavaScript number, and it is divided with BigInt for the same reason. The
 * fractional part is trimmed of trailing zeros but never rounded: an explorer
 * that rounds a balance is telling the reader something the chain did not say.
 */
export function units(raw: string | undefined, decimals: string | number = 18): string {
  if (!raw) return '0'
  let d = Number(decimals)
  if (!Number.isFinite(d) || d < 0 || d > 77) d = 0
  try {
    const v = BigInt(raw)
    if (d === 0) return v.toLocaleString('en-US')
    const base = 10n ** BigInt(d)
    const whole = v / base
    const frac = (v % base).toString().padStart(d, '0').replace(/0+$/, '')
    return frac ? `${whole.toLocaleString('en-US')}.${frac}` : whole.toLocaleString('en-US')
  } catch {
    return raw
  }
}

/**
 * Relative time, the way a feed reads it: "12 secs ago".
 *
 * A block list is scanned for recency far more often than for calendar dates,
 * so the relative form is the primary one and the absolute timestamp lives in
 * the title attribute for when the exact moment matters.
 */
export function ago(ts: number | undefined): string {
  if (!ts) return '—'
  const secs = Math.max(0, Math.floor(Date.now() / 1000 - ts))
  if (secs < 60) return `${secs} sec${secs === 1 ? '' : 's'} ago`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins} min${mins === 1 ? '' : 's'} ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours} hr${hours === 1 ? '' : 's'} ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} day${days === 1 ? '' : 's'} ago`
  const months = Math.floor(days / 30)
  if (months < 12) return `${months} mo${months === 1 ? '' : 's'} ago`
  return `${Math.floor(months / 12)} yr${Math.floor(months / 12) === 1 ? '' : 's'} ago`
}

/** Absolute time for the tooltip beside every relative one. */
export function exact(ts: number | undefined): string {
  return ts ? new Date(ts * 1000).toLocaleString() : ''
}

/**
 * A call's method name without its parameter list — "transfer", not
 * "transfer(address,uint256)" — for the badge a table row has room for.
 * Falls back to the raw selector, which is honest rather than guessed.
 */
export function methodLabel(tx: {op: string; method?: string; selector?: string}): string {
  if (tx.op === 'deploy') return 'deploy'
  if (tx.method) return tx.method.replace(/\(.*/, '')
  return tx.selector ?? 'call'
}

/** Percentage of the gas budget a transaction actually used. */
export function gasPct(used: number, budget: number): number {
  if (!budget) return 0
  return Math.min(100, (used / budget) * 100)
}

/** True for the all-zero address, which ERC-20 mints and burns use. */
export function isZeroAddress(a: string | undefined): boolean {
  return !!a && /^0x0{40}$/i.test(a)
}


/**
 * The spelling to show for an address.
 *
 * The API serves both: `address` is the canonical hex and always present, and
 * `zenon` is the native bech32 spelling, set only where it is safe to act on —
 * a user account. So preferring `zenon` when it exists shows a holder the
 * string they know from their wallet, and leaves a contract as hex, which is
 * what a contract is: a handle, not an account.
 */
export function addressText(hex: string | undefined, zenon?: string): string {
  return zenon || hex || ''
}
