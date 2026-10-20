<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {useRoute, useRouter} from 'vue-router'
import {SearchIcon, WalletIcon} from '@lucide/vue'
import {Button, Input, Select, SelectContent, SelectItem, SelectTrigger, SelectValue} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import InscriptionGrid from '@/components/InscriptionGrid.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingState from '@/components/LoadingState.vue'
import {api, GALLERY_PAGE} from '@/core/api'
import {baseMime, count} from '@/core/format'
import {useIndexer} from '@/core/composables/useIndexer'
import type {Asset} from '@/types'

const route = useRoute()
const router = useRouter()
const {info} = useIndexer()

const assets = ref<Asset[]>([])
const total = ref(0)
const loading = ref(true)
const loadingMore = ref(false)
const error = ref('')

// Filters live in the URL, not in component state: a filtered gallery is a
// thing you send someone, and reloading the page has to land you back on the
// same set.
const owner = computed(() => String(route.query.owner ?? ''))
const issuer = computed(() => String(route.query.issuer ?? ''))
const mime = computed(() => String(route.query.mime ?? ''))
const search = ref(owner.value || issuer.value)

// Select has no "no value" option, so "all types" is a sentinel rather than an
// empty string — an empty SelectItem value is not selectable.
const ALL = '__all__'
const mimeValue = computed(() => mime.value || ALL)

const filters = computed(() => ({
  owner: owner.value,
  issuer: issuer.value,
  mime: mime.value,
  collection: String(route.query.collection ?? ''),
}))

// The newest page only. The gallery used to ask for everything and receive
// the backend's default 100 without saying so, which meant an index larger
// than that simply hid its older half — the silent kind of wrong. Now the page
// size is the UI's own, the count says what is being shown out of what exists,
// and "load more" is how you get the rest.
async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await api.assets({...filters.value, limit: GALLERY_PAGE})
    assets.value = page.assets ?? []
    total.value = page.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function loadMore() {
  loadingMore.value = true
  try {
    const page = await api.assets({
      ...filters.value,
      limit: GALLERY_PAGE,
      offset: assets.value.length,
    })
    assets.value = assets.value.concat(page.assets ?? [])
    total.value = page.total
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingMore.value = false
  }
}

watch(() => route.query, load, {immediate: true, deep: true})
watch([owner, issuer], () => {
  search.value = owner.value || issuer.value
})

const hasMore = computed(() => assets.value.length < total.value)

/**
 * One box, three kinds of thing, because that is what people paste into it.
 *
 * An inscription id is a 64-character block hash and an inscription number is
 * an integer; GET /asset/{id|number} already accepts either, and the admin
 * panel's moderation lookup has worked this way from the start. An address
 * filters the gallery, which is what the box did before and still does.
 */
function submitSearch() {
  const v = search.value.trim()
  if (!v) {
    void router.push({name: 'gallery', query: {}})
    return
  }
  if (/^[0-9a-f]{64}$/i.test(v)) {
    void router.push(`/asset/${v.toLowerCase()}`)
    return
  }
  if (/^\d+$/.test(v)) {
    void router.push(`/asset/${v}`)
    return
  }
  void router.push({name: 'gallery', query: {owner: v}})
}

function applyMime(value: string) {
  const query = {...route.query, mime: value === ALL ? undefined : value}
  void router.push({name: 'gallery', query})
}
</script>

<template>
  <div>
    <PageHeading
      title="Inscriptions"
      :count="
        assets.length < total
          ? `${count(assets.length)} of ${count(total)}`
          : `${count(total)} total`
      "
    />

    <div class="mb-6 flex flex-col gap-2 sm:flex-row">
      <div class="relative flex-1">
        <SearchIcon
          class="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
        />
        <Input
          v-model="search"
          type="search"
          placeholder="Inscription id, number, or owner address"
          class="pl-9 font-mono"
          spellcheck="false"
          autocapitalize="none"
          autocomplete="off"
          @change="submitSearch"
          @keyup.enter="submitSearch"
        />
      </div>

      <Select :model-value="mimeValue" @update:model-value="(v) => applyMime(String(v))">
        <SelectTrigger class="sm:w-64">
          <SelectValue placeholder="All types" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem :value="ALL">All types</SelectItem>
          <SelectItem v-for="m in info?.allowedMimes ?? []" :key="m" :value="m">
            {{ baseMime(m) }}
          </SelectItem>
        </SelectContent>
      </Select>
    </div>

    <!-- Filtering by an address answers "what was issued to it", which is a
         mint-time fact. What it holds now is a different question and a
         different page, so the filter offers the way across. -->
    <RouterLink
      v-if="owner || issuer"
      :to="`/address/${owner || issuer}`"
      class="mb-6 -mt-2 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
    >
      <WalletIcon class="size-4" />
      What this address holds now
    </RouterLink>

    <ErrorState v-if="error" :message="error" />
    <LoadingState v-else-if="loading" />
    <EmptyState
      v-else-if="!assets.length"
      message="Nothing inscribed yet."
      hint="zcribe demo -devnet"
    />
    <template v-else>
      <InscriptionGrid :assets="assets" />
      <div v-if="hasMore" class="mt-8 flex justify-center">
        <Button variant="outline" :disabled="loadingMore" @click="loadMore">
          {{
            loadingMore ? 'Loading…' : `Load ${Math.min(GALLERY_PAGE, total - assets.length)} more`
          }}
        </Button>
      </div>
    </template>
  </div>
</template>
