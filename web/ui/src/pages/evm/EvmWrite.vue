<script setup lang="ts">
import {computed, onMounted, ref, watch} from 'vue'
import {Badge, Button, Card, CardContent, Input, Label} from 'nom-ui'
import {api} from '@/core/api'
import {useWallet} from '@/wallet/useWallet'
import {encodeCall, parseArg, splitSignature} from '@/wallet/abi'
import {units} from './evm'
import HashLink from './HashLink.vue'
import EvmWallet from './EvmWallet.vue'

// Calling a contract from the browser.
//
// The call is an ordinary inscription: a send from the holder's own account,
// carrying the Zenoglyphs envelope in its data field. Syrius supplies the
// signature, the chain position and the plasma; this page supplies the bytes.
// Nothing in the extension knows what an EVM is, and nothing needs to.
//
// Read-only functions are deliberately absent from this panel. They are
// answered by the indexer's own `Query` with no inscription, no plasma and no
// approval dialog — asking someone to sign a block to learn a balance would be
// charging them for a question the explorer already answers for free.
const props = defineProps<{
  address: string
  /** Signatures the deployed runtime dispatches on, from the account page. */
  signatures?: string[]
}>()

const wallet = useWallet()

const signature = ref('')
const args = ref<string[]>([])
const publishing = ref(false)
const error = ref('')
const published = ref('')

// How much gas this call may use, and therefore what it costs. The two are the
// same number: the fee buys the budget, and there are no refunds, so choosing
// what to burn *is* choosing the gas limit.
const gas = ref(100_000)
const quote = ref<Awaited<ReturnType<typeof api.evmFee>> | null>(null)
const quoting = ref(false)

async function refreshQuote() {
  quoting.value = true
  try {
    quote.value = await api.evmFee(gas.value)
  } catch {
    quote.value = null
  } finally {
    quoting.value = false
  }
}
onMounted(refreshQuote)
watch(gas, refreshQuote)

/**
 * The functions worth offering.
 *
 * A getter costs nothing to call and changes nothing, so publishing an
 * inscription for one is pure waste — and `view` is not marked in a selector,
 * so the only signal available is the shape. Zero-argument functions and the
 * standard read methods are filtered out on that basis; anything else is
 * offered, including functions this explorer cannot name, because a contract
 * nobody registered still has to be callable.
 */
const READ_ONLY = new Set([
  'totalSupply()',
  'decimals()',
  'name()',
  'symbol()',
  'balanceOf(address)',
  'allowance(address,address)',
  'get()',
])

const writable = computed(() => (props.signatures ?? []).filter((s) => !READ_ONLY.has(s)))

const params = computed(() => (signature.value ? splitSignature(signature.value).types : []))

watch(signature, () => {
  args.value = params.value.map(() => '')
  error.value = ''
  published.value = ''
})

/** Parameter names, when this explorer knows them. Cosmetic; types are real. */
const NAMES: Record<string, string[]> = {
  'transfer(address,uint256)': ['to', 'value'],
  'approve(address,uint256)': ['spender', 'value'],
  'transferFrom(address,address,uint256)': ['from', 'to', 'value'],
  'add(uint256)': ['n'],
}
const names = computed(() => NAMES[signature.value] ?? [])

/**
 * The calldata, rebuilt on every keystroke, and the error if it cannot be.
 *
 * Shown before publishing rather than after, because the approval dialog in
 * Syrius shows a base64 blob: whatever a signer is going to understand about
 * this call, they have to understand it here.
 */
const preview = computed<{hex: string; error: string}>(() => {
  if (!signature.value) return {hex: '', error: ''}
  try {
    const values = params.value.map((t, i) => parseArg(t, args.value[i] ?? ''))
    return {hex: encodeCall(signature.value, values), error: ''}
  } catch (e) {
    return {hex: '', error: e instanceof Error ? e.message : String(e)}
  }
})

async function publish() {
  publishing.value = true
  error.value = ''
  published.value = ''
  try {
    const values = params.value.map((t, i) => parseArg(t, args.value[i] ?? ''))
    const calldata = encodeCall(signature.value, values)
    const bytes = new Uint8Array(
      (calldata.match(/../g) ?? []).map((h) => Number.parseInt(h, 16)),
    )
    // Re-quote immediately before signing rather than reusing what is on
    // screen. Both terms of the price move — the supply term with the epoch,
    // the congestion term with the momentum — and a stale quote underpays,
    // which buys less gas than asked with the ZNN destroyed either way.
    const fresh = await api.evmFee(gas.value)
    quote.value = fresh
    const fee = fresh.required
      ? {to: fresh.burnAddress, amount: fresh.amount, token: fresh.token}
      : undefined
    published.value = await wallet.publishEvm(
      {op: 'call', to: props.address, gas: fresh.required ? gas.value : undefined},
      bytes,
      fee,
    )
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    publishing.value = false
  }
}
</script>

