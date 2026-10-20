<script setup lang="ts">
import InscriptionCard from './InscriptionCard.vue'
import type {Asset} from '@/types'

defineProps<{assets: Asset[]}>()
</script>

<template>
  <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
    <InscriptionCard v-for="asset in assets" :key="asset.id" :asset="asset" />
  </div>
</template>
