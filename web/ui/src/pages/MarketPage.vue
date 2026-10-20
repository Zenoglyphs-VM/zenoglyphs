<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {Address, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import EmptyState from '@/components/EmptyState.vue'
import ExplorerLink from '@/components/ExplorerLink.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingState from '@/components/LoadingState.vue'
import {api} from '@/core/api'
import {count} from '@/core/format'
import type {Dispenser} from '@/types'

const dispensers = ref<Dispenser[]>([])
const loading = ref(true)
const error = ref('')

onMounted(async () => {
  try {
    const data = await api.dispensers()
    dispensers.value = data.dispensers ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div>
    <PageHeading
      title="Market"
      :count="`${count(dispensers.length)} open dispensers`"
      description="Standing offers: units of an inscription, at a fixed price, until they run out."
    />

    <ErrorState v-if="error" :message="error" />
    <LoadingState v-else-if="loading" />
    <EmptyState v-else-if="!dispensers.length" message="No open dispensers." />

    <Table v-else>
      <TableHeader>
        <TableRow>
          <TableHead>dispenser</TableHead>
          <TableHead>asset</TableHead>
          <TableHead class="text-right">remaining</TableHead>
          <TableHead class="text-right">price</TableHead>
          <TableHead>seller</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow v-for="d in dispensers" :key="d.id">
          <TableCell>
            <span class="inline-flex items-center gap-1.5">
              <Address :address="d.id" :start="10" :end="6" :copy="false" />
              <ExplorerLink target="transaction" :value="d.id" />
            </span>
          </TableCell>
          <TableCell>
            <RouterLink :to="`/asset/${d.asset}`" class="font-mono text-sm hover:text-info">
              {{ d.asset.slice(0, 8) }}…{{ d.asset.slice(-4) }}
            </RouterLink>
          </TableCell>
          <TableCell class="text-right font-mono tabular-nums">{{ d.remaining }}</TableCell>
          <TableCell class="text-right font-mono tabular-nums">{{ d.price }}</TableCell>
          <TableCell>
            <span class="inline-flex items-center gap-1.5">
              <Address :address="d.issuer" :copy="false" />
              <ExplorerLink target="account" :value="d.issuer" />
            </span>
          </TableCell>
        </TableRow>
      </TableBody>
    </Table>
  </div>
</template>
