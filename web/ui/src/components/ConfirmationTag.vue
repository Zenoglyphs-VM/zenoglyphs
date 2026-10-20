<script setup lang="ts">
import {computed} from 'vue'
import {TxStatus} from 'nom-ui'
import {useIndexer} from '@/core/composables/useIndexer'
import type {Asset} from '@/types'

// Soft and full confirmation are different claims, so they get different words.
//
// "soft" means this indexer has parsed and applied the block: the inscription
// exists and renders now, and a reorg could still take it away. "confirmed"
// means enough momentums sit above it that its inscription number is settled.
// Calling the first one "pending" undersold it — the inscription is real and
// viewable — and calling it confirmed would oversell it.
const props = defineProps<{asset: Asset}>()

const {info} = useIndexer()

const full = computed(() => props.asset.status === 'full')

const title = computed(() =>
  full.value
    ? `${props.asset.confirmations} momentums deep`
    : `indexed and applied; ${props.asset.confirmations || 0} of ${
        info.value?.confirmationDepth ?? '?'
      } momentums to full confirmation`,
)
</script>

<template>
  <span :title="title">
    <TxStatus :status="full ? 'success' : 'confirming'" :label="full ? 'confirmed' : 'soft'" />
  </span>
</template>
