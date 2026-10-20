import {computed, ref} from 'vue'
import {api} from '@/core/api'
import type {Info} from '@/types'

// One shared copy of /info for the whole app. Every view needs some of it —
// the sandbox policy, the mime allowlist, the confirmation depth — and polling
// it once here keeps those answers consistent across views rather than each
// one holding a slightly different snapshot.
const info = ref<Info | null>(null)
const error = ref<string | null>(null)
let timer: number | null = null

/** How far this indexer trails the node, in momentums. */
const behind = computed(() => {
  if (!info.value) return 0
  return Math.max(0, info.value.nodeHeight - info.value.syncHeight)
})

// Five momentums is roughly ten seconds of chain: close enough that calling it
// "syncing" would be noise, far enough that a stalled indexer shows up.
const synced = computed(() => behind.value <= 5)

const reachable = computed(() => info.value !== null && error.value === null)

/** The content origin, which is a DIFFERENT origin to this page on purpose. */
const contentOrigin = computed(() => info.value?.contentOrigin ?? '')

/**
 * The sandbox token list for inscribed HTML and SVG.
 *
 * allow-scripts is what makes recursion work: an inscription that fetches
 * another inscription's bytes at render time — the shared-library pattern —
 * needs to be able to run at all. The indexer decides whether to permit it
 * (`zcribe serve -scripts`) and says so in /info, so this page never renders
 * more than the origin serving the bytes is willing to.
 *
 * allow-same-origin is withheld on purpose and must stay withheld. Without it
 * the frame gets an opaque origin, so one inscription's script can never reach
 * another inscription frame's DOM or storage even though both are served from
 * the same host. It is not needed for recursion either: the content origin
 * answers with Access-Control-Allow-Origin: *, so a fetch from an opaque
 * origin still reads the response. Granting it would buy nothing and cost the
 * isolation between inscriptions.
 */
const sandboxTokens = computed(() => (info.value?.scriptsEnabled ? 'allow-scripts' : ''))

async function refresh(): Promise<Info> {
  try {
    const next = await api.info()
    info.value = next
    error.value = null
    return next
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    throw e
  }
}

function startPolling(intervalMs = 5000) {
  if (timer !== null) return
  timer = window.setInterval(() => {
    refresh().catch(() => {
      /* the status indicator already shows the failure */
    })
  }, intervalMs)
}

export function useIndexer() {
  return {
    info,
    error,
    behind,
    synced,
    reachable,
    contentOrigin,
    sandboxTokens,
    refresh,
    startPolling,
  }
}
