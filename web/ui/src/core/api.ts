// The indexer client.
//
// Two origins are in play and the distinction is the whole security model:
// this page is served by the API, and every byte that was inscribed is fetched
// from the content origin the API names in /info. Inscribed HTML and SVG
// execute script, so they are only ever rendered inside a sandboxed iframe
// pointed at that other origin — never injected into this document.

/** The API is the origin serving this page, so every path is relative. */
const API = ''

export class ApiError extends Error {}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(API + path)
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as {error?: string}
    throw new ApiError(body.error || `${res.status} ${res.statusText}`)
  }
  return (await res.json()) as T
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(API + path, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const j = (await res.json().catch(() => ({}))) as {error?: string}
    throw new ApiError(j.error || `${res.status} ${res.statusText}`)
  }
  return (await res.json()) as T
}

/** Fetches inscribed bytes as text, optionally capped for a preview. */
export async function fetchText(url: string, limit?: number): Promise<string> {
  const res = await fetch(url)
  const text = await res.text()
  return limit ? text.slice(0, limit) : text
}

function query(params: Record<string, string | number | undefined>): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') qs.set(k, String(v))
  }
  const s = qs.toString()
  return s ? `?${s}` : ''
}

/**
 * How many inscriptions a listing asks for at a time.
 *
 * The gallery used to ask for everything and get the backend's default 100,
 * silently — past that, an inscription simply did not exist as far as the UI
 * was concerned. A page size the UI chooses, plus a control to ask for the
 * next one, is the difference between "showing the newest 20" and "showing
 * all of them, probably".
 */
export const GALLERY_PAGE = 20

export const api = {
  info: () => get<import('@/types').Info>('/info'),

  assets: (params: {
    owner?: string
    issuer?: string
    collection?: string
    mime?: string
    limit?: number
    offset?: number
  }) =>
    get<import('@/types').AssetsPage>(
      '/assets' + query({...params, limit: params.limit ?? GALLERY_PAGE}),
    ),

  asset: (id: string) => get<import('@/types').Asset>(`/asset/${encodeURIComponent(id)}`),

  holders: (id: string) =>
    get<{holders: import('@/types').Holder[]}>(`/asset/${encodeURIComponent(id)}/holders`),

  transfers: (id: string) =>
    get<{transfers: import('@/types').Transfer[]}>(`/asset/${encodeURIComponent(id)}/transfers`),

  refs: (id: string) => get<import('@/types').RefGraph>(`/asset/${encodeURIComponent(id)}/refs`),

  collections: () => get<{collections: import('@/types').Collection[]}>('/collections'),

  collection: (id: string) =>
    get<{
      collection: import('@/types').Collection
      assets: import('@/types').Asset[]
      total: number
    }>(`/collection/${encodeURIComponent(id)}`),

  dispensers: () => get<{dispensers: import('@/types').Dispenser[]}>('/dispensers'),

  momentum: (height: number | string) =>
    get<import('@/types').Momentum>(`/momentum/${encodeURIComponent(String(height))}`),

  // --- EVM (internal/api/evm.go) ---
  evmOverview: () => get<import('@/types').EvmOverview>('/evm/overview'),

  evmAccounts: (params: {kind?: 'contract'; limit?: number; offset?: number} = {}) =>
    get<{accounts: import('@/types').EvmAccount[]} & import('@/types').Paged>(
      '/evm/accounts' + query(params),
    ),

  evmAccount: (addr: string) =>
    get<import('@/types').EvmAccountDetail>(`/evm/account/${encodeURIComponent(addr)}`),

  /** The transaction feed. `address` matches either side; `height` is one momentum. */
  evmTxs: (params: {address?: string; height?: number; limit?: number; offset?: number} = {}) =>
    get<{txs: import('@/types').EvmTx[]} & import('@/types').Paged>('/evm/txs' + query(params)),

  evmBlocks: (params: {limit?: number; offset?: number} = {}) =>
    get<{blocks: import('@/types').EvmBlock[]} & import('@/types').Paged>(
      '/evm/blocks' + query(params),
    ),

  evmBlock: (height: number | string) =>
    get<{
      block: import('@/types').EvmBlock
      txs: import('@/types').EvmTx[]
      /** Neighbouring momentums that also carried EVM activity; 0 when none. */
      prev: number
      next: number
    }>(`/evm/block/${encodeURIComponent(String(height))}`),

  /** Token transfers, read from Transfer logs rather than from calls. */
  evmTransfers: (
    params: {address?: string; token?: string; limit?: number; offset?: number} = {},
  ) =>
    get<{transfers: import('@/types').EvmTransfer[]} & import('@/types').Paged>(
      '/evm/transfers' + query(params),
    ),

  evmTx: (id: string) => get<import('@/types').EvmTx>(`/evm/tx/${encodeURIComponent(id)}`),

  evmToken: (addr: string) =>
    get<import('@/types').EvmToken>(`/evm/token/${encodeURIComponent(addr)}`),

  // Signs and sends from the operator's own faucet key — see EnableEVMFaucet
  // in internal/api/evm.go. Answers 501 when the operator has not configured
  // one, which the caller renders as an ordinary ApiError.
  // What a call must burn to be entitled to `gas`.
  //
  // Quoted by the server rather than computed here: the fee is a fraction of a
  // token's supply at an epoch, and that emission curve is consensus for this
  // metaprotocol. A second implementation of it in TypeScript would be a second
  // thing to keep in step, and the failure mode is a call that underpays and
  // runs out of gas with the ZNN already destroyed.
  evmFee: (gas: number, token: 'znn' | 'qsr' = 'znn') =>
    get<{
      gas: number
      token: string
      amount: string
      epoch: number
      resourcePrice: number
      burnAddress: string
      required: boolean
      requiredFrom: number
    }>(`/evm/fee?gas=${gas}&token=${token}`),

  evmFaucet: (to: string) =>
    post<{
      inscription: string
      to: string
      toZenon?: string
      token: string
      tokenZenon?: string
      amount: string
    }>('/evm/faucet', {to}),

  address: (addr: string) =>
    get<{
      address: string
      holdings: import('@/types').Holding[]
      /** Set when the address is a dispenser's escrow, naming the listing. */
      dispenser?: string
    }>(`/address/${encodeURIComponent(addr)}`),
}
