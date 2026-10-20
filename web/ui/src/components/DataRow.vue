<script setup lang="ts">
defineProps<{
  label: string
  /**
   * The label is author-supplied, so render it as written.
   *
   * Ledger labels are uppercase, which is right for the fixed vocabulary this
   * app chose ("momentum", "issued to") and wrong for a key someone put in an
   * inscription: `aFieldFromTheFuture` uppercases to a word nobody can read,
   * and the casing was information the author encoded. Extension and attribute
   * keys pass this; everything else does not.
   */
  raw?: boolean
}>()
</script>

<template>
  <!-- display: contents so each row's two cells join the parent grid, which is
       what keeps every value in one column instead of each row aligning itself. -->
  <div class="contents">
    <dt
      class="border-b border-border/60 py-2 text-muted-foreground sm:border-b-0 sm:pt-2.5"
      :class="raw ? 'font-mono text-xs break-all' : 'text-ledger'"
    >
      {{ label }}
    </dt>
    <dd class="min-w-0 border-b border-border/60 pb-2 text-sm break-words sm:pt-2">
      <slot />
    </dd>
  </div>
</template>
