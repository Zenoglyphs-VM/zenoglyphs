<script setup lang="ts">
import {computed, onMounted, ref, watch} from 'vue'
import {Badge, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import LoadingState from '@/components/LoadingState.vue'
import ErrorState from '@/components/ErrorState.vue'
import EmptyState from '@/components/EmptyState.vue'
import {api} from '@/core/api'
import {count} from '@/core/format'
import type {EvmAccount} from '@/types'
import EvmSubNav from './EvmSubNav.vue'
import HashLink from './HashLink.vue'
import Pager from './Pager.vue'

// Three listings, one page: every account, only contracts, or only the
// contracts that carry the ERC-20 selectors. They differ by a filter and
// nothing else, so keeping them as one component is what stops them drifting
// into three slightly different tables.
const props = defineProps<{mode?: 'contract' | 'token'}>()

const LIMIT = 50

const accounts = ref<EvmAccount[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(true)
const error = ref('')

const title = computed(() =>
  props.mode === 'token' ? 'Tokens' : props.mode === 'contract' ? 'Contracts' : 'Addresses',
)
const description = computed(() =>
  props.mode === 'token'
    ? 'Contracts carrying the ERC-20 selectors'
    : props.mode === 'contract'
      ? 'Every contract deployed on this EVM'
      : 'Every account this EVM has seen, busiest first',
)

// Tokens are contracts filtered client-side: `isToken` is a heuristic over
// deployed code (see LooksLikeERC20), not a category the API indexes, so
// asking it to filter by something it does not store would be inventing an
// index for one page.
const rows = computed(() =>
  props.mode === 'token' ? accounts.value.filter((a) => a.isToken) : accounts.value,
)

async function load(next = 0) {
  loading.value = true
  error.value = ''
  try {
    const data = await api.evmAccounts({
      kind: props.mode ? 'contract' : undefined,
      limit: props.mode === 'token' ? 500 : LIMIT,
      offset: props.mode === 'token' ? 0 : next,
    })
    accounts.value = data.accounts
    total.value = data.total
    offset.value = data.offset
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
onMounted(() => load())
watch(() => props.mode, () => load())
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading
      :title="title"
      :description="description"
      :count="rows.length ? `${count(mode === 'token' ? rows.length : total)} total` : undefined"
    />
    <EvmSubNav />

    <LoadingState v-if="loading && !accounts.length" />
    <ErrorState v-else-if="error" :message="error" />
    <EmptyState v-else-if="!rows.length" message="Nothing here yet." />
    <template v-else>
      <div class="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Address</TableHead>
              <TableHead>Type</TableHead>
              <TableHead class="text-right">Txns</TableHead>
              <TableHead class="text-right">Nonce</TableHead>
              <TableHead v-if="mode" class="text-right">Code</TableHead>
              <TableHead v-if="mode">Artifact</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="a in rows" :key="a.address">
              <TableCell>
                <HashLink :value="a.address" :zenon="a.zenon" :to="`/evm/account/${a.address}`" :start="12" :end="8" />
              </TableCell>
              <TableCell>
                <div class="flex flex-wrap items-center gap-1.5">
                  <Badge variant="secondary">{{ a.kind }}</Badge>
                  <RouterLink v-if="a.isToken" :to="`/evm/token/${a.address}`">
                    <Badge>ERC-20</Badge>
                  </RouterLink>
                </div>
              </TableCell>
              <TableCell class="text-right font-mono text-xs tabular-nums">
                {{ count(a.txCount ?? 0) }}
              </TableCell>
              <TableCell class="text-right font-mono text-xs tabular-nums">{{ a.nonce }}</TableCell>
              <TableCell v-if="mode" class="text-right font-mono text-xs tabular-nums">
                {{ count(a.codeSize) }} B
              </TableCell>
              <TableCell v-if="mode">
                <HashLink
                  v-if="a.inscription"
                  :value="a.inscription"
                  :to="`/asset/${a.inscription}`"
                  :start="8"
                  :end="4"
                  muted
                />
                <span v-else class="text-muted-foreground">—</span>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
      <Pager
        v-if="mode !== 'token'"
        :total="total"
        :offset="offset"
        :limit="LIMIT"
        :disabled="loading"
        @go="load"
      />
    </template>
  </div>
</template>
