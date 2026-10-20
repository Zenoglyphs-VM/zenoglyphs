<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {Badge, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import LoadingState from '@/components/LoadingState.vue'
import ErrorState from '@/components/ErrorState.vue'
import EmptyState from '@/components/EmptyState.vue'
import ExplorerLink from '@/components/ExplorerLink.vue'
import {api} from '@/core/api'
import {count} from '@/core/format'
import type {EvmBlock} from '@/types'
import {ago, exact} from './evm'
import EvmSubNav from './EvmSubNav.vue'
import Pager from './Pager.vue'

// This derived EVM has no blocks of its own — every "transaction" here is one
// NoM inscription (see internal/evm/chain.go). A momentum is the closest
// analog: it is what actually batches several of them together. Momentums
// with no EVM activity are not listed, because an empty block on a chain that
// does not produce blocks is not an event.
const LIMIT = 25

const blocks = ref<EvmBlock[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(true)
const error = ref('')

async function load(next = 0) {
  loading.value = true
  error.value = ''
  try {
    const data = await api.evmBlocks({limit: LIMIT, offset: next})
    blocks.value = data.blocks
    total.value = data.total
    offset.value = data.offset
    window.scrollTo({top: 0})
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
onMounted(() => load())
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading
      title="Blocks"
      description="Momentums that carried at least one EVM inscription"
      :count="total ? `${count(total)} total` : undefined"
    />
    <EvmSubNav />

    <LoadingState v-if="loading && !blocks.length" />
    <ErrorState v-else-if="error" :message="error" />
    <EmptyState v-else-if="!blocks.length" message="No EVM activity yet." />
    <template v-else>
      <div class="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Block</TableHead>
              <TableHead>Age</TableHead>
              <TableHead class="text-right">Txns</TableHead>
              <TableHead class="text-right">Reverted</TableHead>
              <TableHead class="text-right">Gas used</TableHead>
              <TableHead class="w-10" />
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="b in blocks" :key="b.height">
              <TableCell>
                <RouterLink
                  :to="`/evm/block/${b.height}`"
                  class="font-mono text-sm text-primary tabular-nums hover:underline"
                >
                  {{ count(b.height) }}
                </RouterLink>
              </TableCell>
              <TableCell class="whitespace-nowrap text-xs text-muted-foreground" :title="exact(b.timestamp)">
                {{ ago(b.timestamp) }}
              </TableCell>
              <TableCell class="text-right">
                <RouterLink :to="`/evm/block/${b.height}`" class="text-primary hover:underline">
                  {{ b.txs }}
                </RouterLink>
              </TableCell>
              <TableCell class="text-right">
                <Badge v-if="b.failed" variant="destructive">{{ b.failed }}</Badge>
                <span v-else class="text-muted-foreground">—</span>
              </TableCell>
              <TableCell class="text-right font-mono text-xs tabular-nums">
                {{ count(b.gasUsed) }}
              </TableCell>
              <TableCell><ExplorerLink target="momentum" :value="b.height" /></TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
      <Pager :total="total" :offset="offset" :limit="LIMIT" :disabled="loading" @go="load" />
    </template>
  </div>
</template>
