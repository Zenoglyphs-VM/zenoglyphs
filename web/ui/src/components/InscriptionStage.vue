<script setup lang="ts">
import {ref, watchEffect} from 'vue'
import {BanIcon, BinaryIcon, ExternalLinkIcon} from '@lucide/vue'
import {Button} from 'nom-ui'
import {useIndexer} from '@/core/composables/useIndexer'
import {fetchText} from '@/core/api'
import {markdown} from '@/core/markdown'
import {bytes} from '@/core/format'
import type {Asset} from '@/types'

// Full-size render for the detail page.
//
// Same rule as the thumbnail: bytes this viewer did not write are rendered in
// an element that cannot reach this document, or as text, or not at all. The
// one exception is markdown, and only because the renderer escapes its input
// before adding any of its own tags — see core/markdown.ts.
const props = defineProps<{asset: Asset}>()

const {sandboxTokens} = useIndexer()

const text = ref('')
const html = ref('')
const loadError = ref('')

watchEffect(() => {
  const a = props.asset
  text.value = ''
  html.value = ''
  loadError.value = ''
  if (a.dataWithheld) return

  if (a.class === 'markdown') {
    fetchText(a.contentUrl)
      .then((t) => {
        html.value = markdown(t)
      })
      .catch((e: Error) => {
        loadError.value = `could not load: ${e.message}`
      })
  } else if (a.class === 'text' || a.class === 'code') {
    fetchText(a.contentUrl)
      .then((t) => {
        text.value = t
      })
      .catch((e: Error) => {
        loadError.value = `could not load: ${e.message}`
      })
  }
})
</script>

<template>
  <div
    class="flex min-h-[20rem] items-center justify-center overflow-hidden rounded-xl border border-border bg-card shadow-sm"
    :class="{'items-stretch': ['markdown', 'text', 'code', 'iframe', 'pdf'].includes(asset.class)}"
  >
    <!-- Redacted: the metadata beside this panel is still served and still
         true, and the bytes are not offered anywhere on the page. What the
         chain holds is unaffected — this is one operator's serving policy,
         which is why the message says who is refusing rather than implying the
         inscription is gone. -->
    <div
      v-if="asset.dataWithheld"
      class="flex max-w-sm flex-col items-center gap-2 p-10 text-center text-muted-foreground"
    >
      <BanIcon class="size-8" />
      <span class="text-sm">Content redacted by this indexer.</span>
      <span class="text-xs text-muted-foreground/80">
        The inscription is still on the chain; this operator does not serve its bytes. Another
        indexer may.
      </span>
    </div>

    <img
      v-else-if="asset.class === 'image'"
      :src="asset.contentUrl"
      :alt="asset.name || asset.id"
      class="max-h-[75vh] w-full object-contain"
    />

    <iframe
      v-else-if="asset.class === 'iframe'"
      :src="asset.contentUrl"
      :sandbox="sandboxTokens"
      class="h-[70vh] w-full border-0"
    />

    <embed
      v-else-if="asset.class === 'pdf'"
      :src="asset.contentUrl"
      type="application/pdf"
      class="h-[70vh] w-full"
    />

    <audio
      v-else-if="asset.class === 'audio'"
      :src="asset.contentUrl"
      controls
      class="w-full p-6"
    />

    <video
      v-else-if="asset.class === 'video'"
      :src="asset.contentUrl"
      controls
      class="max-h-[75vh] w-full"
    />

    <div v-else-if="asset.class === 'markdown'" class="w-full overflow-auto p-6">
      <p v-if="loadError" class="text-sm text-destructive">{{ loadError }}</p>
      <!-- markdown() escapes its input before emitting any tag of its own, so
           by here there is no inscription-supplied markup left in the string.
           See core/markdown.ts. -->
      <!-- eslint-disable-next-line vue/no-v-html -->
      <div v-else class="inscribed-doc" v-html="html" />
    </div>

    <div
      v-else-if="asset.class === 'text' || asset.class === 'code'"
      class="w-full overflow-auto p-6"
    >
      <p v-if="loadError" class="text-sm text-destructive">{{ loadError }}</p>
      <pre v-else class="font-mono text-xs leading-relaxed whitespace-pre-wrap">{{ text }}</pre>
    </div>

    <!-- Binary types are described, never executed or guessed at. -->
    <div v-else class="flex flex-col items-center gap-3 p-10 text-center text-muted-foreground">
      <BinaryIcon class="size-8" />
      <span class="font-mono text-xs">{{ asset.mime }} · {{ bytes(asset.size) }}</span>
      <Button
        as="a"
        :href="asset.contentUrl"
        target="_blank"
        rel="noopener"
        variant="outline"
        size="sm"
      >
        <ExternalLinkIcon />
        Open raw bytes
      </Button>
    </div>
  </div>
</template>
