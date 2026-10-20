<script setup lang="ts">
import {onMounted, ref} from 'vue'
import {Badge, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from 'nom-ui'
import PageHeading from '@/components/PageHeading.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingState from '@/components/LoadingState.vue'
import {count, when} from '@/core/format'
import {adminApi} from '../api'
import type {AdminStatus} from '../types'

const status = ref<AdminStatus | null>(null)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    status.value = await adminApi.status()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeading
      title="Status"
      description="Read-only. Maintenance that touches a running indexer — reindex, dropping checkpoints — stays on the CLI, where it belongs run deliberately: zcribe reindex, zcribe snapshot."
    />

    <ErrorState v-if="error" :message="error" />
    <LoadingState v-else-if="loading" />
    <div v-else-if="status" class="space-y-8">
      <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <div class="rounded-xl border border-border bg-card p-4">
          <p class="text-ledger text-muted-foreground">sync height</p>
          <p class="mt-1 font-mono text-lg tabular-nums">{{ count(status.info.syncHeight) }}</p>
        </div>
        <div class="rounded-xl border border-border bg-card p-4">
          <p class="text-ledger text-muted-foreground">node height</p>
          <p class="mt-1 font-mono text-lg tabular-nums">{{ count(status.info.nodeHeight) }}</p>
        </div>
        <div class="rounded-xl border border-border bg-card p-4">
          <p class="text-ledger text-muted-foreground">events</p>
          <p class="mt-1 font-mono text-lg tabular-nums">{{ count(status.info.events) }}</p>
        </div>
        <div class="rounded-xl border border-border bg-card p-4">
          <p class="text-ledger text-muted-foreground">redacted / hidden</p>
          <p class="mt-1 font-mono text-lg tabular-nums">{{ count(status.moderatedCount) }}</p>
        </div>
      </div>

      <div class="space-y-0 text-sm">
        <div class="flex items-center justify-between border-b border-border py-2">
          <span class="text-muted-foreground">indexer version</span>
          <span class="font-mono"
            >{{ status.info.indexerVersion }} · protocol v{{ status.info.protocolVersion }}</span
          >
        </div>
        <div class="flex items-center justify-between border-b border-border py-2">
          <span class="text-muted-foreground">content origin</span>
          <span class="font-mono">{{ status.info.contentOrigin }}</span>
        </div>
        <div class="flex items-center justify-between border-b border-border py-2">
          <span class="text-muted-foreground">scripts (recursion)</span>
          <Badge :variant="status.info.scriptsEnabled ? 'default' : 'secondary'">
            {{ status.info.scriptsEnabled ? 'enabled' : 'disabled' }}
          </Badge>
        </div>
        <div class="flex items-center justify-between border-b border-border py-2">
          <span class="text-muted-foreground">confirmation depth</span>
          <span class="font-mono">{{ status.info.confirmationDepth }} momentums</span>
        </div>
        <div class="flex items-center justify-between border-b border-border py-2">
          <span class="text-muted-foreground">CDN purge</span>
          <Badge :variant="status.purgeConfigured ? 'default' : 'secondary'">
            {{ status.purgeConfigured ? 'configured' : 'not configured' }}
          </Badge>
        </div>
        <div class="flex items-center justify-between py-2">
          <span class="text-muted-foreground">state root at tip</span>
          <span class="truncate font-mono text-xs">{{ status.info.stateRoot }}</span>
        </div>
      </div>

      <div>
        <h2 class="mb-3 text-sm font-semibold">Checkpoints</h2>
        <p v-if="!status.snapshots.length" class="text-sm text-muted-foreground">
          no checkpoints (tip {{ count(status.tipHeight) }})
        </p>
        <Table v-else>
          <TableHeader>
            <TableRow>
              <TableHead>height</TableHead>
              <TableHead>depth</TableHead>
              <TableHead>events</TableHead>
              <TableHead>taken</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="s in status.snapshots" :key="s.height">
              <TableCell class="font-mono tabular-nums">{{ count(s.height) }}</TableCell>
              <TableCell class="font-mono tabular-nums">{{ s.depth }}</TableCell>
              <TableCell class="font-mono tabular-nums">{{ count(s.events) }}</TableCell>
              <TableCell class="text-muted-foreground">{{ when(s.createdAt) }}</TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
    </div>
  </div>
</template>
