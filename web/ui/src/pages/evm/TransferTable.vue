<script setup lang="ts">
import {ArrowRightIcon} from '@lucide/vue'
import {Badge, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from 'nom-ui'
import EmptyState from '@/components/EmptyState.vue'
import {count} from '@/core/format'
import type {EvmTransfer} from '@/types'
import {ago, exact, units} from './evm'
import HashLink from './HashLink.vue'

// The token-transfer feed, read from Transfer logs.
//
// Logs rather than calls, because an address that only ever *received* tokens
// never sent a transaction and cannot be found any other way. The direction
// pill exists for the same reason a bank statement has one: on an account's
// own page, in and out is the first thing to know.
withDefaults(
  defineProps<{
    transfers: EvmTransfer[]
    /** Whose page this is, if any — enables the in/out column. */
    perspective?: string
    /** Hide the token column, on a token's own page. */
    hideToken?: boolean
    empty?: string
  }>(),
  {empty: 'No token transfers yet.'},
)

function direction(t: EvmTransfer, self?: string): 'in' | 'out' | 'self' | null {
  if (!self) return null
  const me = self.toLowerCase()
  const from = t.from.toLowerCase() === me
  const to = t.to.toLowerCase() === me
  if (from && to) return 'self'
  if (from) return 'out'
  if (to) return 'in'
  return null
}
</script>

<template>
  <EmptyState v-if="!transfers.length" :message="empty" />
  <div v-else class="overflow-x-auto">
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Txn</TableHead>
          <TableHead>Block</TableHead>
          <TableHead>Age</TableHead>
          <TableHead v-if="perspective" class="w-16" />
          <TableHead>From</TableHead>
          <TableHead class="w-8" />
          <TableHead>To</TableHead>
          <TableHead v-if="!hideToken">Token</TableHead>
          <TableHead class="text-right">Amount</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow v-for="(t, i) in transfers" :key="`${t.inscription}-${i}`">
          <TableCell>
            <HashLink :value="t.inscription" :to="`/evm/tx/${t.inscription}`" :start="10" :end="6" />
          </TableCell>
          <TableCell>
            <RouterLink
              :to="`/evm/block/${t.height}`"
              class="font-mono text-xs text-primary tabular-nums hover:underline"
            >
              {{ count(t.height) }}
            </RouterLink>
          </TableCell>
          <TableCell class="whitespace-nowrap text-xs text-muted-foreground" :title="exact(t.timestamp)">
            {{ ago(t.timestamp) }}
          </TableCell>
          <TableCell v-if="perspective">
            <Badge
              v-if="direction(t, perspective)"
              :variant="direction(t, perspective) === 'in' ? 'success' : 'secondary'"
            >
              {{ direction(t, perspective) }}
            </Badge>
          </TableCell>
          <TableCell>
            <HashLink :value="t.from" :zenon="t.fromZenon" :to="`/evm/account/${t.from}`" :start="8" :end="4" />
          </TableCell>
          <TableCell class="text-muted-foreground"><ArrowRightIcon class="size-3.5" /></TableCell>
          <TableCell>
            <HashLink :value="t.to" :zenon="t.toZenon" :to="`/evm/account/${t.to}`" :start="8" :end="4" />
          </TableCell>
          <TableCell v-if="!hideToken">
            <HashLink :value="t.token" :to="`/evm/token/${t.token}`" :start="8" :end="4" muted />
          </TableCell>
          <TableCell class="text-right font-mono text-xs tabular-nums">
            {{ units(t.amount) }}
          </TableCell>
        </TableRow>
      </TableBody>
    </Table>
  </div>
</template>
