<script setup lang="ts">
import {computed, ref} from 'vue'
import {SearchIcon} from '@lucide/vue'
import {
  Address,
  Badge,
  Button,
  Heading,
  Input,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import ExplorerLink from '@/components/ExplorerLink.vue'
import {api} from '@/core/api'
import {bytes, count, duration, when} from '@/core/format'
import {useIndexer} from '@/core/composables/useIndexer'
import type {Momentum} from '@/types'

// The chain view is the cross-indexer agreement surface made visible: the state
// root at a height is what two operators compare when they disagree.
//
// Everything here is a fact about THIS indexer — where it is, what it has
// decided, what it agrees with. The protocol's own constants (the MIME
// allowlist, block and import limits) are the same on every indexer and live
// on /docs, because a number that never changes is not status.
const {info} = useIndexer()

const height = ref('')
const momentum = ref<Momentum | null>(null)
const lookupError = ref('')
const looking = ref(false)

// Chain 1 is mainnet and 69 is the devnet in go-zenon/docker. Anything else is
// reported by number rather than guessed at — an indexer pointed at an unknown
// network should say so, not claim to be on one it recognises.
const network = computed(() => {
  const id = info.value?.chainId
  if (!id) return ''
  if (id === 1) return 'mainnet'
  if (id === 69) return 'devnet'
  return `chain ${id}`
})

async function lookup() {
  const h = height.value.trim()
  if (!h) return
  looking.value = true
  lookupError.value = ''
  momentum.value = null
  try {
    momentum.value = await api.momentum(h)
  } catch (e) {
    lookupError.value = e instanceof Error ? e.message : String(e)
  } finally {
    looking.value = false
  }
}
</script>

<template>
  <div v-if="info">
    <PageHeading title="Chain" description="What this indexer has seen, and what it agrees with." />

    <DataList>
      <DataRow label="network">
        <span v-if="network">{{ network }}</span>
        <span v-else class="text-muted-foreground">unknown</span>
        <span v-if="info.explorer.name" class="text-muted-foreground">
          · links to {{ info.explorer.name }}
        </span>
      </DataRow>
      <DataRow label="protocol">
        Zenoglyphs v{{ info.protocolVersion }} · prefix
        <code class="font-mono text-xs">{{ info.prefix }}</code>
      </DataRow>
      <DataRow label="indexer">zcribe {{ info.indexerVersion }}</DataRow>
      <!-- nodeHeight is only ever written by a successful frontier call, so a
           zero means this indexer has never reached its node — not that the
           chain is empty. Printing "17,432 of 0" as though both numbers were
           measurements is how a failed dependency reads as data. -->
      <DataRow label="sync">
        <span class="font-mono tabular-nums">{{ count(info.syncHeight) }}</span>
        <template v-if="info.nodeHeight">
          <span class="text-muted-foreground"> of </span>
          <span class="font-mono tabular-nums">{{ count(info.nodeHeight) }}</span>
        </template>
        <Badge v-else variant="destructive" class="ml-1.5">node unreachable</Badge>
        <ExplorerLink target="momentum" :value="info.syncHeight" class="ml-1.5" />
      </DataRow>
      <DataRow label="start">
        <span class="font-mono tabular-nums">{{ count(info.startHeight) }}</span>
      </DataRow>
      <DataRow label="confirms">
        {{ info.confirmationDepth }} momentums to full confirmation
      </DataRow>
      <DataRow label="checkpoints">
        <template v-if="info.snapshotRetain">
          every {{ info.snapshotInterval }}, covering a {{ info.snapshotRetain }}-momentum reorg
        </template>
        <template v-else>disabled</template>
      </DataRow>
      <DataRow label="scripts">
        <Badge :variant="info.scriptsEnabled ? 'success' : 'warning'">
          {{
            info.scriptsEnabled
              ? 'inscribed scripts run (recursion on)'
              : 'inscribed scripts blocked'
          }}
        </Badge>
      </DataRow>
      <DataRow label="events">
        <span class="font-mono tabular-nums">{{ count(info.events) }}</span>
      </DataRow>
      <DataRow label="size">
        <span class="font-mono tabular-nums">{{ bytes(info.dbSizeBytes) }}</span>
        <span class="text-muted-foreground"> on disk</span>
        <span class="text-muted-foreground">
          · {{ bytes(info.contentBytes) }} inscribed
        </span>
      </DataRow>
      <DataRow label="sync time">
        <template v-if="info.syncDurationSeconds">
          <span class="font-mono tabular-nums">{{ duration(info.syncDurationSeconds) }}</span>
          <span class="text-muted-foreground"> to fetch and index this chain, at the current rate</span>
        </template>
        <span v-else class="text-muted-foreground">not enough data yet</span>
      </DataRow>
      <DataRow label="stateRoot">
        <Address :address="info.stateRoot || '—'" :start="16" :end="8" />
      </DataRow>
      <DataRow label="content">
        <span class="font-mono text-xs">{{ info.contentOrigin }}</span>
      </DataRow>
    </DataList>

    <section class="mt-10">
      <Heading :level="2" class="mb-3 text-lg">Inspect a momentum</Heading>
      <form class="flex gap-2" @submit.prevent="lookup">
        <Input
          v-model="height"
          type="number"
          inputmode="numeric"
          placeholder="momentum height"
          class="max-w-56 font-mono"
        />
        <Button type="submit" :disabled="looking">
          <SearchIcon />
          Look up
        </Button>
      </form>

      <p v-if="lookupError" class="mt-3 text-sm text-destructive">{{ lookupError }}</p>

      <div v-if="momentum" class="mt-6">
        <Heading :level="3" class="mb-3 text-base">
          <span class="inline-flex items-center gap-1.5">
            Momentum {{ count(momentum.height) }}
            <ExplorerLink target="momentum" :value="momentum.height" />
          </span>
        </Heading>
        <DataList dense>
          <DataRow label="hash"><Address :address="momentum.hash" :start="16" :end="8" /></DataRow>
          <DataRow label="stateRoot">
            <Address :address="momentum.stateRoot" :start="16" :end="8" />
          </DataRow>
          <DataRow label="inscriptions">
            <span class="font-mono tabular-nums">{{ momentum.txs.length }}</span>
          </DataRow>
          <DataRow label="time">{{ when(momentum.timestamp) }}</DataRow>
        </DataList>

        <Table v-if="momentum.txs.length" class="mt-4">
          <TableHeader>
            <TableRow>
              <TableHead class="w-12">#</TableHead>
              <TableHead>block hash</TableHead>
              <TableHead class="w-10" />
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="(t, i) in momentum.txs" :key="t">
              <TableCell class="font-mono tabular-nums text-muted-foreground">{{ i }}</TableCell>
              <TableCell>
                <RouterLink :to="`/asset/${t}`" class="font-mono text-sm break-all hover:text-info">
                  {{ t }}
                </RouterLink>
              </TableCell>
              <TableCell><ExplorerLink target="transaction" :value="t" /></TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
    </section>
  </div>
</template>
