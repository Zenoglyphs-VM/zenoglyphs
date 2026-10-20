<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {SearchIcon, ShieldAlertIcon, UploadIcon} from '@lucide/vue'
import {
  Badge,
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  Textarea,
  useToast,
} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingState from '@/components/LoadingState.vue'
import {bytes, short} from '@/core/format'
import {adminApi, ApiError} from '../api'
import type {AdminModerationLookup, AdminModerationRow, ModerationStatus} from '../types'

const {show: toast} = useToast()

function statusVariant(s: ModerationStatus): 'outline' | 'secondary' | 'destructive' {
  return s === 'hidden' ? 'destructive' : s === 'redacted' ? 'secondary' : 'outline'
}

// --- everything currently restricted ---
const rows = ref<AdminModerationRow[]>([])
const rowsLoading = ref(true)
const rowsError = ref('')

async function loadRows() {
  rowsLoading.value = true
  rowsError.value = ''
  try {
    rows.value = (await adminApi.moderationList()).moderation
  } catch (e) {
    rowsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    rowsLoading.value = false
  }
}
onMounted(loadRows)

// --- look up one asset by id or inscription number ---
const query = ref('')
const lookup = ref<AdminModerationLookup | null>(null)
const lookupError = ref('')
const looking = ref(false)

async function search() {
  const q = query.value.trim()
  if (!q) return
  looking.value = true
  lookupError.value = ''
  lookup.value = null
  try {
    lookup.value = await adminApi.moderationGet(q)
  } catch (e) {
    lookupError.value = e instanceof ApiError ? e.message : 'could not reach the indexer'
  } finally {
    looking.value = false
  }
}

// --- the set-policy dialog, opened from either the lookup card or a row ---
const dialogOpen = ref(false)
const dialogAssetId = ref('')
const dialogStatus = ref<ModerationStatus>('redacted')
const dialogReason = ref('')
const dialogUnseen = ref(false)
const dialogPurge = ref(true)
const dialogSaving = ref(false)

function openDialog(
  assetId: string,
  currentStatus: ModerationStatus,
  currentReason: string,
  unseen: boolean,
) {
  dialogAssetId.value = assetId
  // "visible" is the default action offered — the common case opening this
  // dialog is to restrict something, not to re-confirm it is already fine.
  dialogStatus.value = currentStatus === 'visible' ? 'redacted' : currentStatus
  dialogReason.value = currentReason
  dialogUnseen.value = unseen
  dialogPurge.value = true
  dialogOpen.value = true
}

async function confirmDialog() {
  dialogSaving.value = true
  try {
    const result = await adminApi.moderationSet(dialogAssetId.value, {
      status: dialogStatus.value,
      reason: dialogReason.value,
      unseen: dialogUnseen.value,
      purge: dialogPurge.value,
    })
    toast(`${short(dialogAssetId.value, 10)} set to ${result.status}`, 'success')
    if (result.purge?.attempted && !result.purge.ok) {
      toast(`policy saved, but the CDN purge failed: ${result.purge.error}`, 'warning')
    }
    dialogOpen.value = false
    await loadRows()
    if (lookup.value?.assetId === dialogAssetId.value) await search()
  } catch (e) {
    toast(e instanceof ApiError ? e.message : 'could not save', 'error')
  } finally {
    dialogSaving.value = false
  }
}

// --- bulk import ---
const importText = ref('')
const importPurge = ref(false)
const importing = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

function pickFile() {
  fileInput.value?.click()
}
function onFileChosen(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  file.text().then((t) => {
    importText.value = t
  })
}

async function runImport() {
  if (!importText.value.trim() || importing.value) return
  importing.value = true
  try {
    const result = await adminApi.moderationImport(importText.value, importPurge.value)
    toast(`blocked ${result.blocked} id(s)`, 'success')
    importText.value = ''
    await loadRows()
  } catch (e) {
    toast(e instanceof ApiError ? e.message : 'import failed', 'error')
  } finally {
    importing.value = false
  }
}
</script>

