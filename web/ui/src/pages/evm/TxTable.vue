<script setup lang="ts">
import {Badge, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from 'nom-ui'
import {ArrowRightIcon} from '@lucide/vue'
import EmptyState from '@/components/EmptyState.vue'
import {count} from '@/core/format'
import type {EvmTx} from '@/types'
import {ago, exact, gasPct, methodLabel} from './evm'
import HashLink from './HashLink.vue'

// The transaction table, shared by the overview, the feed, a block, and an
// account. One component rather than four copies, because these four views
// disagreeing about how a transaction reads would be the explorer telling
// four different stories about the same receipt.
withDefaults(
  defineProps<{
    txs: EvmTx[]
    /** Hide the block column — a block's own page already states it. */
    hideBlock?: boolean
    /** Compact mode for the dashboard: fewer columns, no gas. */
    compact?: boolean
    empty?: string
    /** Highlight direction relative to this address, on an account page. */
    perspective?: string
  }>(),
  {empty: 'No transactions yet.'},
)
</script>

<template>
  <EmptyState v-if="!txs.length" :message="empty" />
  <div v-else class="overflow-x-auto">
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Txn</TableHead>
          <TableHead>Method</TableHead>
          <TableHead v-if="!hideBlock">Block</TableHead>
          <TableHead>Age</TableHead>
          <TableHead>From</TableHead>
          <TableHead class="w-8" />
          <TableHead>To</TableHead>
          <TableHead v-if="!compact" class="text-right">Gas</TableHead>
          <TableHead class="text-right">Status</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow v-for="t in txs" :key="t.inscription">
          <TableCell>
            <HashLink :value="t.inscription" :to="`/evm/tx/${t.inscription}`" :start="10" :end="6" />
          </TableCell>

          <TableCell>
            <Badge variant="outline" class="max-w-[10rem] truncate font-mono text-[0.7rem]">
              {{ methodLabel(t) }}
            </Badge>
          </TableCell>

          <TableCell v-if="!hideBlock">
            <RouterLink
              :to="`/evm/block/${t.momentum}`"
              class="font-mono text-xs text-primary tabular-nums hover:underline"
            >
              {{ count(t.momentum) }}
            </RouterLink>
          </TableCell>

          <TableCell class="whitespace-nowrap text-xs text-muted-foreground" :title="exact(t.timestamp)">
            {{ ago(t.timestamp) }}
          </TableCell>

          <TableCell>
            <!-- A link back to the page you are already on is a dead end, so
                 the account being viewed names itself instead. -->
            <Badge
              v-if="perspective && t.from.toLowerCase() === perspective.toLowerCase()"
              variant="secondary"
            >
              this address
            </Badge>
            <HashLink v-else :value="t.from" :zenon="t.fromZenon" :to="`/evm/account/${t.from}`" :start="8" :end="4" />
          </TableCell>

          <TableCell class="text-muted-foreground"><ArrowRightIcon class="size-3.5" /></TableCell>

          <TableCell>
            <Badge
              v-if="perspective && t.to.toLowerCase() === perspective.toLowerCase()"
              variant="secondary"
            >
              this address
            </Badge>
            <HashLink v-else :value="t.to" :zenon="t.toZenon" :to="`/evm/account/${t.to}`" :start="8" :end="4" />
          </TableCell>

          <TableCell v-if="!compact" class="text-right">
            <div class="ml-auto w-24">
              <div class="font-mono text-xs tabular-nums">{{ count(t.gasUsed) }}</div>
              <!-- The bar is the point: a number next to a budget nobody can
                   see is not a measurement anyone can read at a glance. -->
              <div class="mt-1 h-1 w-full overflow-hidden rounded-full bg-border">
                <div class="h-full rounded-full bg-primary/60" :style="{width: gasPct(t.gasUsed, t.gasBudget) + '%'}" />
              </div>
            </div>
          </TableCell>

          <TableCell class="text-right">
            <Badge :variant="t.success ? 'success' : 'destructive'">
              {{ t.success ? 'ok' : 'reverted' }}
            </Badge>
          </TableCell>
        </TableRow>
      </TableBody>
    </Table>
  </div>
</template>
