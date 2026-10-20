<script setup lang="ts">
import {Heading} from 'nom-ui'

// One documentation section. The id is what /#/docs/<id> scrolls to, so it is
// part of the URL contract: renaming one breaks links people have already sent.
defineProps<{id: string; title: string}>()
</script>

<template>
  <section
    :id="`doc-${id}`"
    class="doc scroll-mt-24 border-t border-border pt-8 first:border-0 first:pt-0"
  >
    <Heading :level="2" class="text-xl">{{ title }}</Heading>
    <slot />
  </section>
</template>
