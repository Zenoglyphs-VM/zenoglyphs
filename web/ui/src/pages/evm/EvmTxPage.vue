<script setup lang="ts">
import {computed, onMounted, ref, watch} from 'vue'
import {ArrowRightIcon} from '@lucide/vue'
import {Badge, Card, CardContent, Heading} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import LoadingState from '@/components/LoadingState.vue'
import ErrorState from '@/components/ErrorState.vue'
import {api} from '@/core/api'
import {count} from '@/core/format'
import type {EvmTx} from '@/types'
import {ago, exact, gasPct, units} from './evm'
import EvmSubNav from './EvmSubNav.vue'
import HashLink from './HashLink.vue'
import ParamList from './ParamList.vue'

// One EVM transaction, which is one inscription.
//
// Both identities stay visible on purpose: the receipt is derived and
// disposable, the inscription is permanent and is what any other indexer
// replays to reach the same receipt.
const props = defineProps<{id: string}>()

const tx = ref<EvmTx | null>(null)
const loading = ref(true)
const error = ref('')

// The one-line summary of what actually happened, built from the logs. A
// reader looking at "0xa9059cbb…" wants "moved 100 to 0x12ab", and the raw
// calldata is only useful once that sentence is already on the page.
const transfers = computed(() =>
  (tx.value?.logs ?? []).filter((l) => l.from && l.to && l.amount),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    tx.value = await api.evmTx(props.id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => props.id, load)
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading title="Transaction" description="One inscription, and the state change it produced" />
    <EvmSubNav />

    <LoadingState v-if="loading" />
    <ErrorState v-else-if="error" :message="error" />

    <template v-else-if="tx">
      <!-- Status first and large: it is the one thing every reader is here
           to check, and it decides whether the rest matters. -->
      <div class="mb-6 flex flex-wrap items-center gap-3">
        <Badge :variant="tx.success ? 'success' : 'destructive'" class="text-sm">
          {{ tx.success ? 'Success' : 'Reverted' }}
        </Badge>
        <Badge variant="outline" class="font-mono">{{ tx.op }}</Badge>
        <Badge v-if="tx.method" variant="secondary" class="font-mono">{{ tx.method }}</Badge>
        <span class="text-sm text-muted-foreground" :title="exact(tx.timestamp)">
          {{ ago(tx.timestamp) }}
        </span>
        <span v-if="tx.error" class="text-sm text-destructive">{{ tx.error }}</span>
      </div>

      <!-- What it did, in a sentence, before what it consisted of. -->
      <Card v-if="transfers.length" class="mb-6">
        <CardContent class="pt-5">
          <div class="text-ledger mb-2 text-muted-foreground">Transaction action</div>
          <div v-for="(t, i) in transfers" :key="i" class="flex flex-wrap items-center gap-2 py-1 text-sm">
            <span class="text-muted-foreground">Transfer</span>
            <span class="font-mono tabular-nums">{{ units(t.amount) }}</span>
            <span class="text-muted-foreground">of</span>
            <HashLink :value="t.address" :to="`/evm/token/${t.address}`" :start="8" :end="4" :copy="false" />
            <span class="text-muted-foreground">from</span>
            <HashLink :value="t.from!" :zenon="t.fromZenon" :to="`/evm/account/${t.from}`" :start="8" :end="4" :copy="false" />
            <ArrowRightIcon class="size-3.5 text-muted-foreground" />
            <HashLink :value="t.to!" :zenon="t.toZenon" :to="`/evm/account/${t.to}`" :start="8" :end="4" :copy="false" />
          </div>
        </CardContent>
      </Card>

      <section class="mb-10">
        <DataList>
          <DataRow label="inscription">
            <HashLink :value="tx.inscription" full />
            <p class="mt-1 text-xs text-muted-foreground">
              The permanent artifact —
              <RouterLink :to="`/asset/${tx.inscription}`" class="text-primary hover:underline">
                see it as an inscription
              </RouterLink>. This receipt is derived from it and can be
              recomputed by anyone; the inscription is what is on chain.
            </p>
          </DataRow>
          <DataRow label="block">
            <RouterLink
              :to="`/evm/block/${tx.momentum}`"
              class="font-mono text-primary tabular-nums hover:underline"
            >
              {{ count(tx.momentum) }}
            </RouterLink>
            <span class="ml-2 text-muted-foreground" :title="exact(tx.timestamp)">
              {{ exact(tx.timestamp) }}
            </span>
          </DataRow>
          <DataRow label="from">
            <HashLink :value="tx.from" :zenon="tx.fromZenon" :to="`/evm/account/${tx.from}`" full />
          </DataRow>
          <DataRow :label="tx.op === 'deploy' ? 'contract created' : 'to'">
            <HashLink :value="tx.to" :zenon="tx.toZenon" :to="`/evm/account/${tx.to}`" full />
          </DataRow>
          <DataRow v-if="tx.method || tx.selector" label="method">
            <code v-if="tx.method" class="text-xs">{{ tx.method }}</code>
            <span class="font-mono text-xs text-muted-foreground" :class="tx.method && 'ml-2'">
              {{ tx.selector }}
            </span>
            <span v-if="!tx.method" class="ml-2 text-xs text-muted-foreground">
              unrecognised signature
            </span>
          </DataRow>
          <DataRow label="gas">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-mono tabular-nums">{{ count(tx.gasUsed) }}</span>
              <span class="text-muted-foreground">
                of {{ count(tx.gasBudget) }} budget
                ({{ gasPct(tx.gasUsed, tx.gasBudget).toFixed(1) }}%)
              </span>
            </div>
            <div class="mt-1.5 h-1.5 w-full max-w-sm overflow-hidden rounded-full bg-border">
              <div
                class="h-full rounded-full bg-primary/60"
                :style="{width: gasPct(tx.gasUsed, tx.gasBudget) + '%'}"
              />
            </div>
            <p class="mt-1 text-xs text-muted-foreground">
              The budget is what this inscription's plasma paid for, not a fee
              the sender chose — see the chain page.
            </p>
          </DataRow>
          <DataRow v-if="tx.resourcePrice" label="resource price">
            <span class="font-mono tabular-nums">{{ (tx.resourcePrice / 1000).toFixed(3) }}&times;</span>
            <p class="mt-1 text-xs text-muted-foreground">
              NoM's dynamic-plasma price when this was published. 1.000&times; is
              an uncongested network; above it, the same burned fee buys
              proportionally less gas.
            </p>
          </DataRow>
          <DataRow v-if="tx.feeBurned" label="fee burned">
            <span class="font-mono tabular-nums">{{ tx.feeBurned }}</span>
            <span class="ml-1 font-mono text-xs text-muted-foreground">{{ tx.feeToken }}</span>
          </DataRow>
          <DataRow label="output">
            <span class="font-mono text-xs break-all">{{ tx.output }}</span>
          </DataRow>
        </DataList>
      </section>

      <!-- What the calldata means. The decode depends on a signature registry
           this explorer supplies rather than on anything the chain asserts, so
           the raw bytes stay on the page beside it: a reader has to be able to
           check one against the other. -->
      <section v-if="tx.decoded" class="mb-10">
        <Heading :level="2" class="mb-3">Input</Heading>
        <div class="rounded-lg border border-border p-4">
          <div class="mb-3 flex flex-wrap items-baseline gap-2">
            <code class="text-sm">{{ tx.decoded.signature || 'unknown function' }}</code>
            <span class="font-mono text-xs text-muted-foreground">{{ tx.decoded.selector }}</span>
          </div>
          <p v-if="tx.decoded.note" class="mb-3 text-xs text-muted-foreground">
            {{ tx.decoded.note }}
          </p>
          <ParamList :params="tx.decoded.params" />
          <p v-if="!tx.decoded.params || !tx.decoded.params.length" class="text-xs text-muted-foreground">
            No arguments.
          </p>

          <div v-if="tx.returns && tx.returns.length" class="mt-4 border-t border-border/60 pt-3">
            <div class="text-ledger mb-2 text-muted-foreground">returns</div>
            <ParamList :params="tx.returns" />
          </div>

          <details class="mt-4 border-t border-border/60 pt-3">
            <summary class="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
              raw calldata ({{ ((tx.input?.length ?? 2) - 2) / 2 }} bytes)
            </summary>
            <div class="mt-2 font-mono text-xs break-all text-muted-foreground">{{ tx.input }}</div>
          </details>
        </div>
      </section>

      <!-- A deployment has no ABI to decode against, so what is readable about
           it is a different set of facts: what the artifact turned into. -->
      <section v-if="tx.deploy" class="mb-10">
        <Heading :level="2" class="mb-3">Deployment</Heading>
        <div class="rounded-lg border border-border p-4">
          <DataList dense>
            <DataRow label="init code">
              <span class="font-mono tabular-nums">{{ count(tx.deploy.initCodeSize) }} B</span>
              <span class="ml-2 text-xs text-muted-foreground">what was inscribed</span>
            </DataRow>
            <DataRow label="runtime code">
              <span class="font-mono tabular-nums">{{ count(tx.deploy.runtimeCodeSize) }} B</span>
              <span class="ml-2 text-xs text-muted-foreground">what the constructor stored</span>
            </DataRow>
          </DataList>

          <div v-if="tx.deploy.interface && tx.deploy.interface.length" class="mt-4">
            <div class="text-ledger mb-2 text-muted-foreground">
              interface
              <span v-if="tx.deploy.unknownSelectors" class="normal-case">
                &mdash; {{ tx.deploy.interface.length }} named,
                {{ tx.deploy.unknownSelectors }} unrecognised
              </span>
            </div>
            <div class="flex flex-wrap gap-1.5">
              <code
                v-for="sig in tx.deploy.interface"
                :key="sig"
                class="rounded bg-accent/60 px-1.5 py-0.5 text-xs"
              >{{ sig }}</code>
            </div>
            <p class="mt-2 text-xs text-muted-foreground">
              Selectors the deployed dispatcher compares against. That a
              function is reachable is not a promise it behaves as its name
              suggests &mdash; the bytecode is the only authority, and it is
              <RouterLink :to="`/evm/account/${tx.to}`" class="text-primary hover:underline">
                on the contract page</RouterLink>.
            </p>
          </div>

          <div
            v-if="tx.deploy.constructorArgs && tx.deploy.constructorArgs.length"
            class="mt-4 border-t border-border/60 pt-3"
          >
            <div class="text-ledger mb-2 text-muted-foreground">constructor arguments</div>
            <ParamList :params="tx.deploy.constructorArgs" />
            <p v-if="tx.deploy.note" class="mt-2 text-xs text-muted-foreground">{{ tx.deploy.note }}</p>
          </div>
          <p v-else-if="tx.deploy.note" class="mt-3 text-xs text-muted-foreground">{{ tx.deploy.note }}</p>
        </div>
      </section>

      <section v-if="tx.logs && tx.logs.length">
        <Heading :level="2" class="mb-3">Logs <span class="text-muted-foreground">({{ tx.logs.length }})</span></Heading>
        <div v-for="(lg, i) in tx.logs" :key="i" class="mb-3 rounded-lg border border-border p-4">
          <div class="mb-3 flex flex-wrap items-center gap-2">
            <span class="font-mono text-xs text-muted-foreground tabular-nums">#{{ i }}</span>
            <Badge :variant="lg.event ? 'secondary' : 'outline'">
              {{ lg.event || 'unknown event' }}
            </Badge>
            <HashLink :value="lg.address" :to="`/evm/account/${lg.address}`" :start="10" :end="6" />
          </div>
          <!-- The decoded parameters, which handle any event shape. The
               amount below them is kept for a Transfer, where the
               decimal-scaled figure is the thing worth reading. -->
          <ParamList :params="lg.decoded?.params" />
          <p v-if="lg.decoded?.note" class="mt-2 text-xs text-muted-foreground">{{ lg.decoded.note }}</p>
          <DataList v-if="lg.amount" dense class="mt-3 border-t border-border/60 pt-3">
            <DataRow label="amount">
              <span class="font-mono tabular-nums">{{ units(lg.amount) }}</span>
              <span class="ml-1 text-xs text-muted-foreground">({{ lg.amount }} raw)</span>
            </DataRow>
          </DataList>
          <details class="mt-2 border-t border-border/60 pt-2">
            <summary class="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
              raw topics and data
            </summary>
            <div class="mt-2 space-y-1">
            <div v-for="(t, j) in lg.topics" :key="j" class="font-mono text-xs break-all text-muted-foreground">
              <span class="mr-1 text-foreground/60">topic{{ j }}</span>{{ t }}
            </div>
              <div class="font-mono text-xs break-all text-muted-foreground">
                <span class="mr-1 text-foreground/60">data</span>{{ lg.data }}
              </div>
            </div>
          </details>
        </div>
      </section>
    </template>
  </div>
</template>
