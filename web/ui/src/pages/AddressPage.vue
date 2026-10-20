<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {ImagesIcon, WalletIcon} from '@lucide/vue'
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
import PageHeading from '@/components/PageHeading.vue'
import InscriptionGrid from '@/components/InscriptionGrid.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import ExplorerLink from '@/components/ExplorerLink.vue'
import LoadingState from '@/components/LoadingState.vue'
import {api} from '@/core/api'
import {isEscrow, isProtocolAddress} from '@/core/explorer'
import {baseMime, count} from '@/core/format'
import type {Asset, Holding} from '@/types'

// One address, two different questions, and keeping them apart is the point
// of this page.
//
//   holds     the live balance table: what this address can transfer right
//             now, whoever minted it.
//   issued    what it put on the chain. An issuer never stops being the
//             issuer, and every unit may have moved on years ago.
//
// The gallery's "filter by owner" answered neither of them well: it lists
// inscriptions whose `owner` column names the address, and that column is
// written once at mint and never updated by a transfer. This page reads the
// balances instead, which is the half that is actually live.
const props = defineProps<{addr: string}>()

const holdings = ref<Holding[]>([])
const dispenser = ref('')
const issued = ref<Asset[]>([])
const issuedTotal = ref(0)
const loading = ref(true)
const error = ref('')

const escrow = computed(() => isEscrow(props.addr))
const protocolOwned = computed(() => isProtocolAddress(props.addr))

async function load(addr: string) {
  loading.value = true
  error.value = ''
  try {
    // Holdings are the page; issuance is a second panel that must not be
    // able to fail it.
    const [held, mints] = await Promise.all([
      api.address(addr),
      api.assets({issuer: addr, limit: 12}).catch(() => ({assets: [], total: 0})),
    ])
    holdings.value = held.holdings ?? []
    dispenser.value = held.dispenser ?? ''
    issued.value = mints.assets ?? []
    issuedTotal.value = mints.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.addr, load, {immediate: true})
</script>

<template>
  <div>
    <PageHeading title="Address" :count="holdings.length ? `${count(holdings.length)} held` : ''" />

    <div class="mb-8 flex flex-wrap items-center gap-2">
      <Address :address="addr" :start="14" :end="10" />
      <ExplorerLink target="account" :value="addr" />
      <!-- A protocol-owned address is not somebody's wallet, and an explorer
           has nothing to show for one. Saying so beats a link to an empty
           account page. -->
      <Badge v-if="escrow" variant="outline">dispenser escrow</Badge>
      <Badge v-else-if="protocolOwned" variant="outline">protocol address</Badge>
    </div>

    <!-- The escrow address is a display form and cannot be turned back into an
         id by anyone holding it; the indexer matched it for us, so this is the
         one place the listing behind it can be named. -->
    <p v-if="dispenser" class="mb-8 -mt-4 text-sm text-muted-foreground">
      Stock held for dispenser
      <span class="font-mono text-xs break-all">{{ dispenser }}</span>
    </p>

    <ErrorState v-if="error" :message="error" />
    <LoadingState v-else-if="loading" />

    <template v-else>
      <section>
        <Heading :level="2" class="mb-3 flex items-center gap-2 text-lg">
          <WalletIcon class="size-4 text-muted-foreground" />
          Holdings
        </Heading>

        <EmptyState
          v-if="!holdings.length"
          message="This address holds no inscriptions."
          hint="balances move on transfer; issuance does not"
        />
        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead class="w-20">#</TableHead>
              <TableHead>inscription</TableHead>
              <TableHead>type</TableHead>
              <TableHead class="text-right">units</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="h in holdings" :key="h.asset.id">
              <TableCell class="font-mono tabular-nums text-muted-foreground">
                {{ h.asset.number }}
              </TableCell>
              <TableCell>
                <span class="inline-flex items-center gap-1.5">
                  <RouterLink :to="`/asset/${h.asset.id}`" class="text-sm hover:text-info">
                    {{ h.asset.name || h.asset.id.slice(0, 10) + '…' }}
                  </RouterLink>
                  <Badge v-if="h.asset.dataWithheld" variant="warning">redacted</Badge>
                </span>
              </TableCell>
              <TableCell class="font-mono text-xs text-muted-foreground">
                {{ baseMime(h.asset.mime) }}
              </TableCell>
              <TableCell class="text-right font-mono tabular-nums">{{ h.amount }}</TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </section>

      <section v-if="issued.length" class="mt-10">
        <Heading :level="2" class="mb-3 flex items-center gap-2 text-lg">
          <ImagesIcon class="size-4 text-muted-foreground" />
          Issued
          <span class="text-ledger font-normal text-muted-foreground">
            {{ count(issuedTotal) }} inscribed by this address
          </span>
        </Heading>
        <InscriptionGrid :assets="issued" />
        <RouterLink
          v-if="issuedTotal > issued.length"
          :to="{name: 'gallery', query: {issuer: addr}}"
          class="mt-4 inline-block text-sm text-muted-foreground hover:text-foreground"
        >
          See all {{ count(issuedTotal) }} →
        </RouterLink>
      </section>
    </template>
  </div>
</template>