<template>
  <div class="space-y-10">
    <PageHeading
      title="Moderation"
      :count="`${rows.length} restricted`"
      description="Redacted stays searchable with content replaced by a placeholder on the frontend. Hidden is removed from listings and metadata entirely — reserve it for material that must not be served at all."
    />

    <section class="space-y-3">
      <form class="flex gap-2" @submit.prevent="search">
        <Input
          v-model="query"
          placeholder="Asset id (64-hex) or inscription number"
          class="font-mono"
        />
        <Button type="submit" :disabled="!query.trim() || looking">
          <SearchIcon class="size-4" />
          Look up
        </Button>
      </form>

      <ErrorState v-if="lookupError" :message="lookupError" />

      <div v-if="lookup" class="rounded-xl border border-border bg-card p-4">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <p v-if="lookup.found && lookup.asset" class="font-medium">
              {{ lookup.asset.name || `inscription #${lookup.asset.number}` }}
            </p>
            <p v-else class="text-muted-foreground">not indexed by this indexer yet</p>
            <p class="mt-0.5 truncate font-mono text-xs text-muted-foreground">
              {{ lookup.assetId }}
            </p>
            <p v-if="lookup.found && lookup.asset" class="mt-1 text-xs text-muted-foreground">
              {{ lookup.asset.mime }} · {{ bytes(lookup.asset.size) }} · #{{ lookup.asset.number }}
            </p>
          </div>
          <Badge :variant="statusVariant(lookup.status)">{{ lookup.status }}</Badge>
        </div>
        <p v-if="lookup.reason" class="mt-2 text-sm text-muted-foreground">
          reason: {{ lookup.reason }}
        </p>

        <div class="mt-4 flex flex-wrap gap-2">
          <Button
            v-if="lookup.found"
            as="a"
            :href="lookup.galleryUrl"
            target="_blank"
            rel="noopener"
            variant="outline"
            size="sm"
          >
            View in gallery ↗
          </Button>
          <Button
            size="sm"
            variant="outline"
            @click="openDialog(lookup.assetId, lookup.status, lookup.reason || '', !lookup.found)"
          >
            <ShieldAlertIcon class="size-4" />
            Change policy
          </Button>
        </div>
      </div>
    </section>

    <section>
      <h2 class="mb-3 text-sm font-semibold">Currently restricted</h2>
      <ErrorState v-if="rowsError" :message="rowsError" />
      <LoadingState v-else-if="rowsLoading" />
      <EmptyState v-else-if="!rows.length" message="No redactions or hides set." />
      <Table v-else>
        <TableHeader>
          <TableRow>
            <TableHead>asset</TableHead>
            <TableHead>status</TableHead>
            <TableHead>reason</TableHead>
            <TableHead class="text-right">actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="row in rows" :key="row.assetId">
            <TableCell>
              <p class="font-medium">
                {{ row.name || (row.found ? '(unnamed)' : 'not indexed yet') }}
              </p>
              <p class="font-mono text-xs text-muted-foreground">{{ short(row.assetId, 12) }}</p>
            </TableCell>
            <TableCell
              ><Badge :variant="statusVariant(row.status)">{{ row.status }}</Badge></TableCell
            >
            <TableCell class="max-w-xs truncate text-muted-foreground">{{ row.reason }}</TableCell>
            <TableCell class="text-right">
              <Button
                size="sm"
                variant="outline"
                @click="openDialog(row.assetId, row.status, row.reason || '', !row.found)"
              >
                Change
              </Button>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </section>

    <section class="grid gap-6 sm:grid-cols-2">
      <div class="space-y-2">
        <h2 class="text-sm font-semibold">Bulk import</h2>
        <p class="text-xs text-muted-foreground">
          One asset id per line, optional "# reason" — the format -export writes and
          <code class="font-mono">zcribe moderate -import</code> reads. Every id is fully hidden,
          never only redacted: a shared list is for material that must not be served at all.
        </p>
        <Textarea
          v-model="importText"
          rows="6"
          class="font-mono text-xs"
          placeholder="aa11…00 # reported for X"
        />
        <div class="flex flex-wrap items-center gap-3">
          <input ref="fileInput" type="file" accept=".txt" class="hidden" @change="onFileChosen" />
          <Button type="button" variant="outline" size="sm" @click="pickFile">
            <UploadIcon class="size-4" />
            Choose file…
          </Button>
          <label class="flex items-center gap-1.5 text-sm text-muted-foreground">
            <input v-model="importPurge" type="checkbox" class="size-3.5" />
            purge CDN after
          </label>
          <Button
            size="sm"
            class="ml-auto"
            :disabled="!importText.trim() || importing"
            @click="runImport"
          >
            {{ importing ? 'Importing…' : 'Import' }}
          </Button>
        </div>
      </div>

      <div class="space-y-2">
        <h2 class="text-sm font-semibold">Export</h2>
        <p class="text-xs text-muted-foreground">
          Download this indexer's blocklist — to share with another operator, or as a backup. Only
          assets with a restriction are included, in the same format bulk import reads.
        </p>
        <Button as="a" :href="adminApi.moderationExportURL" variant="outline" size="sm"
          >Download blocklist</Button
        >
      </div>
    </section>

    <Dialog v-model:open="dialogOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Set policy</DialogTitle>
          <DialogDescription class="truncate font-mono text-xs">{{
            dialogAssetId
          }}</DialogDescription>
        </DialogHeader>

        <div class="space-y-4">
          <div class="space-y-1.5">
            <Label>Status</Label>
            <Select
              :model-value="dialogStatus"
              @update:model-value="(v) => (dialogStatus = v as ModerationStatus)"
            >
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="visible">Visible — no restriction</SelectItem>
                <SelectItem value="redacted"
                  >Redacted — searchable, content replaced with a placeholder</SelectItem
                >
                <SelectItem value="hidden"
                  >Hidden — removed from listings and metadata entirely</SelectItem
                >
              </SelectContent>
            </Select>
          </div>

          <div class="space-y-1.5">
            <Label>Reason (for your own records)</Label>
            <Textarea v-model="dialogReason" rows="2" />
          </div>

          <p v-if="dialogUnseen" class="text-sm text-muted-foreground">
            This id has not been indexed yet. Saving pre-blocks it — the policy above applies the
            moment this indexer reaches it.
          </p>

          <label
            v-if="dialogStatus !== 'visible'"
            class="flex items-center gap-2 text-sm text-muted-foreground"
          >
            <input v-model="dialogPurge" type="checkbox" class="size-3.5" />
            purge the CDN in front of the content origin now
          </label>
        </div>

        <DialogFooter>
          <Button variant="outline" :disabled="dialogSaving" @click="dialogOpen = false"
            >Cancel</Button
          >
          <Button :disabled="dialogSaving" @click="confirmDialog">{{
            dialogSaving ? 'Saving…' : 'Save'
          }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
