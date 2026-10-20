<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {KeyIcon, TrashIcon} from '@lucide/vue'
import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
  useToast,
} from 'nom-ui'
import ErrorState from '@/components/ErrorState.vue'
import {adminApi, ApiError} from '../api'

const {show: toast} = useToast()

// --- change password ---
//
// The backend re-checks currentPassword itself (see
// handleAdminChangePassword) — a session cookie proves "was logged in", not
// "still is the operator", and this form is precisely the boundary between
// those two. The confirm field below is a client-side typo guard only; the
// server has no opinion on it.
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const changing = ref(false)
const changeError = ref('')

const mismatch = () => newPassword.value !== '' && newPassword.value !== confirmPassword.value
const tooShort = () => newPassword.value.length > 0 && newPassword.value.length < 12

async function changePassword() {
  if (!currentPassword.value || !newPassword.value || changing.value) return
  if (mismatch()) {
    changeError.value = 'new password and confirmation do not match'
    return
  }
  changing.value = true
  changeError.value = ''
  try {
    await adminApi.changePassword(currentPassword.value, newPassword.value)
    toast('password changed — every other session was signed out', 'success')
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  } catch (e) {
    // A 401 here means the CURRENT password was wrong, not that the session
    // expired (the request would not have reached the handler otherwise) —
    // so this does not go through onUnauthorized's "drop to login" path.
    changeError.value = e instanceof ApiError ? e.message : 'could not reach the indexer'
  } finally {
    changing.value = false
  }
}

// --- cache ---
//
// Content is served immutable, so purging a specific asset (the Moderation
// tab's own purge action, run automatically whenever a policy is set) is the
// normal case and is cheap and precise. This button is the blunt instrument
// for everything else: a CDN reconfigured after content was already cached
// under the old settings, or simply not knowing which ids are stale. It is
// gated behind a confirm dialog because "evict literally everything" can
// spike origin load while the cache refills, which per-id purging never does.
const purgeConfigured = ref(false)
const purgeAllOpen = ref(false)
const purgingAll = ref(false)
const statusError = ref('')

async function loadPurgeStatus() {
  try {
    purgeConfigured.value = (await adminApi.status()).purgeConfigured
  } catch (e) {
    statusError.value = e instanceof Error ? e.message : String(e)
  }
}
onMounted(loadPurgeStatus)

async function confirmPurgeAll() {
  purgingAll.value = true
  try {
    const {purge} = await adminApi.purgeAll()
    if (purge.ok) {
      toast(`entire cache flushed via ${purge.backend}`, 'success')
    } else {
      toast(`cache flush failed: ${purge.error}`, 'error')
    }
  } catch (e) {
    toast(e instanceof ApiError ? e.message : 'could not reach the indexer', 'error')
  } finally {
    purgingAll.value = false
    purgeAllOpen.value = false
  }
}
</script>

<template>
  <div class="max-w-lg space-y-8">
    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2 text-base">
          <KeyIcon class="size-4 text-muted-foreground" />
          Change password
        </CardTitle>
        <CardDescription>
          Changing it signs out every other session immediately — this one gets a fresh one
          automatically.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form class="space-y-4" @submit.prevent="changePassword">
          <div class="space-y-1.5">
            <Label for="current-password">Current password</Label>
            <Input
              id="current-password"
              v-model="currentPassword"
              type="password"
              autocomplete="current-password"
              :disabled="changing"
            />
          </div>
          <div class="space-y-1.5">
            <Label for="new-password">New password</Label>
            <Input
              id="new-password"
              v-model="newPassword"
              type="password"
              autocomplete="new-password"
              :disabled="changing"
            />
            <p v-if="tooShort()" class="text-xs text-destructive">at least 12 characters</p>
          </div>
          <div class="space-y-1.5">
            <Label for="confirm-password">Confirm new password</Label>
            <Input
              id="confirm-password"
              v-model="confirmPassword"
              type="password"
              autocomplete="new-password"
              :disabled="changing"
            />
            <p v-if="mismatch()" class="text-xs text-destructive">passwords do not match</p>
          </div>

          <Alert v-if="changeError" variant="destructive">
            <AlertDescription>{{ changeError }}</AlertDescription>
          </Alert>

          <Button
            type="submit"
            :disabled="changing || !currentPassword || newPassword.length < 12 || mismatch()"
          >
            {{ changing ? 'Changing…' : 'Change password' }}
          </Button>
        </form>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2 text-base">
          <TrashIcon class="size-4 text-muted-foreground" />
          Cache
        </CardTitle>
        <CardDescription>
          Content is served immutable, so moderation already purges specific ids automatically. This
          flushes everything — reach for it when the CDN itself changed, not when you know which ids
          are stale.
        </CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <ErrorState v-if="statusError" :message="statusError" />
        <div class="flex items-center justify-between">
          <span class="text-sm text-muted-foreground">CDN purge</span>
          <Badge :variant="purgeConfigured ? 'default' : 'secondary'">
            {{ purgeConfigured ? 'configured' : 'not configured' }}
          </Badge>
        </div>
        <Button variant="destructive" :disabled="!purgeConfigured" @click="purgeAllOpen = true">
          Flush entire cache
        </Button>
        <p v-if="!purgeConfigured" class="text-xs text-muted-foreground">
          Set ZCRIBE_CF_ZONE / ZCRIBE_CF_TOKEN or ZCRIBE_PURGE_WEBHOOK and restart to enable purging
          (see the CDN purge doc — <code>zcribe moderate -purge-help</code>).
        </p>
      </CardContent>
    </Card>

    <Dialog v-model:open="purgeAllOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Flush the entire cache?</DialogTitle>
          <DialogDescription>
            Every cached object is evicted, not just what is currently moderated. The origin will
            see a traffic spike until the cache refills. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" :disabled="purgingAll" @click="purgeAllOpen = false">
            Cancel
          </Button>
          <Button variant="destructive" :disabled="purgingAll" @click="confirmPurgeAll">
            {{ purgingAll ? 'Flushing…' : 'Flush everything' }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
