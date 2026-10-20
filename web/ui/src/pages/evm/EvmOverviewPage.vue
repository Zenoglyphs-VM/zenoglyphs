<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {
  ActivityIcon,
  BoxesIcon,
  CoinsIcon,
  FileCodeIcon,
  TriangleAlertIcon,
  UsersIcon,
} from '@lucide/vue'
import {Badge, Button, Card, CardContent, Heading} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import LoadingState from '@/components/LoadingState.vue'
import ErrorState from '@/components/ErrorState.vue'
import EmptyState from '@/components/EmptyState.vue'
import {api} from '@/core/api'
import {bytes, count} from '@/core/format'
import type {EvmBlock, EvmOverview, EvmTx} from '@/types'
import {ago, exact} from './evm'
import EvmSubNav from './EvmSubNav.vue'
import EvmSearch from './EvmSearch.vue'
import HashLink from './HashLink.vue'

// The EVM landing page: what exists, and what just happened.
//
// The two feeds are the front page — a reader arriving here wants to know
// whether anything is happening and to click into it, not to read a table of
// constants. The chain-state facts that used to lead now sit underneath them,
// because they change once a deploy and are read once a visit.
//
// Every number here is derived. There is no EVM table in the database: the
// `application/evm` inscriptions are the state, and this page is a view over
// a replay of them, which is why the state root is what two independent
// indexers compare to prove they agree.
const overview = ref<EvmOverview | null>(null)
const blocks = ref<EvmBlock[]>([])
const blockTotal = ref(0)
const txs = ref<EvmTx[]>([])
const loading = ref(true)
const error = ref('')

