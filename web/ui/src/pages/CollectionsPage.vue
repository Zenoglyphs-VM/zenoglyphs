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
import type {Collection} from '@/types'

const collections = ref<Collection[]>([])
const loading = ref(true)
const error = ref('')

onMounted(async () => {
  try {
    const data = await api.collections()
    collections.value = data.collections ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div>
    <PageHeading title="Collections" :count="`${count(collections.length)} total`" />

    <ErrorState v-if="error" :message="error" />
    <LoadingState v-else-if="loading" />
    <EmptyState v-else-if="!collections.length" message="No collections yet." />

    <Table v-else>
      <TableHeader>
        <TableRow>
          <TableHead>name</TableHead>
          <TableHead>id</TableHead>
          <TableHead>issuer</TableHead>
          <TableHead class="text-right">momentum</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow v-for="c in collections" :key="c.ID">
          <TableCell>
            <RouterLink :to="`/collection/${c.ID}`" class="font-medium hover:text-info">
              {{ c.Name }}
            </RouterLink>
          </TableCell>
          <TableCell>
            <span class="inline-flex items-center gap-1.5">
              <Address :address="c.ID" :start="10" :end="6" :copy="false" />
              <ExplorerLink target="transaction" :value="c.ID" />
            </span>
          </TableCell>
          <TableCell>
            <span class="inline-flex items-center gap-1.5">
              <Address :address="c.Issuer" :copy="false" />
              <ExplorerLink target="account" :value="c.Issuer" />
            </span>
          </TableCell>
          <TableCell class="text-right font-mono tabular-nums">
            <span class="inline-flex items-center gap-1.5">
              {{ count(c.Height) }}
              <ExplorerLink target="momentum" :value="c.Height" />
            </span>
          </TableCell>
        </TableRow>
      </TableBody>
    </Table>
  </div>
</template>
