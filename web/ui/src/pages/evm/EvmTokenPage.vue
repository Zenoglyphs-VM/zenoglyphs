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
import {count} from '@/core/format'
import type {EvmToken, EvmTransfer} from '@/types'
import {units} from './evm'
import EvmSubNav from './EvmSubNav.vue'
import TransferTable from './TransferTable.vue'
import HashLink from './HashLink.vue'
import Pager from './Pager.vue'

// One ERC-20: what it is, who holds it, and everything it has moved.
//
// Holders come from Transfer events and are read back through balanceOf — a
// Solidity mapping has no enumerable key set, so the storage trie cannot be
// walked to find them. Every explorer rebuilds this list from logs; this is
// that, not a shortcut around something better.
const props = defineProps<{addr: string}>()

const LIMIT = 25

const token = ref<EvmToken | null>(null)
const loading = ref(true)
const error = ref('')
const tab = ref('transfers')

const transfers = ref<EvmTransfer[]>([])
const trTotal = ref(0)
const trOffset = ref(0)

const supply = computed(() => token.value?.totalSupply ?? '0')

async function load() {
  loading.value = true
  error.value = ''
  try {
    token.value = await api.evmToken(props.addr)
    await loadTransfers(0)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function loadTransfers(next = 0) {
  try {
    const page = await api.evmTransfers({token: props.addr, limit: LIMIT, offset: next})
    transfers.value = page.transfers
    trTotal.value = page.total
    trOffset.value = page.offset
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(load)
watch(() => props.addr, load)

// Basis points, so the division stays in BigInt: a uint256 does not survive
// Number, and a percentage computed after a lossy conversion is a figure the
// chain never stated.
function sharePct(balance: string): number {
  if (!supply.value || supply.value === '0') return 0
  try {
    return Number((BigInt(balance) * 10000n) / BigInt(supply.value)) / 100
  } catch {
    return 0
  }
}

function share(balance: string): string {
  if (!supply.value || supply.value === '0') return '—'
  return sharePct(balance).toFixed(2) + '%'
}
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading title="Token" description="An ERC-20 held as an inscription" />
    <EvmSubNav />

    <LoadingState v-if="loading" />
    <ErrorState v-else-if="error" :message="error" />

    <template v-else-if="token">
      <div v-if="!token.isToken" class="mb-6 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm">
        This contract does not carry the ERC-20 selectors. Everything below is
        read through calls that may not mean what their names suggest.
      </div>

      <div class="mb-6 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardContent class="pt-5">
            <div class="text-ledger mb-2 text-muted-foreground">Token</div>
            <DataList dense>
              <DataRow label="contract">
                <HashLink :value="token.address" full />
              </DataRow>
              <DataRow label="standard">
                <Badge v-if="token.isToken">ERC-20</Badge>
                <Badge v-else variant="warning">unverified</Badge>
              </DataRow>
              <DataRow label="decimals">
                <span class="font-mono tabular-nums">{{ token.decimals }}</span>
              </DataRow>
            </DataList>
          </CardContent>
        </Card>

        <Card>
          <CardContent class="pt-5">
            <div class="text-ledger mb-2 text-muted-foreground">Supply</div>
            <DataList dense>
              <DataRow label="total supply">
                <span class="font-mono text-base tabular-nums">
                  {{ units(token.totalSupply, token.decimals) }}
                </span>
                <div class="mt-0.5 text-xs text-muted-foreground">{{ token.totalSupply }} raw</div>
              </DataRow>
              <DataRow label="holders">
                <span class="font-mono tabular-nums">{{ count(token.holders.length) }}</span>
              </DataRow>
              <DataRow label="transfers">
                <span class="font-mono tabular-nums">{{ count(trTotal) }}</span>
              </DataRow>
            </DataList>
          </CardContent>
        </Card>
      </div>

      <Tabs v-model="tab">
        <TabsList class="mb-4">
          <TabsTrigger value="transfers">Transfers</TabsTrigger>
          <TabsTrigger value="holders">Holders</TabsTrigger>
        </TabsList>

        <TabsContent value="transfers">
          <TransferTable :transfers="transfers" hide-token empty="This token has never moved." />
          <Pager :total="trTotal" :offset="trOffset" :limit="LIMIT" @go="loadTransfers" />
        </TabsContent>

        <TabsContent value="holders">
          <EmptyState v-if="!token.holders.length" message="No holders with a non-zero balance." />
          <table v-else class="w-full text-sm">
            <thead class="text-ledger text-muted-foreground">
              <tr class="border-b border-border">
                <th class="w-12 py-2 text-left font-normal">#</th>
                <th class="py-2 text-left font-normal">Holder</th>
                <th class="py-2 text-right font-normal">Balance</th>
                <th class="py-2 text-right font-normal">Share</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(h, i) in token.holders" :key="h.address" class="border-b border-border/60">
                <td class="py-2 font-mono text-xs text-muted-foreground tabular-nums">{{ i + 1 }}</td>
                <td class="py-2">
                  <HashLink :value="h.address" :zenon="h.zenon" :to="`/evm/account/${h.address}`" :start="12" :end="8" />
                </td>
                <td class="py-2 text-right font-mono tabular-nums">
                  {{ units(h.balance, token.decimals) }}
                </td>
                <td class="py-2 text-right">
                  <div class="flex items-center justify-end gap-2">
                    <div class="hidden h-1.5 w-20 overflow-hidden rounded-full bg-border sm:block">
                      <div class="h-full rounded-full bg-primary/60" :style="{width: sharePct(h.balance) + '%'}" />
                    </div>
                    <span class="w-14 font-mono text-xs text-muted-foreground tabular-nums">
                      {{ share(h.balance) }}
                    </span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </TabsContent>
      </Tabs>
    </template>
  </div>
</template>
