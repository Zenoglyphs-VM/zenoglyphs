<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {ArrowLeftIcon, EyeOffIcon, ExternalLinkIcon, FlameIcon} from '@lucide/vue'
import {
  Address,
  Badge,
  Heading,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from 'nom-ui'
import InscriptionStage from '@/components/InscriptionStage.vue'
import ConfirmationTag from '@/components/ConfirmationTag.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import ErrorState from '@/components/ErrorState.vue'
import ExplorerLink from '@/components/ExplorerLink.vue'
import LoadingState from '@/components/LoadingState.vue'
import {api} from '@/core/api'
import {isEscrow} from '@/core/explorer'
import {bytes, count, short, when} from '@/core/format'
import type {Asset, Holder, Ref as AssetRef, Transfer} from '@/types'

const props = defineProps<{id: string}>()

const asset = ref<Asset | null>(null)
const holders = ref<Holder[]>([])
const transfers = ref<Transfer[]>([])
const refs = ref<AssetRef[]>([])
const usedBy = ref<string[]>([])
const loading = ref(true)
const error = ref('')

async function load(id: string) {
  loading.value = true
  error.value = ''
  try {
    // The four calls are independent, and the three side panels are optional:
    // an indexer that cannot answer one of them still has an inscription worth
    // showing, so only the asset itself is allowed to fail the page.
    const [a, h, t, g] = await Promise.all([
      api.asset(id),
      api.holders(id).catch(() => ({holders: []})),
      api.transfers(id).catch(() => ({transfers: []})),
      api.refs(id).catch(() => ({refs: [], referencedBy: []})),
    ])
    asset.value = a
    holders.value = h.holders ?? []
    transfers.value = t.transfers ?? []
    refs.value = g.refs ?? []
    usedBy.value = g.referencedBy ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.id, load, {immediate: true})

// R-2.9 extensions are consensus-inert — carried on chain, committed to by the
// block hash, never read by the state machine. That makes them worth showing,
// and worth showing as data: a value is rendered as JSON rather than
// interpreted, because this viewer does not know what a key from a future
// version means and must not pretend to.
const extensions = computed(() => Object.entries(asset.value?.extensions ?? {}))

function extValue(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

// Three groups, not six colours: issuance is the one row that creates supply,
// a burn is the one that destroys it, and everything in between is a move.
function kindStyle(kind: Transfer['kind']): 'success' | 'warning' | 'outline' {
  if (kind === 'mint') return 'success'
  if (kind === 'burn') return 'warning'
  return 'outline'
}
</script>

<template>
  <ErrorState v-if="error" :message="error" />
  <LoadingState v-else-if="loading || !asset" />

  <div v-else>
    <!-- Two bands, and the split is what the data asks for. Beside the artwork:
         the identity of this inscription, which is a two-column list. Below it,
         full width: its ledger — composite imports, holders, activity — which
         runs to six columns of hashes and addresses and does not fit in a 26rem
         column no matter how hard it is truncated. -->
    <div class="grid gap-8 lg:grid-cols-[minmax(0,1fr)_26rem]">
      <div class="lg:sticky lg:top-20 lg:self-start">
        <InscriptionStage :asset="asset" />
      </div>

      <div class="min-w-0">
        <RouterLink
          to="/"
          class="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeftIcon class="size-4" />
          All inscriptions
        </RouterLink>

        <!-- The author's description is metadata, so it goes in the metadata
             table with everything else rather than sitting here as a subtitle.
             It is the one field on this page whose length the viewer does not
             control, and a paragraph of it above the fold pushed the facts
             people came for off the screen. -->
        <Heading :level="1" class="text-2xl break-words">
          {{ asset.name || 'Inscription #' + asset.number }}
        </Heading>

        <DataList class="mt-5">
          <DataRow label="number">
            <span class="font-mono tabular-nums">#{{ asset.number }}</span>
          </DataRow>
          <DataRow label="id">
            <span class="inline-flex items-center gap-1.5">
              <Address :address="asset.id" :start="10" :end="8" />
              <ExplorerLink
                target="transaction"
                :value="asset.id"
                title="The mint block — an inscription id is the hash of the block that created it"
              />
            </span>
          </DataRow>
          <DataRow label="type">
            <span class="font-mono text-xs">{{ asset.mime }}</span>
          </DataRow>
          <DataRow label="size">
            <span class="font-mono tabular-nums">{{ bytes(asset.size) }}</span>
            <span v-if="asset.parts > 1" class="text-muted-foreground">
              in {{ asset.parts }} blocks
            </span>
          </DataRow>
          <DataRow label="supply">
            <span class="font-mono tabular-nums">{{ asset.supply }}</span>
            <span v-if="asset.decimals" class="text-muted-foreground">
              ({{ asset.decimals }} dp)
            </span>
          </DataRow>
          <DataRow label="issuer">
            <span class="inline-flex items-center gap-1.5">
              <RouterLink :to="`/address/${asset.issuer}`" class="hover:text-info">
                <Address :address="asset.issuer" :copy="false" />
              </RouterLink>
              <ExplorerLink target="account" :value="asset.issuer" />
            </span>
          </DataRow>
          <!-- "issued to", not "owner": assets.owner is written once at mint and
               no transfer updates it — nothing in internal/store issues an
               UPDATE against it. The live answer is the Holders table below, so
               this row is labelled for what it actually holds rather than left
               to contradict it. The Telegram front end says the same thing. -->
          <DataRow label="issued to">
            <span class="inline-flex items-center gap-1.5">
              <RouterLink :to="`/address/${asset.owner}`" class="hover:text-info">
                <Address :address="asset.owner" :copy="false" />
              </RouterLink>
              <ExplorerLink target="account" :value="asset.owner" />
            </span>
          </DataRow>
          <DataRow v-if="asset.collection" label="collection">
            <RouterLink
              :to="'/collection/' + asset.collection"
              class="font-mono text-sm hover:text-info"
            >
              {{ short(asset.collection, 10) }}
            </RouterLink>
          </DataRow>
          <DataRow label="momentum">
            <span class="inline-flex items-center gap-1.5">
              <span class="font-mono tabular-nums">{{ count(asset.momentum) }}</span>
              <span class="text-muted-foreground">· idx {{ asset.momentumIndex }}</span>
              <ExplorerLink target="momentum" :value="asset.momentum" />
            </span>
          </DataRow>
          <DataRow label="inscribed">{{ when(asset.timestamp) }}</DataRow>
          <DataRow label="status"><ConfirmationTag :asset="asset" /></DataRow>
          <!-- A withheld inscription keeps its metadata and loses its bytes:
               that is what "redacted" means here, and it is the whole
               difference from "hidden", which this indexer does not serve at
               all. So the row says so instead of offering a link that would
               answer 410 — the viewer must not invite a request the operator
               has already refused. -->
          <DataRow label="content">
            <span
              v-if="asset.dataWithheld"
              class="inline-flex items-center gap-1.5 text-sm text-muted-foreground"
            >
              <EyeOffIcon class="size-3.5" />
              redacted by this indexer
            </span>
            <a
              v-else
              :href="asset.contentUrl"
              target="_blank"
              rel="noopener"
              class="inline-flex items-center gap-1 text-sm hover:text-info"
            >
              raw bytes
              <ExternalLinkIcon class="size-3.5" />
            </a>
          </DataRow>
          <DataRow v-if="asset.description" label="description">
            {{ asset.description }}
          </DataRow>
          <!-- Extension keys keep the author's own names rather than being
               folded into one "ext" row: an old indexer showing a key it does
               not understand, verbatim, is the whole point of R-2.9. -->
          <DataRow v-for="[k, v] in extensions" :key="k" :label="String(k)" raw>
            <span class="font-mono text-xs break-all">{{ extValue(v) }}</span>
          </DataRow>
        </DataList>
      </div>
    </div>

    <div class="mt-10 space-y-10">
      <!-- Composite: what this inscription imports, and who imports it. How a
           slot resolves is the same for every composite, so it is documented
           once on the docs page instead of above each one. -->
      <section v-if="refs.length || usedBy.length">
        <Heading :level="2" class="mb-3 text-lg">Composite</Heading>

        <template v-if="refs.length">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-16">slot</TableHead>
                <TableHead>imports</TableHead>
                <TableHead>type</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="r in refs" :key="r.slot">
                <TableCell class="font-mono tabular-nums">{{ r.slot }}</TableCell>
                <TableCell>
                  <span class="inline-flex items-center gap-1.5">
                    <RouterLink
                      v-if="r.resolved"
                      :to="'/asset/' + r.id"
                      class="text-sm hover:text-info"
                    >
                      {{ r.name || short(r.id, 8) }}
                    </RouterLink>
                    <Badge v-else variant="warning">
                      {{ r.withheld ? 'withheld here' : 'not indexed' }}
                    </Badge>
                    <ExplorerLink target="transaction" :value="r.id" />
                  </span>
                </TableCell>
                <TableCell class="font-mono text-xs text-muted-foreground">
                  {{ r.mime || '—' }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </template>

        <template v-if="usedBy.length">
          <Heading :level="3" class="mt-6 mb-3 text-base">Imported by</Heading>
          <div class="flex flex-wrap gap-1.5">
            <RouterLink v-for="u in usedBy" :key="u" :to="'/asset/' + u">
              <Badge variant="outline" class="font-mono">{{ short(u, 6) }}</Badge>
            </RouterLink>
          </div>
        </template>
      </section>

      <section v-if="asset.attributes && Object.keys(asset.attributes).length">
        <Heading :level="2" class="mb-3 text-lg">Attributes</Heading>
        <DataList dense>
          <DataRow v-for="(v, k) in asset.attributes" :key="k" :label="String(k)" raw>
            {{ String(v) }}
          </DataRow>
        </DataList>
      </section>

      <section v-if="holders.length">
        <Heading :level="2" class="mb-3 text-lg">Holders</Heading>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>address</TableHead>
              <TableHead class="text-right">units</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="h in holders" :key="h.address">
              <TableCell>
                <!-- A dispenser's stock sits in this table under an address of
                     the protocol's own (z1zen0glyphxescr0w…), not a wallet. It is
                     rendered as the address it is, badged for what it is, and
                     titled with the listing it belongs to. -->
                <span class="inline-flex items-center gap-1.5">
                  <RouterLink :to="`/address/${h.address}`" class="hover:text-info">
                    <Address :address="h.address" :copy="false" />
                  </RouterLink>
                  <Badge
                    v-if="isEscrow(h.address)"
                    variant="outline"
                    :title="h.dispenser ? `dispenser ${h.dispenser}` : undefined"
                  >
                    escrow
                  </Badge>
                  <ExplorerLink v-else target="account" :value="h.address" />
                </span>
              </TableCell>
              <TableCell class="text-right font-mono tabular-nums">{{ h.amount }}</TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </section>

      <section v-if="transfers.length">
        <Heading :level="2" class="mb-3 text-lg">Activity</Heading>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead class="w-24">what</TableHead>
              <TableHead>momentum</TableHead>
              <TableHead>tx</TableHead>
              <TableHead>when</TableHead>
              <TableHead>from</TableHead>
              <TableHead>to</TableHead>
              <TableHead class="text-right">units</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="(t, i) in transfers" :key="t.momentum + '-' + i">
              <!-- What the movement was. "From and to" cannot say it: a
                   dispenser fill and an ordinary send differ only in who the
                   sender is. Rows the indexer reconstructed rather than
                   recorded say so on hover — see internal/api/provenance.go. -->
              <TableCell>
                <Badge
                  :variant="kindStyle(t.kind)"
                  :title="t.synthetic ? 'reconstructed from the event log' : undefined"
                >
                  {{ t.kind ?? 'transfer' }}
                </Badge>
              </TableCell>
              <TableCell class="font-mono tabular-nums">
                <span class="inline-flex items-center gap-1.5">
                  {{ count(t.momentum) }}
                  <ExplorerLink target="momentum" :value="t.momentum" />
                </span>
              </TableCell>
              <!-- The block that actually moved the units. This is the row a
                   holder checks against the chain, so it links out. -->
              <TableCell>
                <span class="inline-flex items-center gap-1.5">
                  <span class="font-mono text-xs" :title="t.block">{{ short(t.block, 6) }}</span>
                  <ExplorerLink target="transaction" :value="t.block" />
                </span>
              </TableCell>
              <TableCell class="text-xs whitespace-nowrap text-muted-foreground">
                {{ when(t.timestamp) }}
              </TableCell>
              <TableCell>
                <Badge v-if="isEscrow(t.from)" variant="outline" :title="t.fromDispenser">
                  escrow
                </Badge>
                <span v-else class="font-mono text-xs" :title="t.from">{{ short(t.from, 6) }}</span>
              </TableCell>
              <TableCell>
                <Badge v-if="t.burned" variant="warning">
                  <FlameIcon />
                  burned
                </Badge>
                <Badge v-else-if="isEscrow(t.to)" variant="outline" :title="t.toDispenser">
                  escrow
                </Badge>
                <span v-else class="font-mono text-xs" :title="t.to">{{ short(t.to, 6) }}</span>
              </TableCell>
              <TableCell class="text-right font-mono tabular-nums">{{ t.amount }}</TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </section>
    </div>
  </div>
</template>
