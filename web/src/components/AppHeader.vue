<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { notifications, setNotifications, unavailableReason } from '../lib/notify'
import { running } from '../lib/running'
import { session, signOut } from '../session'

const router = useRouter()

const notifyTitle = computed(() => {
  const reason = unavailableReason()
  if (reason) return reason
  return notifications.enabled ? 'Notifications on: you are told when your runs finish' : 'Notify me when my runs finish'
})

async function toggleNotifications() {
  const reason = unavailableReason()
  if (reason) {
    alert(reason)
    return
  }
  await setNotifications(!notifications.enabled)
  if (!notifications.enabled && unavailableReason()) alert(unavailableReason())
}

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
        <RouterLink to="/running" :class="{ active: $route.name === 'running' }">
          Running<span v-if="running.runs.length" class="count" :aria-label="`${running.runs.length} in progress`">{{ running.runs.length }}</span>
        </RouterLink>
        <RouterLink to="/runs" :class="{ active: $route.name === 'runs' || $route.name === 'run' }">History</RouterLink>
        <RouterLink v-if="session.me?.admin" to="/admin" :class="{ active: $route.name === 'admin' }">Admin</RouterLink>
      </nav>
      <div class="user">
        <button
          class="btn btn-quiet"
          type="button"
          :aria-pressed="notifications.enabled"
          :title="notifyTitle"
          @click="toggleNotifications"
        >
          {{ notifications.enabled ? '🔔' : '🔕' }}<span class="sr-only">{{ notifyTitle }}</span>
        </button>
        <span :title="session.me?.groups.join(', ')">{{ session.me?.name }}</span>
        <span v-if="session.me?.admin" class="badge tone-info">admin</span>
        <button v-if="session.authType !== 'none'" class="btn btn-quiet" type="button" @click="logout">Sign out</button>
      </div>
    </div>
  </header>
</template>
