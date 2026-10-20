<script setup lang="ts">
import {ref} from 'vue'
import {LockIcon} from '@lucide/vue'
import {
  Alert,
  AlertDescription,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  Input,
} from 'nom-ui'
import {adminApi, ApiError} from '../api'

const emit = defineEmits<{authenticated: []}>()

const password = ref('')
const submitting = ref(false)
const error = ref('')

async function submit() {
  if (!password.value || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    await adminApi.login(password.value)
    password.value = ''
    emit('authenticated')
  } catch (e) {
    // The backend deliberately returns the same "invalid password" message
    // whether the id is wrong or unknown — nothing here narrows that; a
    // lockout (429) carries its own "try again in Ns" text through unchanged.
    error.value = e instanceof ApiError ? e.message : 'could not reach the indexer'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="mx-auto max-w-sm py-16">
    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2 text-lg">
          <LockIcon class="size-4 text-muted-foreground" />
          Admin sign-in
        </CardTitle>
      </CardHeader>
      <CardContent>
        <form class="space-y-4" @submit.prevent="submit">
          <Input
            v-model="password"
            type="password"
            placeholder="Password"
            autocomplete="current-password"
            autofocus
            :disabled="submitting"
          />
          <Alert v-if="error" variant="destructive">
            <AlertDescription>{{ error }}</AlertDescription>
          </Alert>
          <Button type="submit" class="w-full" :disabled="!password || submitting">
            {{ submitting ? 'Signing in…' : 'Sign in' }}
          </Button>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
