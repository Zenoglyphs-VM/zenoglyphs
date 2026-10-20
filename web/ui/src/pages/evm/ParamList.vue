<script setup lang="ts">
import {Badge} from 'nom-ui'
import type {EvmParam} from '@/types'
import HashLink from './HashLink.vue'
import {isZeroAddress} from './evm'

// A decoded ABI argument list.
//
// Three rules the layout follows, each of which is a judgement about what a
// reader is actually trying to do:
//
//   - **The name goes first and the type second.** `to` is what someone reads;
//     `address` is what they check afterwards.
//   - **An address is a link, never a string.** Every decoded address here is
//     an account or a contract somewhere else in this explorer, and the whole
//     point of decoding was to make it reachable.
//   - **A word the decoder could not type shows every reading at once.** For
//     an unregistered selector there is no signature to say whether a word is
//     a number or an address, so both are shown and neither is asserted —
//     picking one would be a guess wearing the costume of a decode.
defineProps<{params?: EvmParam[]}>()

/** A word with no type is one the decoder is offering readings of, not one it decoded. */
function untyped(p: EvmParam): boolean {
  return p.type === 'word' || p.type === 'trailing'
}

function isAddress(p: EvmParam): boolean {
  return p.type === 'address' || p.type.startsWith('address')
}
</script>

<template>
  <div v-if="params && params.length" class="space-y-2">
    <div
      v-for="(p, i) in params"
      :key="i"
      class="grid grid-cols-[auto_1fr] items-baseline gap-x-3 gap-y-1 sm:grid-cols-[10rem_1fr]"
    >
      <div class="flex items-baseline gap-2">
        <span v-if="p.name" class="font-mono text-xs text-foreground">{{ p.name }}</span>
        <span v-else class="font-mono text-xs text-muted-foreground">#{{ i }}</span>
        <span class="font-mono text-[0.7rem] text-muted-foreground">{{ p.type }}</span>
      </div>

      <div class="min-w-0">
        <!-- An address: linked, in the native spelling, and marked when it
             is a contract rather than somebody a wallet could pay. -->
        <template v-if="isAddress(p)">
          <span v-if="isZeroAddress(p.value)" class="font-mono text-xs text-muted-foreground">
            null address
          </span>
          <template v-else>
            <HashLink :value="p.value" :zenon="p.zenon" :to="`/evm/account/${p.value}`" full />
            <!-- An address argument that is a contract rather than a
                 recipient. Worth marking inline: in a transfer, the
                 difference between the two is the difference between
                 delivering the tokens and stranding them. -->
            <Badge v-if="p.sendable === false" variant="outline" class="ml-1 text-[0.65rem]">
              contract, not payable
            </Badge>
          </template>
        </template>

        <!-- An array: its elements, indented. -->
        <template v-else-if="p.items && p.items.length">
          <div class="text-xs text-muted-foreground">{{ p.value }}</div>
          <div class="mt-1 border-l border-border pl-3">
            <ParamList :params="p.items" />
          </div>
        </template>

        <!-- A word with no signature to type it: every reading, none claimed. -->
        <template v-else-if="untyped(p)">
          <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span class="font-mono text-xs tabular-nums">{{ p.value }}</span>
            <span class="font-mono text-[0.7rem] text-muted-foreground break-all">{{ p.hex }}</span>
          </div>
          <div v-if="p.zenon" class="mt-0.5 flex items-baseline gap-2">
            <Badge variant="outline" class="text-[0.65rem]">as address</Badge>
            <HashLink :value="p.hex!" :zenon="p.zenon" :to="`/evm/account/${p.zenon}`" full :copy="false" />
          </div>
        </template>

        <!-- Everything else: the decoded value, as text. -->
        <span v-else class="font-mono text-xs break-all tabular-nums">{{ p.value }}</span>
      </div>
    </div>
  </div>
</template>
