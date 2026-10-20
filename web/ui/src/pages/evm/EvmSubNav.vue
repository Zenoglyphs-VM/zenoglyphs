<script setup lang="ts">
import {computed} from 'vue'
import {useRoute} from 'vue-router'
import EvmWallet from './EvmWallet.vue'

// The EVM section's own row, one level below the site-wide "EVM" tab in
// AppHeader. It exists because the EVM explorer grew four things worth a
// page of their own — chain state, blocks, contracts, addresses — and the
// site-wide nav only has room to point at one of them.
const tabs = [
  {to: '/evm', label: 'Chain', match: ['evm']},
  {to: '/evm/blocks', label: 'Blocks', match: ['evm-blocks', 'evm-block']},
  {to: '/evm/txs', label: 'Transactions', match: ['evm-txs', 'evm-tx']},
  {to: '/evm/contracts', label: 'Contracts', match: ['evm-contracts']},
  {to: '/evm/tokens', label: 'Tokens', match: ['evm-tokens', 'evm-token']},
  {to: '/evm/addresses', label: 'Addresses', match: ['evm-addresses', 'evm-account']},
  {to: '/evm/faucet', label: 'Faucet', match: ['evm-faucet']},
]

const route = useRoute()
const active = computed(() => String(route.name ?? ''))
</script>

<template>
  <nav class="mb-6 flex flex-wrap items-center gap-1 border-b border-border pb-2">
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

    <!-- The wallet sits in the section nav rather than the site header: it is
         only meaningful inside the EVM, where an account is a thing you can
         publish from. Everywhere else on this site is read-only. -->
    <div class="ml-auto">
      <EvmWallet />
    </div>
  </nav>
</template>
