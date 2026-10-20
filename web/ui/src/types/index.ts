// The shapes zcribe's API actually returns, mirroring internal/api/api.go.
//
// These are hand-written rather than generated: the API is small, and a type
// that drifts from the Go struct is caught by the one place that reads it.

/**
 * Block-explorer permalink templates, each containing one `{v}`.
 *
 * Absent keys are meaningful: they say this explorer has no page for that kind
 * of object, and the viewer must render the value as plain text rather than
 * link it. zenonhub.io has no per-momentum page, so `momentum` is unset there.
 */
export interface ExplorerLinks {
  name?: string
  account?: string
  transaction?: string
  momentum?: string
  token?: string
}

/** GET /info — protocol, indexer and policy facts the whole UI keys off. */
export interface Info {
  protocol: string
  prefix: string
  protocolVersion: number
  indexerVersion: string
  syncHeight: number
  nodeHeight: number
  startHeight: number
  confirmationDepth: number
  stateRoot: string
  events: number
  /** Absolute origin the inscribed bytes are served from — never this one. */
  contentOrigin: string
  /** Whether inscribed HTML and SVG are allowed to execute (recursion). */
  scriptsEnabled: boolean
  snapshotInterval: number
  snapshotRetain: number
  maxRefs: number
  maxBlockData: number
  maxBlockDataPoW: number
  maxAssetParts: number
  allowedMimes: string[]
  /** Database file size on disk, in bytes (main file + WAL + shm). */
  dbSizeBytes: number
  /** Sum of every asset's inscribed content, in bytes — a subset of dbSizeBytes. */
  contentBytes: number
  /**
   * Approximately how long fetching and indexing this whole chain takes, at
   * the indexer's currently measured momentums/sec. Absent until enough
   * momentums have been processed to measure a rate.
   */
  syncDurationSeconds?: number
  /** The network the indexed node is on: 1 mainnet, 69 the docker devnet. */
  chainId?: number
  explorer: ExplorerLinks
}

/** How the indexer classifies content, derived from the mime allowlist. */
export type AssetClass =
  'image' | 'iframe' | 'video' | 'audio' | 'pdf' | 'text' | 'markdown' | 'code' | 'binary'

export interface Asset {
  id: string
  number: number
  issuer: string
  owner: string
  collection?: string
  name?: string
  description?: string
  supply: string
  decimals: number
  mime: string
  class: AssetClass
  attributes?: Record<string, unknown>
  extensions?: Record<string, unknown>
  size: number
  parts: number
  momentum: number
  momentumIndex: number
  timestamp: number
  contentUrl: string
  dataWithheld?: boolean
  /** "full" once enough momentums sit above it; "soft" while applied only. */
  status: string
  confirmations: number
  confirmed: boolean
  refs?: string[]
}

export interface AssetsPage {
  assets: Asset[]
  total: number
}

export interface Holder {
  address: string
  amount: string
  /**
   * Set when this row is a dispenser's escrow rather than someone's wallet.
   * `address` is then the z1zen0glyphxescr0w… address that names the escrow, and
   * this is the dispenser it belongs to — the address is a display form and
   * cannot be resolved back to an id.
   */
  dispenser?: string
}

/**
 * What a movement was. The indexer names it because "from and to" cannot:
 * a sale and a plain send differ only in who the sender is.
 *
 * `mint`, `escrow` and `release` are reconstructed by the indexer at serve
 * time — the events behind them move balances without writing a transfer row
 * (see internal/api/provenance.go) — and arrive flagged `synthetic`.
 */
export type TransferKind = 'mint' | 'transfer' | 'burn' | 'escrow' | 'release' | 'sale'

export interface Transfer {
  kind?: TransferKind
  momentum: number
  momentumIndex: number
  /** The account block that moved the units — this is the tx to verify. */
  block: string
  from: string
  to: string
  amount: string
  burned?: boolean
  timestamp: number
  /** The dispenser behind an escrow party, when either side is one. */
  fromDispenser?: string
  toDispenser?: string
  /** True when the indexer reconstructed this row rather than recording it. */
  synthetic?: boolean
}

/** One asset an address holds, from GET /address/{addr}. */
export interface Holding {
  asset: Asset
  amount: string
}

/** One declared import of a composite inscription. */
export interface Ref {
  slot: number
  id: string
  name?: string
  mime?: string
  resolved: boolean
  withheld?: boolean
}

export interface RefGraph {
  refs: Ref[]
  referencedBy: string[]
}

/** Collections come off the store with Go's exported field names. */
export interface Collection {
  ID: string
  Name: string
  Description?: string
  Issuer: string
  Height: number
}

export interface Dispenser {
  id: string
  asset: string
  issuer: string
  remaining: string
  price: string
}

export interface Momentum {
  height: number
  hash: string
  stateRoot: string
  timestamp: number
  txs: string[]
}

// --- EVM ---
//
// The EVM is a metaprotocol layered on inscriptions (internal/evm): contracts
// and calls are `application/evm` mints, and everything below is *derived* by
// replaying them. Nothing here is stored on chain, which is why an account has
// no id of its own — its address is its identity.

export interface EvmOverview {
  chainId: number
  stateRoot: string
  height: number
  accounts: number
  contracts: number
  tokens: number
  txs: number
  failed: number
  applied: number
  skipped: number
  gasCap: number
  gasPerPlasma: number
  inscriptionMime: string
  /** Inscribed bytes this state was replayed from, in bytes. */
  contentBytes: number
  /** How long the last full replay took, in milliseconds. */
  rebuildMs: number
}

