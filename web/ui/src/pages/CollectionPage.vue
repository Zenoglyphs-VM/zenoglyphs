<script setup lang="ts">
import {ref, watch} from 'vue'
import {ArrowLeftIcon} from '@lucide/vue'
import {Address} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import InscriptionGrid from '@/components/InscriptionGrid.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import ExplorerLink from '@/components/ExplorerLink.vue'
import LoadingState from '@/components/LoadingState.vue'
import {api} from '@/core/api'
import {count} from '@/core/format'
import type {Asset, Collection} from '@/types'

const props = defineProps<{id: string}>()

const collection = ref<Collection | null>(null)
const assets = ref<Asset[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')

async function load(id: string) {
  loading.value = true
  error.value = ''
  try {
    const data = await api.collection(id)
    collection.value = data.collection
    assets.value = data.assets ?? []
    total.value = data.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.id, load, {immediate: true})
</script>

<template>
  <div>
    <RouterLink
      to="/collections"
      class="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeftIcon class="size-4" />
      Collections
    </RouterLink>

    <ErrorState v-if="error" :message="error" />
    <LoadingState v-else-if="loading || !collection" />

    <template v-else>
      <PageHeading
        :title="collection.Name"
        :count="`${count(total)} inscriptions`"
        :description="collection.Description"
      />
      <!-- Who issued a collection is the whole membership rule — a mint joins
           only when the same account issued both — so the page that names the
           collection has to name its issuer too, and let you go check. -->
      <DataList dense class="mb-8">
        <DataRow label="id">
          <span class="inline-flex items-center gap-1.5">
            <Address :address="collection.ID" :start="10" :end="8" />
            <ExplorerLink target="transaction" :value="collection.ID" />
          </span>
        </DataRow>
        <DataRow label="issuer">
          <span class="inline-flex items-center gap-1.5">
            <RouterLink
              :to="{name: 'gallery', query: {issuer: collection.Issuer}}"
              class="hover:text-info"
            >
              <Address :address="collection.Issuer" :copy="false" />
            </RouterLink>
            <ExplorerLink target="account" :value="collection.Issuer" />
          </span>
        </DataRow>
        <DataRow label="created">
          <span class="inline-flex items-center gap-1.5">
            <span class="font-mono tabular-nums">{{ count(collection.Height) }}</span>
            <ExplorerLink target="momentum" :value="collection.Height" />
          </span>
        </DataRow>
      </DataList>

      <EmptyState v-if="!assets.length" message="Nothing inscribed into this collection yet." />
      <InscriptionGrid v-else :assets="assets" />
    </template>
  </div>
</template>
