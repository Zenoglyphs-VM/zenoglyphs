<script setup lang="ts">
import {LayersIcon} from '@lucide/vue'
import {Badge} from 'nom-ui'
import InscriptionThumb from './InscriptionThumb.vue'
import {baseMime, short} from '@/core/format'
import type {Asset} from '@/types'

defineProps<{asset: Asset}>()
</script>

<template>
  <RouterLink
    :to="`/asset/${asset.id}`"
    class="group flex flex-col overflow-hidden rounded-xl border border-border bg-card shadow-sm transition-[border-color,box-shadow] hover:border-primary hover:shadow-md"
  >
    <div class="relative aspect-square overflow-hidden bg-muted/40">
      <InscriptionThumb :asset="asset" />

      <!-- Two facts the grid has to carry, because they change what a card
           means: not yet fully confirmed, and built out of other inscriptions. -->
      <Badge v-if="asset.status !== 'full'" variant="warning" class="absolute top-2 left-2"
        >soft</Badge
      >
      <Badge
        v-if="asset.dataWithheld"
        variant="outline"
        class="absolute bottom-2 left-2 bg-background/85"
        title="this indexer does not serve this inscription's bytes"
      >
        redacted
      </Badge>
      <Badge
        v-if="asset.refs?.length"
        variant="pending"
        class="absolute top-2 right-2"
        :title="`imports ${asset.refs.length} inscription(s)`"
      >
        <LayersIcon />
        {{ asset.refs.length }}
      </Badge>
    </div>

    <div class="flex min-w-0 flex-col gap-1 border-t border-border px-3 py-2.5">
      <span class="truncate text-sm font-medium group-hover:text-primary">
        {{ asset.name || short(asset.id) }}
      </span>
      <span class="flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <span class="font-mono tabular-nums">#{{ asset.number }}</span>
        <span class="truncate font-mono">{{ baseMime(asset.mime) }}</span>
      </span>
    </div>
  </RouterLink>
</template>