onMounted(async () => {
  try {
    const [o, b, t] = await Promise.all([
      api.evmOverview(),
      api.evmBlocks({limit: 6}),
      api.evmTxs({limit: 6}),
    ])
    overview.value = o
    blocks.value = b.blocks
    blockTotal.value = b.total
    txs.value = t.txs
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading title="EVM" description="A virtual machine derived from inscriptions" />
    <EvmSubNav />

    <div class="mb-8 grid gap-4 lg:grid-cols-[2fr_1fr]">
      <EvmSearch />
    </div>

    <LoadingState v-if="loading" />
    <ErrorState v-else-if="error" :message="error" />

    <template v-else-if="overview">
      <!-- The counters, as one band. Each one is a link: a number nobody can
           click is a dead end on a page whose whole job is navigation. -->
      <section class="mb-8 grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <RouterLink
          v-for="s in [
            {label: 'Blocks', value: count(blockTotal), to: '/evm/blocks', icon: BoxesIcon},
            {label: 'Transactions', value: count(overview.txs), to: '/evm/txs', icon: ActivityIcon},
            {label: 'Contracts', value: count(overview.contracts), to: '/evm/contracts', icon: FileCodeIcon},
            {label: 'Tokens', value: count(overview.tokens), to: '/evm/tokens', icon: CoinsIcon},
            {label: 'Addresses', value: count(overview.accounts), to: '/evm/addresses', icon: UsersIcon},
            {label: 'Reverted', value: count(overview.failed), to: '/evm/txs', icon: TriangleAlertIcon},
          ]"
          :key="s.label"
          :to="s.to"
          class="rounded-lg border border-border p-4 transition-colors hover:border-primary/50 hover:bg-accent/40"
        >
          <div class="flex items-center gap-1.5 text-ledger text-muted-foreground">
            <component :is="s.icon" class="size-3.5" />
            {{ s.label }}
          </div>
          <div class="mt-1.5 text-2xl font-semibold tabular-nums">{{ s.value }}</div>
        </RouterLink>
      </section>

      <!-- The two feeds, side by side: the shape every explorer converges on
           because "what is happening" has exactly two answers. -->
      <section class="mb-10 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardContent class="pt-5">
            <div class="mb-3 flex items-center justify-between border-b border-border pb-3">
              <Heading :level="2" class="text-base">Latest blocks</Heading>
              <RouterLink to="/evm/blocks">
                <Button variant="ghost" size="sm">View all</Button>
              </RouterLink>
            </div>
            <EmptyState v-if="!blocks.length" message="No EVM activity yet." />
            <ul v-else class="divide-y divide-border/60">
              <li v-for="b in blocks" :key="b.height" class="flex items-center gap-3 py-2.5">
                <div class="flex size-9 shrink-0 items-center justify-center rounded-md bg-accent/60 text-muted-foreground">
                  <BoxesIcon class="size-4" />
                </div>
                <div class="min-w-0 flex-1">
                  <RouterLink
                    :to="`/evm/block/${b.height}`"
                    class="font-mono text-sm text-primary tabular-nums hover:underline"
                  >
                    {{ count(b.height) }}
                  </RouterLink>
                  <div class="text-xs text-muted-foreground" :title="exact(b.timestamp)">
                    {{ ago(b.timestamp) }}
                  </div>
                </div>
                <div class="text-right">
                  <RouterLink
                    :to="`/evm/block/${b.height}`"
                    class="text-sm text-primary hover:underline"
                  >
                    {{ b.txs }} txn{{ b.txs === 1 ? '' : 's' }}
                  </RouterLink>
                  <div class="text-xs text-muted-foreground tabular-nums">
                    {{ count(b.gasUsed) }} gas
                  </div>
                </div>
                <Badge v-if="b.failed" variant="destructive">{{ b.failed }} failed</Badge>
              </li>
            </ul>
          </CardContent>
        </Card>

        <Card>
          <CardContent class="pt-5">
            <div class="mb-3 flex items-center justify-between border-b border-border pb-3">
              <Heading :level="2" class="text-base">Latest transactions</Heading>
              <RouterLink to="/evm/txs">
                <Button variant="ghost" size="sm">View all</Button>
              </RouterLink>
            </div>
            <EmptyState v-if="!txs.length" message="No EVM transactions yet." />
            <ul v-else class="divide-y divide-border/60">
              <li v-for="t in txs" :key="t.inscription" class="flex items-center gap-3 py-2.5">
                <div class="flex size-9 shrink-0 items-center justify-center rounded-md bg-accent/60 text-muted-foreground">
                  <ActivityIcon class="size-4" />
                </div>
                <div class="min-w-0 flex-1">
                  <HashLink
                    :value="t.inscription"
                    :to="`/evm/tx/${t.inscription}`"
                    :start="10"
                    :end="6"
                    :copy="false"
                  />
                  <div class="text-xs text-muted-foreground" :title="exact(t.timestamp)">
                    {{ ago(t.timestamp) }}
                  </div>
                </div>
                <!-- Through HashLink, like every other address on the site.
                     This used to slice the hex by hand, which meant it kept
                     showing 0x… after every other surface had switched to the
                     native spelling — the hazard of rendering an address
                     anywhere but the one component that knows how. -->
                <div class="min-w-0 text-right text-xs">
                  <div class="flex items-center justify-end gap-1 text-muted-foreground">
                    from
                    <HashLink
                      :value="t.from"
                      :zenon="t.fromZenon"
                      :to="`/evm/account/${t.from}`"
                      :start="8"
                      :end="4"
                      :copy="false"
                    />
                  </div>
                  <div class="flex items-center justify-end gap-1 text-muted-foreground">
                    to
                    <HashLink
                      :value="t.to"
                      :zenon="t.toZenon"
                      :to="`/evm/account/${t.to}`"
                      :start="8"
                      :end="4"
                      :copy="false"
                    />
                  </div>
                </div>
                <Badge :variant="t.success ? 'success' : 'destructive'">
                  {{ t.success ? 'ok' : 'reverted' }}
                </Badge>
              </li>
            </ul>
          </CardContent>
        </Card>
      </section>

      <section class="mb-10">
        <Heading :level="2" class="mb-3">Chain state</Heading>
        <DataList>
          <DataRow label="state root">
            <HashLink :value="overview.stateRoot" :start="20" :end="10" />
            <p class="mt-1 text-xs text-muted-foreground">
              What two independent derivers compare. Agreeing here means
              agreeing on the whole history that produced it.
            </p>
          </DataRow>
          <DataRow label="chain id">{{ overview.chainId }}</DataRow>
          <DataRow label="momentum">
            <RouterLink
              :to="`/evm/block/${overview.height}`"
              class="font-mono text-primary tabular-nums hover:underline"
            >
              {{ count(overview.height) }}
            </RouterLink>
          </DataRow>
          <DataRow label="inscriptions">
            {{ count(overview.applied) }} applied<span v-if="overview.skipped">,
            {{ count(overview.skipped) }} skipped</span>
          </DataRow>
          <DataRow label="size">
            <span class="font-mono tabular-nums">{{ bytes(overview.contentBytes) }}</span>
            <span class="text-muted-foreground"> of inscribed EVM data</span>
          </DataRow>
          <DataRow label="rebuild time">
            <span class="font-mono tabular-nums">{{ count(overview.rebuildMs) }} ms</span>
            <span class="text-muted-foreground">
              to replay all of it into this state — most requests hit a cache
              and pay none of it
            </span>
          </DataRow>
          <DataRow label="gas budget">
            {{ overview.gasPerPlasma }} gas per unit of plasma the inscription cost,
            capped at {{ count(overview.gasCap) }}
            <p class="mt-1 text-xs text-muted-foreground">
              NoM charges an inscription for its bytes; this EVM charges its
              computation. Deriving the budget from the plasma actually paid
              keeps the ratio constant at every size, so no inscription shape
              buys more of a deriver's time than it paid for.
            </p>
          </DataRow>
          <DataRow label="carried as">
            <code class="text-xs">{{ overview.inscriptionMime }}</code>
          </DataRow>
        </DataList>
        <p class="mt-3 max-w-3xl text-xs text-muted-foreground">
          None of this is stored on chain. Contracts and calls are ordinary
          inscriptions; the state above is recomputed by replaying them in
          canonical order, so any indexer reaching a different state root has
          diverged and can prove it in one comparison.
        </p>
      </section>
    </template>
  </div>
</template>
