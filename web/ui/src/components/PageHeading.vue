<script setup lang="ts">
import {Heading} from 'nom-ui'

defineProps<{title: string; count?: string; description?: string}>()
</script>

<template>
  <div class="mb-6 flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
    <div class="min-w-0">
      <Heading :level="1" class="text-3xl">{{ title }}</Heading>
      <p v-if="description" class="mt-1 text-sm text-muted-foreground">{{ description }}</p>
    </div>
    <span v-if="count" class="text-ledger text-muted-foreground">{{ count }}</span>
    <slot />
  </div>
</template>
