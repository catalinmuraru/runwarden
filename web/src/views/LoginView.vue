<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiError } from '../api'
import { redirectToProvider, session } from '../session'

const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

function next(): string {
  const n = route.query.next
  return typeof n === 'string' && n.startsWith('/') && !n.startsWith('//') ? n : '/'
}

onMounted(() => {
  if (session.me) {
    void router.replace(next())
  } else if (session.authType === 'oidc' || session.authType === 'gitlab') {
    redirectToProvider(next())
  }
})

async function submit() {
  busy.value = true
  error.value = ''
  try {
    session.me = await api.loginPassword(username.value, password.value)
    password.value = ''
    await router.replace(next())
  } catch (e) {
    error.value =
      e instanceof ApiError && (e.status === 401 || e.status === 429) ? e.message : 'Sign-in failed. Try again later.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="card" style="max-width: 24rem; margin: 3rem auto">
    <h1>Runwarden</h1>
    <form v-if="session.authType === 'ldap'" class="stack" @submit.prevent="submit">
      <div class="field">
        <label for="username">User name</label>
        <input id="username" v-model="username" type="text" autocomplete="username" required autofocus />
      </div>
      <div class="field">
        <label for="password">Password</label>
        <input id="password" v-model="password" type="password" autocomplete="current-password" required />
      </div>
      <p v-if="error" class="alert" role="alert">{{ error }}</p>
      <button class="btn btn-primary" type="submit" :disabled="busy">Sign in</button>
    </form>
    <p v-else-if="session.authType === 'oidc' || session.authType === 'gitlab'" class="muted">Redirecting to sign-in…</p>
    <p v-else class="alert" role="alert">Sign-in is not available. Check the server configuration.</p>
  </div>
</template>
