<script setup lang="ts">
import {computed} from 'vue'
import {Spinner} from 'nom-ui'
import {useIndexer} from '@/core/composables/useIndexer'
import {count} from '@/core/format'

const {info, error, behind, synced} = useIndexer()

// Four states, four claims, and they are different claims on purpose:
// unreachable means this page has no indexer at all; blind means the indexer
// answered but has never reached its own node, so it cannot know whether it is
// behind; syncing means it knows it is behind and what you are looking at is
// incomplete; synced means the projection is current to the height shown.
//
// The blind case is the one that used to lie. nodeHeight is written only by a
// successful frontier call, so it is zero when the node has never answered —
// and "behind" then computes to zero, which rendered as a green "synced".
const state = computed(() => {
  if (error.value || !info.value) return 'down' as const
  if (!info.value.nodeHeight) return 'blind' as const
  return synced.value ? ('live' as const) : ('syncing' as const)
})
</script>

<template>
  <div
    class="flex items-center gap-2 text-xs whitespace-nowrap"
    :title="
      state === 'down'
        ? 'the indexer API did not answer'
        : state === 'blind'
          ? 'the indexer is running but has not reached its node'
          : `indexer at ${info?.syncHeight}, node at ${info?.nodeHeight}`
    "
  >
    <template v-if="state === 'live'">
      <span class="size-1.5 rounded-full bg-success" aria-hidden="true" />
      <span class="text-muted-foreground">
        synced
        <span class="font-mono tabular-nums text-foreground">{{ count(info?.syncHeight) }}</span>
      </span>
    </template>

    <template v-else-if="state === 'syncing'">
      <Spinner class="size-3 text-warning" />
      <span class="text-muted-foreground">
        syncing
        <span class="font-mono tabular-nums text-foreground">
          {{ count(info?.syncHeight) }}/{{ count(info?.nodeHeight) }}
        </span>
        <span class="text-warning"> · {{ count(behind) }} behind</span>
      </span>
    </template>

    <template v-else-if="state === 'blind'">
      <span class="size-1.5 rounded-full bg-warning" aria-hidden="true" />
      <span class="text-muted-foreground">
        indexed
        <span class="font-mono tabular-nums text-foreground">{{ count(info?.syncHeight) }}</span>
        <span class="text-warning"> · node unreachable</span>
      </span>
    </template>

    <template v-else>
      <span class="size-1.5 rounded-full bg-destructive" aria-hidden="true" />
      <span class="text-destructive">indexer unreachable</span>
    </template>
  </div>
</template>
