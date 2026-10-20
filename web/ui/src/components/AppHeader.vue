<script setup lang="ts">
import {computed} from 'vue'
import {useRoute} from 'vue-router'
import {MoonIcon, SunIcon} from '@lucide/vue'
import {Button, useTheme} from 'nom-ui'
import {useIndexer} from '@/core/composables/useIndexer'
import ZcribeMark from './ZcribeMark.vue'
import SyncStatus from './SyncStatus.vue'

const route = useRoute()
const {theme, toggleTheme} = useTheme()
const {info} = useIndexer()

const tabs = [
  {to: '/', label: 'Inscriptions', match: ['gallery', 'asset']},
  {to: '/collections', label: 'Collections', match: ['collections', 'collection']},
  {to: '/dispensers', label: 'Market', match: ['market']},
  {to: '/chain', label: 'Chain', match: ['chain']},
  {
    to: '/evm',
    label: 'EVM',
    match: [
      'evm',
      'evm-blocks',
      'evm-block',
      'evm-txs',
      'evm-contracts',
      'evm-tokens',
      'evm-addresses',
      'evm-account',
      'evm-token',
      'evm-tx',
    ],
  },
  {to: '/docs', label: 'Docs', match: ['docs']},
]

const active = computed(() => String(route.name ?? 'gallery'))
</script>

<template>
  <header
    class="sticky top-0 z-40 border-b border-border bg-background/85 backdrop-blur supports-[backdrop-filter]:bg-background/70"
  >
    <div class="mx-auto flex h-14 max-w-7xl items-center gap-4 px-4 sm:px-6">
      <RouterLink to="/" class="flex shrink-0 items-center gap-2">
        <ZcribeMark class="size-6" />
        <span class="text-base font-semibold tracking-tight">
          <span class="text-primary">z</span>cribe
        </span>
      </RouterLink>

      <nav class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
        <RouterLink
          v-for="tab in tabs"
          :key="tab.to"
          :to="tab.to"
          class="rounded-md px-2.5 py-1.5 text-sm font-medium whitespace-nowrap transition-colors"
          :class="
            tab.match.includes(active)
              ? 'bg-accent text-accent-foreground'
              : 'text-muted-foreground hover:text-foreground'
          "
        >
          {{ tab.label }}
        </RouterLink>
      </nav>

      <SyncStatus class="hidden sm:flex" />

      <Button
        variant="ghost"
        size="icon-sm"
        :aria-label="theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'"
        @click="toggleTheme"
      >
        <SunIcon v-if="theme === 'dark'" />
        <MoonIcon v-else />
      </Button>
    </div>

    <!-- Below sm the status line gets its own row rather than being dropped:
         whether the indexer is caught up changes what the page below means. -->
    <div v-if="info" class="border-t border-border px-4 py-1.5 sm:hidden">
      <SyncStatus />
    </div>
  </header>
</template>
