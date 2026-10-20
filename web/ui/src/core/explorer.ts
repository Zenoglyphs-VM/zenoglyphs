// Block-explorer links.
//
// The templates come from /info, not from here. Which explorer can address
// which object varies — zenonhub.io has no per-momentum page at all — so the
// indexer publishes one template per object kind and a missing template means
// "this explorer cannot show this". Every caller has to handle a null URL,
// which is why this returns one rather than a best-effort guess: a hash that
// renders as plain copyable text is honest, and a link that always 404s is not.
import {computed} from 'vue'
import {useIndexer} from '@/core/composables/useIndexer'
import type {ExplorerLinks} from '@/types'

/** The object kinds an explorer might be able to address. */
export type ExplorerTarget = 'account' | 'transaction' | 'momentum' | 'token'

export function useExplorer() {
  const {info} = useIndexer()

  const explorer = computed<ExplorerLinks>(() => info.value?.explorer ?? {})

  /** What to call this explorer in a tooltip — "verify on zenonhub". */
  const name = computed(() => explorer.value.name ?? 'the explorer')

  /**
   * The permalink for one value, or null when this explorer has no page for
   * that kind of object.
   *
   * Values are encoded: an address or a hash never needs it, but the template
   * is operator-supplied and a value that has to be escaped should be, rather
   * than trusting every future preset to be path-safe.
   */
  function url(target: ExplorerTarget, value: string | number | undefined): string | null {
    const tpl = explorer.value[target]
    if (!tpl || value === undefined || value === '' || value === '—') return null
    return tpl.replace('{v}', encodeURIComponent(String(value)))
  }

  return {name, url}
}

/**
 * The protocol's own address namespace (spec R-8.6). Every address the
 * protocol owns rather than a person begins here and then spells what it is:
 * `z1zen0glyphxdurn…` is the burn sink, `z1zen0glyphxescr0w…` a dispenser's escrow.
 */
const NAMESPACE = 'z1zen0glyphx'
const ESCROW_PREFIX = `${NAMESPACE}escr0w`

/**
 * True for a dispenser's escrow, which holds stock while it is for sale.
 *
 * The indexer keys escrow by the dispenser's own id and the API renders that
 * key as an address in the namespace above, so this is a prefix test on a
 * real address rather than a guess about a string that looked like a hash.
 * The `isHash` fallback below is what an older indexer still sends.
 */
export function isEscrow(v: string | undefined): boolean {
  return !!v && (v.startsWith(ESCROW_PREFIX) || isHash(v))
}

/**
 * True for a protocol-owned address of any kind, escrow or burn sink.
 *
 * A prefix test is sound here, which it would not have been under the old
 * `z1qzenx` namespace. This one starts `z1ze`, whose leading byte is 0x16 —
 * neither of the two type bytes NoM can mint — and key derivation always
 * writes 0x00, so no wallet can be ground into this prefix to have itself
 * labelled protocol-owned. See A-26.
 */
export function isProtocolAddress(v: string | undefined): boolean {
  return !!v && v.startsWith(NAMESPACE)
}

/**
 * True for the 64-hex ids an older indexer used where an address would sit.
 *
 * Before escrow was rendered in the namespace, a dispenser fill showed the
 * dispenser's block hash in the "from" column (`escrowHolder` in
 * internal/indexer/apply.go). Rendering that as if it were an account is the
 * kind of small lie that makes a ledger view untrustworthy, so it is still
 * detected and still labelled as escrow.
 */
export function isHash(v: string | undefined): boolean {
  return !!v && /^[0-9a-f]{64}$/i.test(v)
}
