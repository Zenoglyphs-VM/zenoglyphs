<script setup lang="ts">
import {ref, watchEffect} from 'vue'
import {
  BanIcon,
  BinaryIcon,
  BracesIcon,
  FileTextIcon,
  FileTypeIcon,
  MusicIcon,
  VideoIcon,
} from '@lucide/vue'
import {useIndexer} from '@/core/composables/useIndexer'
import {fetchText} from '@/core/api'
import {baseMime} from '@/core/format'
import type {Asset} from '@/types'

// The gallery thumbnail: cheap, and never larger than the card.
//
// The class comes from the indexer, which derives it from the mime allowlist.
// An unknown class falls through to the glyph path, which never renders the
// bytes at all.
const props = defineProps<{asset: Asset}>()

const {sandboxTokens} = useIndexer()

const preview = ref('')
watchEffect(() => {
  const a = props.asset
  if (a.dataWithheld) return
  if (a.class === 'text' || a.class === 'markdown' || a.class === 'code') {
    fetchText(a.contentUrl, 600)
      .then((t) => {
        preview.value = t
      })
      .catch(() => {
        /* a thumbnail that will not load is left blank, not errored over */
      })
  }
})

const glyphs = {
  pdf: FileTypeIcon,
  audio: MusicIcon,
  video: VideoIcon,
  code: BracesIcon,
  text: FileTextIcon,
  markdown: FileTextIcon,
  binary: BinaryIcon,
} as const
</script>

<template>
  <!-- "redacted" is the operator's own word for this state (the admin panel's
       three-way choice is visible / redacted / hidden), and it is the accurate
       one: the inscription is still indexed, still numbered, still described —
       only its bytes are refused here. -->
  <div
    v-if="asset.dataWithheld"
    class="flex size-full flex-col items-center justify-center gap-1.5 text-muted-foreground"
  >
    <BanIcon class="size-6" />
    <span class="text-ledger">redacted</span>
  </div>

  <img
    v-else-if="asset.class === 'image'"
    :src="asset.contentUrl"
    :alt="asset.name || asset.id"
    loading="lazy"
    class="size-full object-contain"
  />

  <!-- Inscribed HTML and SVG can run script. Forms, navigation and same-origin
       access stay off in every case; see useIndexer's sandboxTokens. -->
  <iframe
    v-else-if="asset.class === 'iframe'"
    :src="asset.contentUrl"
    :sandbox="sandboxTokens"
    loading="lazy"
    tabindex="-1"
    class="pointer-events-none size-full border-0"
  />

  <video
    v-else-if="asset.class === 'video'"
    :src="asset.contentUrl"
    muted
    preload="metadata"
    class="size-full object-contain"
  />

  <pre
    v-else-if="asset.class === 'text' || asset.class === 'markdown' || asset.class === 'code'"
    class="size-full overflow-hidden p-3 font-mono text-[10px] leading-snug text-muted-foreground"
    >{{ preview }}</pre>

  <div
    v-else
    class="flex size-full flex-col items-center justify-center gap-1.5 text-muted-foreground"
  >
    <component :is="glyphs[asset.class] ?? glyphs.binary" class="size-6" />
    <span class="text-ledger">{{ baseMime(asset.mime).split('/')[1] || asset.class }}</span>
  </div>
</template>
