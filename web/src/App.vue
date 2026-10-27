<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ScriptSidebar from './components/ScriptSidebar.vue'
import { startRunning, stopRunning } from './lib/running'
import { loadScripts } from './lib/scripts'
import { session } from './session'

const router = useRouter()
const route = useRoute()
const menuOpen = ref(false) // the sidebar on narrow screens

watch(
  () => session.me?.name,
  (name) => {
    if (!name) {
      stopRunning()
      return
    }
    void loadScripts()
    startRunning(name, {
      open: (id) => void router.push({ name: 'run', params: { id } }),
      current: () => {
        const r = router.currentRoute.value
        if (r.name === 'run') return String(r.params.id)
        if (r.name === 'script' && r.query.run) return String(r.query.run)
        return undefined
      },
    })
  },
  { immediate: true },
)

watch(() => route.fullPath, () => (menuOpen.value = false))
</script>

<template>
  <div v-if="session.me" class="layout" :class="{ 'menu-open': menuOpen }">
    <div class="mobile-bar">
      <button class="btn btn-quiet" type="button" :aria-expanded="menuOpen" aria-controls="sidebar" @click="menuOpen = !menuOpen">
        ☰ <span class="sr-only">Scripts and menu</span>
      </button>
      <RouterLink to="/" class="brand">Runwarden</RouterLink>
    </div>
    <ScriptSidebar id="sidebar" />
    <main class="content">
      <RouterView />
    </main>
  </div>
</template>
