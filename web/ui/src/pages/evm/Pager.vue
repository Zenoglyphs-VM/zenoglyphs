<script setup lang="ts">
import {computed} from 'vue'
import {ChevronLeftIcon, ChevronRightIcon, ChevronsLeftIcon, ChevronsRightIcon} from '@lucide/vue'
import {Button} from 'nom-ui'
import {count} from '@/core/format'

// Offset paging, stated in rows rather than in pages.
//
// "Showing 26-50 of 312" is the line that tells a reader both where they are
// and how much is left; a bare page number tells them neither. The controls
// mirror what every explorer settles on, because stepping one page at a time
// through a long feed needs a way back to the start.
const props = defineProps<{total: number; offset: number; limit: number; disabled?: boolean}>()
const emit = defineEmits<{(e: 'go', offset: number): void}>()

const last = computed(() => Math.max(0, Math.floor(Math.max(0, props.total - 1) / props.limit) * props.limit))
const from = computed(() => (props.total ? props.offset + 1 : 0))
const to = computed(() => Math.min(props.offset + props.limit, props.total))
const atStart = computed(() => props.offset <= 0)
const atEnd = computed(() => props.offset >= last.value)
</script>

<template>
  <div v-if="total > limit" class="mt-4 flex flex-wrap items-center justify-between gap-3">
    <p class="text-xs text-muted-foreground tabular-nums">
      Showing {{ count(from) }}–{{ count(to) }} of {{ count(total) }}
    </p>
    <div class="flex items-center gap-1">
      <Button variant="ghost" size="icon-sm" :disabled="disabled || atStart" aria-label="First page" @click="emit('go', 0)">
        <ChevronsLeftIcon />
      </Button>
      <Button variant="ghost" size="icon-sm" :disabled="disabled || atStart" aria-label="Previous page" @click="emit('go', Math.max(0, offset - limit))">
        <ChevronLeftIcon />
      </Button>
      <span class="px-2 text-xs text-muted-foreground tabular-nums">
        {{ count(Math.floor(offset / limit) + 1) }} / {{ count(Math.floor(last / limit) + 1) }}
      </span>
      <Button variant="ghost" size="icon-sm" :disabled="disabled || atEnd" aria-label="Next page" @click="emit('go', offset + limit)">
        <ChevronRightIcon />
      </Button>
      <Button variant="ghost" size="icon-sm" :disabled="disabled || atEnd" aria-label="Last page" @click="emit('go', last)">
        <ChevronsRightIcon />
      </Button>
    </div>
  </div>
</template>
