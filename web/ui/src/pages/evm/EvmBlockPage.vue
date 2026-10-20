<script setup lang="ts">
import {onMounted, ref, watch} from 'vue'
import {ChevronLeftIcon, ChevronRightIcon} from '@lucide/vue'
import {Badge, Button, Heading} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import LoadingState from '@/components/LoadingState.vue'
import ErrorState from '@/components/ErrorState.vue'
import ExplorerLink from '@/components/ExplorerLink.vue'
import {api} from '@/core/api'
import {count} from '@/core/format'
import type {EvmBlock, EvmTx} from '@/types'
import {ago, exact} from './evm'
import EvmSubNav from './EvmSubNav.vue'
import TxTable from './TxTable.vue'
import HashLink from './HashLink.vue'

// One momentum, seen as a block.
//
// prev/next skip momentums with no EVM activity, so stepping through the
// chain here moves between things that happened rather than through the
// hundreds of empty momentums between them.
const props = defineProps<{height: string}>()

const block = ref<EvmBlock | null>(null)
const txs = ref<EvmTx[]>([])
const prev = ref(0)
const next = ref(0)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await api.evmBlock(props.height)
    block.value = data.block
    txs.value = data.txs
    prev.value = data.prev
    next.value = data.next
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => props.height, load)
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading :title="`Block ${count(Number(height))}`" description="A momentum, and the EVM activity it carried">
      <div class="flex items-center gap-1">
        <RouterLink v-if="prev" :to="`/evm/block/${prev}`">
          <Button variant="outline" size="icon-sm" aria-label="Previous block with activity">
            <ChevronLeftIcon />
          </Button>
        </RouterLink>
        <RouterLink v-if="next" :to="`/evm/block/${next}`">
          <Button variant="outline" size="icon-sm" aria-label="Next block with activity">
            <ChevronRightIcon />
          </Button>
        </RouterLink>
      </div>
    </PageHeading>
    <EvmSubNav />

    <LoadingState v-if="loading" />
    <ErrorState v-else-if="error" :message="error" />

    <template v-else-if="block">
      <section class="mb-10">
        <DataList>
          <DataRow label="height">
            <span class="font-mono tabular-nums">{{ count(block.height) }}</span>
            <ExplorerLink target="momentum" :value="block.height" class="ml-1.5" />
          </DataRow>
          <DataRow label="timestamp">
            <span :title="exact(block.timestamp)">{{ ago(block.timestamp) }}</span>
            <span class="ml-2 text-muted-foreground">{{ exact(block.timestamp) }}</span>
          </DataRow>
          <DataRow v-if="block.hash" label="momentum hash">
            <HashLink :value="block.hash" :start="20" :end="10" />
            <p class="mt-1 text-xs text-muted-foreground">
              The momentum this activity is committed to. It is what makes the
              ordering below verifiable rather than asserted.
            </p>
          </DataRow>
          <DataRow label="transactions">
            <span class="font-mono tabular-nums">{{ block.txs }}</span>
            <Badge v-if="block.failed" variant="destructive" class="ml-2">
              {{ block.failed }} reverted
            </Badge>
          </DataRow>
          <DataRow label="gas used">
            <span class="font-mono tabular-nums">{{ count(block.gasUsed) }}</span>
          </DataRow>
        </DataList>
      </section>

      <section>
        <Heading :level="2" class="mb-3">Transactions</Heading>
        <TxTable :txs="txs" hide-block empty="No EVM transactions in this momentum." />
      </section>
    </template>
  </div>
</template>
