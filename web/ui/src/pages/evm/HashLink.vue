<script setup lang="ts">
import {computed} from 'vue'
import {CopyButton, Tooltip, TooltipContent, TooltipProvider, TooltipTrigger} from 'nom-ui'
import {shortHex} from './evm'

// A hash or address: truncated, monospace, linked, and copyable.
//
// nom-ui's own Address component does all of this except the one thing an
// explorer needs most — it forces target="_blank", because it is built for
// linking OUT to a block explorer. This is the same shape wired to the
// router instead, since here the explorer is where the link already is.
const props = withDefaults(
  defineProps<{
    value: string
    /**
     * The native spelling of an address. When set it is what the reader
     * sees; `value` stays the link target and the canonical form, so a page
     * URL does not change with the spelling shown on it.
     */
    zenon?: string
    /** Router path. Omitted renders plain text — for a value with no page. */
    to?: string
    start?: number
    end?: number
    copy?: boolean
    /** Show the whole value instead of truncating it. */
    full?: boolean
    /** Dim it, for values that are context rather than the subject. */
    muted?: boolean
  }>(),
  {start: 8, end: 6, copy: true, full: false, muted: false},
)

const shown = computed(() => props.zenon || props.value)
const display = computed(() => (props.full ? shown.value : shortHex(shown.value, props.start, props.end)))
const truncated = computed(() => display.value !== shown.value)
</script>

<template>
  <span class="inline-flex max-w-full items-center gap-1">
    <TooltipProvider v-if="truncated">
      <Tooltip>
        <TooltipTrigger as-child>
          <RouterLink
            v-if="to"
            :to="to"
            class="font-mono text-xs text-primary hover:underline"
            :class="muted && 'text-muted-foreground hover:text-foreground'"
          >
            {{ display }}
          </RouterLink>
          <span v-else class="font-mono text-xs" :class="muted && 'text-muted-foreground'">
            {{ display }}
          </span>
        </TooltipTrigger>
        <TooltipContent class="font-mono text-xs">
          <div>{{ shown }}</div>
          <div v-if="zenon" class="mt-1 opacity-60">{{ value }}</div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>

    <template v-else>
      <RouterLink
        v-if="to"
        :to="to"
        class="font-mono text-xs break-all text-primary hover:underline"
        :class="muted && 'text-muted-foreground hover:text-foreground'"
      >
        {{ display }}
      </RouterLink>
      <span v-else class="font-mono text-xs break-all" :class="muted && 'text-muted-foreground'">
        {{ display }}
      </span>
    </template>

    <CopyButton v-if="copy" :value="shown" size="icon-sm" class="size-6 shrink-0 opacity-60 hover:opacity-100" />
  </span>
</template>
