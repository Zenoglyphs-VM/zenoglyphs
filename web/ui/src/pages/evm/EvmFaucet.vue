<script setup lang="ts">
import {computed, ref} from 'vue'
import {WalletIcon} from '@lucide/vue'
import {Badge, Button} from 'nom-ui'
import {api, ApiError} from '@/core/api'
import {useWallet} from '@/wallet/useWallet'
import HashLink from './HashLink.vue'
import EvmWallet from './EvmWallet.vue'

// Test tokens, sent to a Syrius account.
//
// This connected an injected EIP-1193 wallet until 2026-09-10 — MetaMask or
// Rabby, via window.ethereum — purely to read a 20-byte address. That was a
// reasonable-looking mistake and worth recording, because the addresses are the
// same width and it therefore *worked*:
//
//   **An Ethereum address is not a NoM account.** A MetaMask key produces an
//   address whose first byte is whatever the hash gave, and only 0x00 is a NoM
//   user account. Nobody can ever sign an inscription from an Ethereum address,
//   so every token this faucet sent to one was stranded on arrival — visible in
//   the explorer, in a balance, and unspendable forever.
//
// Syrius is the right wallet because it holds the key that can actually *use*
// what arrives: a NoM account is the EVM account, so the recipient can turn
// round and call a contract from the same address. The server refuses any
// recipient that is not a user account, so the old failure cannot recur even if
// someone pastes an Ethereum address by hand.
//
// The EIP-1193 path is removed rather than disabled. It can come back if there
// is ever a reason to read an Ethereum address here, and it should come back
// with an explicit warning attached.
const wallet = useWallet()

const manual = ref('')
const sending = ref(false)
const error = ref('')
const sent = ref<{
  inscription: string
  amount: string
  token: string
  tokenZenon?: string
  to?: string
  toZenon?: string
} | null>(null)

// The connected account, or whatever was typed in instead. Connecting is the
// path that works; the field exists so someone sending to a second account of
// their own does not have to switch wallets to do it.
const recipient = computed(() => manual.value.trim() || wallet.address.value || '')

async function receive() {
  error.value = ''
  sent.value = null
  sending.value = true
  try {
    sent.value = await api.evmFaucet(recipient.value)
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e)
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <div class="rounded-lg border border-border p-4">
    <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-2">
        <WalletIcon class="size-4 text-muted-foreground" />
        <h3 class="text-sm font-semibold">Test tokens</h3>
      </div>
      <EvmWallet />
    </div>

    <p class="mb-3 text-sm text-muted-foreground">
      Receive a test ERC-20 balance on this EVM. It is sent to your Zenon
      account, which <em>is</em> your EVM account — so you can call contracts
      with the same address afterwards, from the Write panel on any contract
      page.
    </p>

    <div class="space-y-3">
      <div v-if="wallet.connected.value" class="flex flex-wrap items-center gap-2 text-sm">
        <span class="text-muted-foreground">sending to</span>
        <Badge variant="secondary" class="font-mono text-xs">
          {{ manual.trim() || wallet.address.value }}
        </Badge>
      </div>

      <div>
        <label for="faucet-to" class="text-xs text-muted-foreground">
          or another address
        </label>
        <input
          id="faucet-to"
          v-model="manual"
          class="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 font-mono text-xs"
          placeholder="z1… (a Zenon account — not an Ethereum address)"
        />
      </div>

      <div class="flex flex-wrap items-center gap-3">
        <Button size="sm" :disabled="sending || !recipient" @click="receive">
          {{ sending ? 'sending…' : 'receive test tokens' }}
        </Button>
        <span v-if="!recipient" class="text-xs text-muted-foreground">
          connect a wallet, or type an address
        </span>
      </div>
    </div>

    <p v-if="error" class="mt-3 text-sm text-destructive">{{ error }}</p>
    <div v-if="sent" class="mt-3 flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
      <Badge variant="success">Sent</Badge>
      <span>{{ sent.amount }} raw units of</span>
      <HashLink
        :value="sent.token"
        :zenon="sent.tokenZenon"
        :to="`/evm/token/${sent.token}`"
        :start="8"
        :end="4"
        :copy="false"
      />
      <span>to</span>
      <HashLink
        :value="sent.to ?? recipient"
        :zenon="sent.toZenon"
        :to="`/evm/account/${sent.to ?? recipient}`"
        :start="8"
        :end="4"
        :copy="false"
      />
      <RouterLink :to="`/evm/tx/${sent.inscription}`" class="text-primary hover:underline">
        view inscription
      </RouterLink>
    </div>
  </div>
</template>
