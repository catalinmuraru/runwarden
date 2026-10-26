<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type SignInMethod } from '../api'
import { errorMessage, nextFrom, redirectURL } from './flow'

const methods = ref<SignInMethod[]>([])
const loaded = ref(false)
const selected = ref<SignInMethod | null>(null)
const username = ref('')
const password = ref('')
const error = ref(errorMessage(window.location.search))
const busy = ref(false)
const next = nextFrom(window.location)

const isAdmin = computed(() => selected.value?.id === 'local')

onMounted(async () => {
  try {
    const cfg = await api.authConfig()
    if (cfg.no_auth) {
      window.location.assign(next)
      return
    }
    methods.value = cfg.methods
  } catch {
    error.value = 'Sign-in is unavailable right now. Try again later.'
  } finally {
    loaded.value = true
  }
})

function choose(m: SignInMethod) {
  error.value = ''
  if (m.kind === 'redirect') {
    window.location.assign(redirectURL(m.id, next))
    return
  }
  selected.value = m
}

async function submit() {
  if (!selected.value) return
  busy.value = true
  error.value = ''
  try {
    await api.loginPassword(selected.value.id, isAdmin.value ? '' : username.value, password.value)
    password.value = ''
    window.location.assign(next)
  } catch (e) {
    error.value =
      e instanceof ApiError && (e.status === 401 || e.status === 429) ? e.message : 'Sign-in failed. Try again later.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="container">
    <div class="card stack" style="max-width: 24rem; margin: 3rem auto">
      <h1>Runwarden</h1>
      <p v-if="error" class="alert" role="alert">{{ error }}</p>
      <p v-if="!loaded" class="muted">Loading…</p>

      <template v-else-if="!selected">
        <p v-if="!methods.length" class="alert" role="alert">Sign-in is not available. Check the server configuration.</p>
        <p v-else class="muted">Choose how to sign in:</p>
        <button
          v-for="m in methods"
          :key="m.id"
          class="btn"
          :class="{ 'btn-primary': m.kind === 'redirect' }"
          type="button"
          style="width: 100%"
          @click="choose(m)"
        >
          {{ m.id === 'local' ? m.label : `Sign in with ${m.label}` }}
        </button>
      </template>

      <form v-else class="stack" @submit.prevent="submit">
        <p class="muted">
          {{ isAdmin ? 'Built-in administrator: use the password from the server.' : `Sign in with ${selected.label}` }}
        </p>
        <div v-if="!isAdmin" class="field">
          <label for="username">User name</label>
          <input id="username" v-model="username" type="text" autocomplete="username" required autofocus />
        </div>
        <div class="field">
          <label for="password">Password</label>
          <input id="password" v-model="password" type="password" autocomplete="current-password" required :autofocus="isAdmin" />
        </div>
        <div class="row">
          <button class="btn btn-primary" type="submit" :disabled="busy">Sign in</button>
          <button class="btn btn-quiet" type="button" @click="selected = null">Back</button>
        </div>
      </form>
    </div>
  </main>
</template>
