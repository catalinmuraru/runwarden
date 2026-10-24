<script setup lang="ts">
import { useRouter } from 'vue-router'
import { session, signOut } from '../session'

const router = useRouter()

async function logout() {
  const providerLogout = await signOut()
  if (providerLogout) {
    window.location.assign(providerLogout)
    return
  }
  await router.push({ name: 'login' })
}
</script>

<template>
  <header class="app-header">
    <div class="container header-row">
      <RouterLink to="/" class="brand">Runwarden</RouterLink>
      <nav aria-label="Main">
        <RouterLink to="/" :class="{ active: $route.name === 'scripts' || $route.name === 'script' }">Scripts</RouterLink>
        <RouterLink to="/runs" :class="{ active: $route.name === 'runs' || $route.name === 'run' }">History</RouterLink>
      </nav>
      <div class="user">
        <span :title="session.me?.groups.join(', ')">{{ session.me?.name }}</span>
        <span v-if="session.me?.admin" class="badge tone-info">admin</span>
        <button v-if="session.authType !== 'none'" class="btn btn-quiet" type="button" @click="logout">Sign out</button>
      </div>
    </div>
  </header>
</template>
