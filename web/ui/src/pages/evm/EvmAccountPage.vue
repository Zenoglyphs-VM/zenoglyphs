<script setup lang="ts">
import {computed, onMounted, ref, watch} from 'vue'
import {Badge, Card, CardContent, Tabs, TabsContent, TabsList, TabsTrigger} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import LoadingState from '@/components/LoadingState.vue'
import ErrorState from '@/components/ErrorState.vue'
import EmptyState from '@/components/EmptyState.vue'
import {api} from '@/core/api'
import {bytes, count} from '@/core/format'
import type {EvmAccountDetail, EvmTransfer, EvmTx} from '@/types'
import {units} from './evm'
import EvmSubNav from './EvmSubNav.vue'
import TxTable from './TxTable.vue'
import TransferTable from './TransferTable.vue'
import HashLink from './HashLink.vue'
import Pager from './Pager.vue'
import EvmWrite from './EvmWrite.vue'

// One EVM account, which may also be a contract.
//
// Tabs rather than one long scroll: an account has five unrelated things to
// say about itself, and stacking them meant the storage dump of a contract
// buried its transactions. Each tab loads only when it is opened — an
// account page should not pay for a holder scan nobody asked to see.
const props = defineProps<{addr: string}>()

const LIMIT = 25

const data = ref<EvmAccountDetail | null>(null)
const loading = ref(true)
const error = ref('')
const tab = ref('txs')

const txs = ref<EvmTx[]>([])
const txTotal = ref(0)
const txOffset = ref(0)
const txLoading = ref(false)

const transfers = ref<EvmTransfer[]>([])
const trTotal = ref(0)
const trOffset = ref(0)
const trLoaded = ref(false)

const isContract = computed(() => data.value?.account.kind === 'contract')

