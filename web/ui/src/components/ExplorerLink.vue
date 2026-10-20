<script setup lang="ts">
import {computed} from 'vue'
import {ExternalLinkIcon} from '@lucide/vue'
import {useExplorer, type ExplorerTarget} from '@/core/explorer'

// The "verify this on chain" affordance, deliberately small.
//
// Almost every hash in this viewer already has an in-app destination: an asset
// id opens the inscription, an address filters the gallery. Turning those into
// explorer links would take the navigation away; putting the explorer link
// beside them keeps both. So this renders an icon only — it sits next to a
// value rather than replacing it — and renders nothing at all when the
// configured explorer has no page for that kind of object.
const props = defineProps<{
  target: ExplorerTarget
  value: string | number | undefined
  /** Overrides the default "view <target> on <explorer>" tooltip. */
  title?: string
}>()

const {name, url} = useExplorer()

const href = computed(() => url(props.target, props.value))
const label = computed(() => props.title ?? `View ${props.target} on ${name.value}`)
</script>

<template>
  <a
    v-if="href"
    :href="href"
    target="_blank"
    rel="noopener noreferrer"
    :title="label"
    :aria-label="label"
    class="inline-flex shrink-0 items-center align-middle text-muted-foreground/60 transition-colors hover:text-info"
  >
    <ExternalLinkIcon class="size-3.5" />
  </a>
</template>
