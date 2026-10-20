<script setup lang="ts">
import {Address} from 'nom-ui'
import {useIndexer} from '@/core/composables/useIndexer'
import {count} from '@/core/format'

// The footer carries identity, not configuration: which protocol, which
// indexer, and the state root that says whether this one agrees with any
// other. Block-size limits and the rest of the policy moved to /docs — they
// are the same on every page and never change while you are reading one.
const {info} = useIndexer()
</script>

<template>
  <footer v-if="info" class="mt-12 border-t border-border">
    <div
      class="mx-auto flex max-w-7xl flex-wrap items-center gap-x-4 gap-y-1 px-4 py-4 text-xs text-muted-foreground sm:px-6"
    >
      <span>
        Zenoglyphs v{{ info.protocolVersion }} · prefix
        <code class="font-mono text-foreground">{{ info.prefix }}</code>
      </span>
      <span>zcribe {{ info.indexerVersion }}</span>
      <span class="font-mono tabular-nums">{{ count(info.events) }} events</span>
      <!-- The state root is computed by this indexer, not published by the
           chain, so it has no explorer page — the thing you do with it is
           compare it against another indexer's, which is what /chain is for.
           The hashes that DO exist on chain link out from the pages that show
           them. -->
      <RouterLink
        to="/chain"
        class="flex items-center gap-1 transition-colors hover:text-foreground"
        title="Compare this state root against another indexer"
      >
        stateRoot
        <Address
          :address="info.stateRoot || '—'"
          :start="10"
          :end="6"
          :copy="false"
          class="text-xs"
        />
      </RouterLink>
      <RouterLink to="/docs" class="ml-auto transition-colors hover:text-foreground"
        >Docs</RouterLink
      >
    </div>
  </footer>
</template>
