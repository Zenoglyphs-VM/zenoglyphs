<script setup lang="ts">
import {computed, onMounted} from 'vue'
import {Badge, Button} from 'nom-ui'
import {initWallet, useWallet} from '@/wallet/useWallet'
import {useIndexer} from '@/core/composables/useIndexer'
import {shortHex} from './evm'

// Connect a Syrius wallet.
//
// The account this connects is not a separate identity the explorer issues —
// it *is* the EVM account. A NoM address and an EVM address are both 20 bytes
// and this metaprotocol makes the mapping the identity, so the key already in
// the extension is the key to the matching EVM account, with no registration
// and nothing to derive. That is why there is no "create an EVM account" step
// anywhere in this UI: there is nothing to create.
const wallet = useWallet()

// The chain this indexer is following, from the shared /info store rather
// than a prop: it is the same answer on every page, and threading it through
// each one would be six chances for a page to forget and silently stop
// checking.
//
// A wallet pointed at another network would sign a block this explorer never
// sees, so the mismatch is worth saying before someone spends plasma finding
// out.
const {info} = useIndexer()
const expected = computed(() => info.value?.chainId)

const wrongChain = computed(
  () =>
    wallet.connected.value &&
    expected.value !== undefined &&
    wallet.chainId.value !== null &&
    wallet.chainId.value !== expected.value,
)

onMounted(initWallet)
</script>

<template>
  <div class="flex flex-wrap items-center gap-2">
    <template v-if="wallet.connected.value">
      <Badge variant="secondary" class="font-mono text-xs">
        {{ shortHex(wallet.address.value ?? '', 8, 6) }}
      </Badge>
      <Badge v-if="wrongChain" variant="destructive" class="text-xs">
        wallet is on chain {{ wallet.chainId.value }}, this explorer follows {{ expected }}
      </Badge>
      <Button variant="ghost" size="sm" @click="wallet.disconnect()">Disconnect</Button>
    </template>

    <template v-else>
      <Button size="sm" :disabled="wallet.connecting.value" @click="wallet.connect()">
        {{ wallet.connecting.value ? 'Waiting for Syrius…' : 'Connect wallet' }}
      </Button>
      <!-- Absence of the extension is a fact about the browser, not an error
           the page did something to cause, so it reads as a pointer rather
           than as a failure. -->
      <a
        v-if="!wallet.present.value"
        href="https://chromewebstore.google.com/search/syrius"
        target="_blank"
        rel="noopener noreferrer"
        class="text-xs text-muted-foreground hover:text-foreground"
      >
        no Zenon wallet detected — get the Syrius extension
      </a>
    </template>

    <span v-if="wallet.error.value" class="text-xs text-destructive">
      {{ wallet.error.value }}
    </span>
  </div>
</template>
