<script setup lang="ts">
import {ref} from 'vue'
import {useRouter} from 'vue-router'
import {SearchIcon} from '@lucide/vue'
import {Button, Input} from 'nom-ui'

// One box for every kind of identifier this chain has.
//
// The resolution is by *shape*, done here rather than at the API, because
// every shape is unambiguous: 40 hex characters is an address, 64 is an
// inscription id, digits are a momentum height. A round trip to be told which
// of three mutually exclusive patterns matched would be a request that
// answers a question the client already knows.
const router = useRouter()
const q = ref('')
const error = ref('')

function go() {
  const raw = q.value.trim()
  error.value = ''
  if (!raw) return

  const hex = raw.toLowerCase().replace(/^0x/, '')
  if (/^[0-9a-f]{40}$/.test(hex)) {
    router.push(`/evm/account/0x${hex}`)
  } else if (/^[0-9a-f]{64}$/.test(hex)) {
    router.push(`/evm/tx/${hex}`)
  } else if (/^\d+$/.test(raw)) {
    router.push(`/evm/block/${raw}`)
  } else {
    error.value = 'Not an address (40 hex), an inscription id (64 hex), or a momentum height.'
    return
  }
  q.value = ''
}
</script>

<template>
  <div>
    <form class="flex gap-2" @submit.prevent="go">
      <Input
        v-model="q"
        class="font-mono"
        placeholder="Search by address / txn hash / block"
        aria-label="Search the EVM explorer"
        spellcheck="false"
      />
      <Button type="submit" aria-label="Search">
        <SearchIcon />
        <span class="hidden sm:inline">Search</span>
      </Button>
    </form>
    <p v-if="error" class="mt-2 text-xs text-destructive">{{ error }}</p>
  </div>
</template>