async function load() {
  loading.value = true
  error.value = ''
  trLoaded.value = false
  transfers.value = []
  try {
    data.value = await api.evmAccount(props.addr)
    await loadTxs(0)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function loadTxs(next = 0) {
  txLoading.value = true
  try {
    const page = await api.evmTxs({address: props.addr, limit: LIMIT, offset: next})
    txs.value = page.txs
    txTotal.value = page.total
    txOffset.value = page.offset
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    txLoading.value = false
  }
}

async function loadTransfers(next = 0) {
  try {
    const page = await api.evmTransfers({address: props.addr, limit: LIMIT, offset: next})
    transfers.value = page.transfers
    trTotal.value = page.total
    trOffset.value = page.offset
    trLoaded.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

// Transfers are fetched the first time their tab is opened, not on load.
watch(tab, (t) => {
  if (t === 'transfers' && !trLoaded.value) loadTransfers(0)
})

onMounted(load)
watch(
  () => props.addr,
  () => {
    tab.value = 'txs'
    load()
  },
)

// Share of supply in basis points, so the division stays in BigInt. A uint256
// does not survive Number, and a percentage computed after a lossy conversion
// is a figure the chain never stated.
function share(balance: string): string {
  const supply = data.value?.token?.totalSupply
  if (!supply || supply === '0') return '—'
  try {
    return (Number((BigInt(balance) * 10000n) / BigInt(supply)) / 100).toFixed(2) + '%'
  } catch {
    return '—'
  }
}
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading
      :title="isContract ? 'Contract' : 'Address'"
      description="An EVM identity, derived from the inscriptions that touched it"
    />
    <EvmSubNav />

    <LoadingState v-if="loading" />
    <ErrorState v-else-if="error" :message="error" />

    <template v-else-if="data">
      <!-- The identity band: what this address IS, before what it has done. -->
      <div class="mb-6 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardContent class="pt-5">
            <div class="text-ledger mb-2 text-muted-foreground">Overview</div>
            <DataList dense>
              <DataRow label="address">
                <HashLink :value="data.account.address" :zenon="data.account.zenon" full />
                <!-- The bech32 spelling is shown for every address, so the
                     one thing it cannot say on its own is said here: a
                     contract lives in a namespace no keypair can reach, and
                     ZNN sent there is destroyed rather than delivered. -->
                <p v-if="data.account.sendable === false" class="mt-1 text-xs text-muted-foreground">
                  Not a payable address — no keypair can reach the
                  <code class="text-[0.7rem]">{{ data.account.namespace }}</code>
                  namespace, so a wallet sending here would destroy the funds.
                  It is a contract handle, not an account.
                </p>
              </DataRow>
              <DataRow label="type">
                <div class="flex flex-wrap items-center gap-1.5">
                  <Badge variant="secondary">{{ data.account.kind }}</Badge>
                  <RouterLink v-if="data.account.isToken" :to="`/evm/token/${data.account.address}`">
                    <Badge>ERC-20</Badge>
                  </RouterLink>
                </div>
              </DataRow>
              <DataRow label="balance">
                <span class="font-mono tabular-nums">{{ data.account.balance }}</span>
              </DataRow>
              <DataRow label="nonce">
                <span class="font-mono tabular-nums">{{ data.account.nonce }}</span>
              </DataRow>
              <DataRow label="transactions">
                <span class="font-mono tabular-nums">{{ count(txTotal) }}</span>
              </DataRow>
            </DataList>
          </CardContent>
        </Card>

        <Card v-if="isContract">
          <CardContent class="pt-5">
            <div class="text-ledger mb-2 text-muted-foreground">Contract</div>
            <DataList dense>
              <DataRow label="code size">
                <span class="font-mono tabular-nums">{{ bytes(data.account.codeSize) }}</span>
              </DataRow>
              <DataRow v-if="data.account.inscription" label="artifact">
                <HashLink
                  :value="data.account.inscription"
                  :to="`/asset/${data.account.inscription}`"
                  :start="12"
                  :end="8"
                />
                <p class="mt-1 text-xs text-muted-foreground">
                  The inscription this code lives in. The address is
                  0x16&#8214;keccak(assetId)[13:] — a commitment to exactly
                  these bytes, which cannot name any others, and whose leading
                  byte puts it outside every namespace a keypair can reach. So
                  it can never collide with somebody's account, and for the
                  same reason it is not an address to send ZNN to.
                </p>
              </DataRow>
              <DataRow v-if="data.token" label="total supply">
                <span class="font-mono tabular-nums">
                  {{ units(data.token.totalSupply, data.token.decimals) }}
                </span>
              </DataRow>
              <DataRow v-if="data.token" label="holders">
                <span class="font-mono tabular-nums">{{ data.token.holders.length }}</span>
              </DataRow>
            </DataList>
          </CardContent>
        </Card>
      </div>

      <!-- Calling the contract. It sits directly under the identity band
           because "what can I do with this" is the next question after "what
           is this", and above the tabs because it is an action rather than
           another view of the same state. -->
      <div v-if="isContract" class="mb-6">
        <EvmWrite :address="data.account.address" :signatures="data.interface" />
      </div>

      <Tabs v-model="tab">
        <TabsList class="mb-4 flex-wrap">
          <TabsTrigger value="txs">Transactions</TabsTrigger>
          <TabsTrigger value="transfers">Token transfers</TabsTrigger>
          <TabsTrigger value="holdings">Tokens held</TabsTrigger>
          <TabsTrigger v-if="data.token" value="holders">Holders</TabsTrigger>
          <TabsTrigger v-if="isContract" value="storage">Storage</TabsTrigger>
          <TabsTrigger v-if="isContract" value="code">Code</TabsTrigger>
        </TabsList>

        <TabsContent value="txs">
          <TxTable
            :txs="txs"
            :perspective="data.account.address"
            empty="This address has no EVM transactions."
          />
          <Pager
            :total="txTotal"
            :offset="txOffset"
            :limit="LIMIT"
            :disabled="txLoading"
            @go="loadTxs"
          />
        </TabsContent>

        <TabsContent value="transfers">
          <TransferTable
            :transfers="transfers"
            :perspective="data.account.address"
            empty="No token transfers involving this address."
          />
          <Pager :total="trTotal" :offset="trOffset" :limit="LIMIT" @go="loadTransfers" />
        </TabsContent>

        <TabsContent value="holdings">
          <EmptyState v-if="!data.holdings.length" message="This account holds no tokens." />
          <table v-else class="w-full text-sm">
            <thead class="text-ledger text-muted-foreground">
              <tr class="border-b border-border">
                <th class="py-2 text-left font-normal">Token</th>
                <th class="py-2 text-right font-normal">Balance</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="h in data.holdings" :key="h.token" class="border-b border-border/60">
                <td class="py-2">
                  <HashLink :value="h.token" :to="`/evm/token/${h.token}`" :start="12" :end="8" />
                </td>
                <td class="py-2 text-right font-mono tabular-nums">
                  {{ units(h.balance, h.decimals) }}
                  <span class="ml-1 text-xs text-muted-foreground">({{ h.balance }} raw)</span>
                </td>
              </tr>
            </tbody>
          </table>
        </TabsContent>

        <TabsContent v-if="data.token" value="holders">
          <EmptyState
            v-if="!data.token.holders.length"
            message="No holders with a non-zero balance."
          />
          <table v-else class="w-full text-sm">
            <thead class="text-ledger text-muted-foreground">
              <tr class="border-b border-border">
                <th class="py-2 text-left font-normal">Holder</th>
                <th class="py-2 text-right font-normal">Balance</th>
                <th class="py-2 text-right font-normal">Share</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="h in data.token.holders" :key="h.address" class="border-b border-border/60">
                <td class="py-2">
                  <HashLink :value="h.address" :zenon="h.zenon" :to="`/evm/account/${h.address}`" :start="12" :end="8" />
                </td>
                <td class="py-2 text-right font-mono tabular-nums">
                  {{ units(h.balance, data.token.decimals) }}
                </td>
                <td class="py-2 text-right font-mono text-muted-foreground tabular-nums">
                  {{ share(h.balance) }}
                </td>
              </tr>
            </tbody>
          </table>
        </TabsContent>

        <TabsContent v-if="isContract" value="storage">
          <EmptyState v-if="!data.storage.length" message="This contract has written no storage." />
          <template v-else>
            <table class="w-full text-sm">
              <thead class="text-ledger text-muted-foreground">
                <tr class="border-b border-border">
                  <th class="py-2 text-left font-normal">Slot</th>
                  <th class="py-2 text-left font-normal">Value</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="s in data.storage" :key="s.Key" class="border-b border-border/60">
                  <td class="py-2 pr-4 font-mono text-xs break-all">{{ s.Key }}</td>
                  <td class="py-2 font-mono text-xs break-all">{{ s.Value }}</td>
                </tr>
              </tbody>
            </table>
            <p class="mt-2 text-xs text-muted-foreground">
              Mapping slots are keccak(key ‖ slot) and cannot be inverted, so
              they stay hashes here. Token balances are read back through the
              contract instead — see the Holders tab.
            </p>
          </template>
        </TabsContent>

        <TabsContent v-if="isContract" value="code">
          <div class="rounded-lg border border-border p-4">
            <div class="mb-2 flex items-center justify-between">
              <span class="text-ledger text-muted-foreground">
                Deployed bytecode · {{ bytes(data.account.codeSize) }}
              </span>
            </div>
            <pre class="max-h-96 overflow-auto font-mono text-xs break-all whitespace-pre-wrap text-muted-foreground">{{ data.code }}</pre>
            <p class="mt-2 text-xs text-muted-foreground">
              There is no source to verify against: this chain has no compiler
              in it. What the address commits to is these bytes, and the
              artifact above is where they came from.
            </p>
          </div>
        </TabsContent>
      </Tabs>
    </template>
  </div>
</template>
