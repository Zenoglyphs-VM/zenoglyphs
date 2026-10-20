<script setup lang="ts">
import {Card, CardContent, Heading} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import EvmSubNav from './EvmSubNav.vue'
import EvmFaucet from './EvmFaucet.vue'

// The faucet used to be a card on the chain overview, which put a thing you
// *do* in the middle of a page of things you *read*, and buried it under the
// fold on the one page nobody scrolls. Its own tab is also where a newcomer
// looks for it.
</script>

<template>
  <div class="mx-auto max-w-7xl px-4 py-8 sm:px-6">
    <PageHeading
      title="Faucet"
      description="Test tokens, sent to the Zenon account you already have"
    />
    <EvmSubNav />

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <EvmFaucet />

      <Card>
        <CardContent class="pt-5">
          <Heading :level="2" class="mb-2 text-base">Why your own address</Heading>
          <p class="text-sm text-muted-foreground">
            A Zenon address and an EVM address are both twenty bytes, and this
            metaprotocol maps them unchanged — so the account in your Syrius
            extension already <em>is</em> an EVM account. Nothing is derived,
            nothing is registered, and there is no separate key to look after.
          </p>
          <p class="mt-3 text-sm text-muted-foreground">
            That is why the faucet asks for a <code class="text-xs">z1…</code>
            address and refuses anything else. An Ethereum address is the same
            width and would be accepted by the arithmetic, but no Zenon key can
            sign an inscription from one, so tokens sent there would arrive and
            never move again.
          </p>
          <p class="mt-3 text-sm text-muted-foreground">
            Once you hold a balance, the Write panel on any contract page
            publishes calls from the same account.
          </p>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
