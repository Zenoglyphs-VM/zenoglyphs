<script setup lang="ts">
import {onMounted, ref} from 'vue'
import PageHeading from '@/components/PageHeading.vue'
import LoadingState from '@/components/LoadingState.vue'
import ErrorState from '@/components/ErrorState.vue'
import {api} from '@/core/api'
import {count} from '@/core/format'
import type {EvmTx} from '@/types'
import EvmSubNav from './EvmSubNav.vue'
import TxTable from './TxTable.vue'
import Pager from './Pager.vue'

// The whole transaction feed, newest first and paged. The overview shows the
// newest handful; this is the page for actually going back through them.
const LIMIT = 25

const txs = ref<EvmTx[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(true)
const error = ref('')

async function load(next = 0) {
  loading.value = true
  error.value = ''
  try {
    const data = await api.evmTxs({limit: LIMIT, offset: next})
    txs.value = data.txs
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
      title="Transactions"
      description="Every EVM call and deploy, newest first"
      :count="total ? `${count(total)} total` : undefined"
    />
    <EvmSubNav />

    <LoadingState v-if="loading && !txs.length" />
    <ErrorState v-else-if="error" :message="error" />
    <template v-else>
      <TxTable :txs="txs" empty="No EVM transactions yet." />
      <Pager :total="total" :offset="offset" :limit="LIMIT" :disabled="loading" @go="load" />
    </template>
  </div>
</template>
