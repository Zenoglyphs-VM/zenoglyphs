<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {Toaster} from 'nom-ui'
import ZcribeMark from '@/components/ZcribeMark.vue'
import {adminApi, onUnauthorized} from './api'
import LoginView from './views/LoginView.vue'
import ModerationView from './views/ModerationView.vue'
import StatusView from './views/StatusView.vue'
import SettingsView from './views/SettingsView.vue'

type Tab = 'moderation' | 'status' | 'settings'
const tabs: Tab[] = ['moderation', 'status', 'settings']

// No vue-router here, deliberately: this is one page with two sections, not
// an app with deep-linkable resources, so a hash read once at load and
// written on click is the whole navigation model.
const tab = ref<Tab>(
  tabs.includes(location.hash.slice(1) as Tab) ? (location.hash.slice(1) as Tab) : 'moderation',
)
function setTab(t: Tab) {
  tab.value = t
  location.hash = t
}

const checking = ref(true)
const enabled = ref(true)
const authenticated = ref(false)

async function checkSession() {
  checking.value = true
  try {
    const s = await adminApi.session()
    enabled.value = s.enabled
    authenticated.value = s.authenticated
  } catch {
    // Unreachable API: treat as "not authenticated" rather than wedge the
    // panel on a spinner. The login form's own request will surface why.
    authenticated.value = false
  } finally {
    checking.value = false
  }
}

// A 401 from anywhere in the app — an expired session, a logout elsewhere —
// drops straight back to the login view instead of leaving a view stuck
// mid-error.
onUnauthorized(() => {
  authenticated.value = false
})

async function logout() {
  try {
    await adminApi.logout()
  } catch {
    /* the session is revoked server-side either way per admin.go; a network
       error here just means the client didn't hear back */
  }
  authenticated.value = false
}

onMounted(checkSession)
</script>

<template>
  <div class="flex min-h-screen flex-col">
    <header
      class="sticky top-0 z-40 border-b border-border bg-background/85 backdrop-blur supports-[backdrop-filter]:bg-background/70"
    >
      <div class="mx-auto flex h-14 max-w-5xl items-center gap-4 px-4 sm:px-6">
        <!-- A plain <a>, not a RouterLink: there is no router in this app (one
             page, three sections) and "/" is a different application served by
             the same origin, so this has to be a real navigation. Without it
             the only way back to the viewer was editing the URL by hand. -->
        <a
          href="/"
          class="flex shrink-0 items-center gap-2 transition-opacity hover:opacity-80"
          title="Back to the public viewer"
        >
          <ZcribeMark class="size-6" />
          <span class="text-base font-semibold tracking-tight">
            <span class="text-primary">z</span>cribe
            <span class="text-muted-foreground">admin</span>
          </span>
        </a>

        <nav v-if="authenticated" class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
          <button
            v-for="t in tabs"
            :key="t"
            type="button"
            class="rounded-md px-2.5 py-1.5 text-sm font-medium capitalize whitespace-nowrap transition-colors"
            :class="
              tab === t
                ? 'bg-accent text-accent-foreground'
                : 'text-muted-foreground hover:text-foreground'
            "
            @click="setTab(t)"
          >
            {{ t }}
          </button>
        </nav>
        <div v-else class="flex-1" />

        <a
          href="/"
          class="shrink-0 text-sm whitespace-nowrap text-muted-foreground transition-colors hover:text-foreground"
        >
          View site
        </a>

        <button
          v-if="authenticated"
          type="button"
          class="text-sm text-muted-foreground transition-colors hover:text-foreground"
          @click="logout"
        >
          Log out
        </button>
      </div>
    </header>

    <main class="mx-auto w-full max-w-5xl flex-1 px-4 py-8 sm:px-6">
      <div v-if="checking" class="py-16 text-center text-sm text-muted-foreground">Loading…</div>

      <div
        v-else-if="!enabled"
        class="rounded-xl border border-dashed border-border px-6 py-16 text-center"
      >
        <p class="text-sm text-muted-foreground">
          The admin panel is not configured on this indexer.
        </p>
        <p class="mt-2 font-mono text-xs text-muted-foreground/80">
          set ZCRIBE_ADMIN_PASSWORD and restart zcribe serve
        </p>
      </div>

      <LoginView v-else-if="!authenticated" @authenticated="checkSession" />

      <template v-else>
        <ModerationView v-if="tab === 'moderation'" />
        <StatusView v-else-if="tab === 'status'" />
        <SettingsView v-else />
      </template>
    </main>

    <Toaster />
  </div>
</template>
