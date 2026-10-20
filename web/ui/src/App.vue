<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {Toaster} from 'nom-ui'
import AppHeader from '@/components/AppHeader.vue'
import AppFooter from '@/components/AppFooter.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingState from '@/components/LoadingState.vue'
import {useIndexer} from '@/core/composables/useIndexer'

const {refresh, startPolling} = useIndexer()

// Nothing renders until /info has answered once: the sandbox policy, the mime
// allowlist and the confirmation depth all come from it, and a view that
// guesses at those renders something subtly wrong rather than nothing.
const ready = ref(false)
const fatal = ref('')

onMounted(async () => {
  try {
    await refresh()
    ready.value = true
  } catch (e) {
    fatal.value = `${e instanceof Error ? e.message : String(e)}\n\nis \`zcribe serve\` running?`
  }
  startPolling()
})
</script>

<template>
  <div class="flex min-h-screen flex-col">
    <AppHeader />

    <main class="mx-auto w-full max-w-7xl flex-1 px-4 py-8 sm:px-6" aria-live="polite">
      <ErrorState v-if="fatal" title="Cannot reach the indexer API" :message="fatal" />
      <LoadingState v-else-if="!ready" />
      <RouterView v-else v-slot="{Component}">
        <component :is="Component" />
      </RouterView>
    </main>

    <AppFooter />
    <Toaster />
  </div>
</template>