<template>
  <Card>
    <CardContent class="pt-5">
      <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div class="text-ledger text-muted-foreground">Write</div>
        <EvmWallet />
      </div>

      <p v-if="!writable.length" class="text-sm text-muted-foreground">
        No writable functions were recognised in this contract's bytecode. It
        can still be called with raw calldata using
        <code class="text-xs">zcribe evm call</code>.
      </p>

      <template v-else>
        <div class="space-y-3">
          <div>
            <Label for="evm-write-fn" class="text-xs">function</Label>
            <select
              id="evm-write-fn"
              v-model="signature"
              class="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 font-mono text-xs"
            >
              <option value="">choose a function…</option>
              <option v-for="sig in writable" :key="sig" :value="sig">{{ sig }}</option>
            </select>
          </div>

          <div v-for="(t, i) in params" :key="i">
            <Label :for="`evm-arg-${i}`" class="text-xs">
              {{ names[i] || `argument ${i}` }}
              <span class="ml-1 font-mono text-muted-foreground">{{ t }}</span>
            </Label>
            <Input
              :id="`evm-arg-${i}`"
              v-model="args[i]"
              class="mt-1 font-mono text-xs"
              :placeholder="t === 'address' ? 'z1… or 0x…' : t"
            />
          </div>
        </div>

        <div>
          <Label for="evm-write-gas" class="text-xs">
            gas
            <span class="ml-1 text-muted-foreground">what you are paying for</span>
          </Label>
          <Input id="evm-write-gas" v-model.number="gas" type="number" min="1" class="mt-1 font-mono text-xs" />
        </div>

        <!-- What will actually be signed. Syrius shows a base64 blob, so this
             is the last point at which the call is legible. -->
        <div v-if="signature" class="mt-4 border-t border-border/60 pt-3">
          <div class="text-ledger mb-1 text-muted-foreground">calldata</div>
          <div v-if="preview.error" class="text-xs text-destructive">{{ preview.error }}</div>
          <div v-else class="font-mono text-xs break-all text-muted-foreground">
            0x{{ preview.hex }}
          </div>
          <p v-if="quote?.required" class="mt-2 text-xs text-muted-foreground">
            Published as an inscription from your own account, carrying
            <strong class="text-foreground">
              {{ units(quote.amount, 8) }} {{ quote.token.startsWith('zts1znn') ? 'ZNN' : 'QSR' }}
            </strong>
            to the protocol burn sink — an address no keypair can reach, so the
            fee is provably destroyed rather than paid to anyone. It buys
            {{ gas.toLocaleString() }} gas at a resource price of
            {{ (quote.resourcePrice / 1000).toFixed(3) }}&times;.
            <strong class="text-foreground">There are no refunds:</strong>
            unused gas is burned with the rest.
          </p>
          <p v-else class="mt-2 text-xs text-muted-foreground">
            Published as an inscription from your own account — a send to
            yourself carrying these bytes, with no fee: this chain requires one
            only from momentum {{ quote?.requiredFrom || '—' }}. The account you
            sign with is the EVM account: the same 20 bytes, so no key is
            derived and nothing is registered.
          </p>
        </div>

        <div class="mt-4 flex flex-wrap items-center gap-3">
          <Button
            size="sm"
            :disabled="!signature || !!preview.error || publishing || !wallet.connected.value"
            @click="publish"
          >
            {{ publishing ? 'Waiting for Syrius…' : 'Publish call' }}
          </Button>
          <span v-if="!wallet.connected.value" class="text-xs text-muted-foreground">
            connect a wallet to publish
          </span>
          <span v-else-if="quoting" class="text-xs text-muted-foreground">quoting…</span>
        </div>

        <div v-if="error" class="mt-3 text-sm text-destructive">{{ error }}</div>
        <div v-if="published" class="mt-3 flex flex-wrap items-center gap-2 text-sm">
          <Badge variant="success">Published</Badge>
          <HashLink :value="published" :to="`/evm/tx/${published}`" :start="10" :end="6" />
          <span class="text-xs text-muted-foreground">
            the receipt appears once the indexer reaches this momentum
          </span>
        </div>
      </template>
    </CardContent>
  </Card>
</template>