export interface EvmAccount {
  address: string
  /**
   * The native spelling — the same 20 bytes as `address`, written the way
   * syrius writes them. Present for every address, contracts included; this
   * is what a reader is shown, and `address` is the canonical key.
   */
  zenon?: string
  /**
   * Whether a NoM wallet may usefully pay this address. False for a contract,
   * which lives in a namespace no keypair can reach — ZNN sent there is
   * destroyed rather than delivered.
   */
  sendable?: boolean
  /** user | embedded | protocol | unreachable — which namespace byte 0 is in. */
  namespace?: string
  kind: 'account' | 'contract'
  nonce: number
  balance: string
  codeSize: number
  isToken?: boolean
  /** The inscription carrying a contract's code — its permanent artifact. */
  inscription?: string
  /** Transactions this account was party to. Set by listings only. */
  txCount?: number
}

/** A page of results: what was asked for, and how much there is. */
export interface Paged {
  total: number
  offset: number
  count: number
}

/**
 * One momentum, summarised as a block.
 *
 * This chain has no blocks of its own — a momentum is what batches EVM
 * inscriptions together — so a "block" here is the momentum that carried them.
 */
export interface EvmBlock {
  height: number
  timestamp: number
  hash?: string
  txs: number
  failed: number
  gasUsed: number
}

/** One ERC-20 Transfer event, with its transaction's context restored. */
export interface EvmTransfer {
  inscription: string
  height: number
  timestamp: number
  token: string
  from: string
  /** The native spelling of `from`, present only for a user account. */
  fromZenon?: string
  to: string
  toZenon?: string
  amount: string
}

/**
 * One decoded ABI value.
 *
 * `zenon` is the native spelling of an address parameter — for every address,
 * contracts included. Whether it is one a wallet may pay is a separate
 * question, answered by `sendable`.
 *
 * `hex` is set only for a word the decoder could not type — an argument to an
 * unregistered selector, or a constructor argument. Such a word is shown with
 * every reading the bytes allow rather than one guessed type, because a
 * 32-byte word genuinely is both the number and the address.
 */
export interface EvmParam {
  name?: string
  type: string
  value: string
  zenon?: string
  /** Address parameters only: false for one no keypair can reach. */
  sendable?: boolean
  hex?: string
  items?: EvmParam[]
}

/** A decoded call or event: which function, and with what. */
export interface EvmDecoded {
  selector: string
  signature?: string
  name?: string
  params?: EvmParam[]
  /** Anything the decode could not do — always render this if present. */
  note?: string
}

export interface EvmLog {
  address: string
  topics: string[]
  data: string
  event?: string
  from?: string
  fromZenon?: string
  to?: string
  toZenon?: string
  amount?: string
  decoded?: EvmDecoded
}

/** What a deployment turned into, in the absence of an ABI. */
export interface EvmDeploy {
  initCodeSize: number
  runtimeCodeSize: number
  /** Registered signatures whose selectors appear in the deployed runtime. */
  interface?: string[]
  /** Dispatcher selectors no registered signature explains. */
  unknownSelectors?: number
  constructorArgs?: EvmParam[]
  note?: string
}

export interface EvmTx {
  inscription: string
  op: 'deploy' | 'call'
  from: string
  fromZenon?: string
  to: string
  toZenon?: string
  success: boolean
  gasUsed: number
  /** What the inscription's plasma entitled it to. */
  gasBudget: number
  output: string
  error?: string
  logs?: EvmLog[]
  selector?: string
  method?: string
  /** Calldata verbatim, alongside `decoded` so a reader can check one
   * against the other — the decode depends on a signature registry the
   * explorer supplies, not on anything the chain asserts. */
  input?: string
  decoded?: EvmDecoded
  returns?: EvmParam[]
  deploy?: EvmDeploy
  /** The dynamic-plasma congestion multiplier this call was priced at, in
   * thousandths. 1000 is an uncongested network. */
  resourcePrice?: number
  /** The carrier block's value, set only when it went to the burn sink. */
  feeBurned?: string
  feeToken?: string
  /** The momentum that carried this inscription — the closest thing this
   * derived chain has to a block. */
  momentum: number
  timestamp: number
}

/** One token balance held by an account. Decimals come from the token. */
export interface EvmHolding {
  token: string
  balance: string
  decimals: string
}

export interface EvmHolder {
  address: string
  zenon?: string
  balance: string
}

export interface EvmAccountDetail {
  account: EvmAccount
  /** Signatures the deployed runtime dispatches on. Empty for a plain account. */
  interface?: string[]
  /** Dispatcher selectors no registered signature explains. */
  unknownSelectors?: number
  /** The chain this indexer follows, so a wallet on another one is caught. */
  chainId?: number
  storage: {Key: string; Value: string}[]
  /** The newest few only; the full list is paged at /evm/txs?address=. */
  txs: EvmTx[]
  txTotal: number
  /** Deployed bytecode, 0x-prefixed. Empty for a plain account. */
  code: string
  /** Tokens this account holds — the balances view of a *user*. */
  holdings: EvmHolding[]
  /**
   * Present only when this account is itself a token contract: its supply and
   * the balances of everyone holding it — the balances view of a *contract*.
   */
  token?: {
    totalSupply: string
    decimals: string
    holders: EvmHolder[]
  } | null
}

export interface EvmToken {
  address: string
  isToken: boolean
  totalSupply: string
  decimals: string
  holders: EvmHolder[]
  transfers: EvmLog[]
}
