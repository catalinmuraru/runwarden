<script setup lang="ts">
import { watch } from 'vue'
import { useRouter } from 'vue-router'
import AppHeader from './components/AppHeader.vue'
import { startRunning, stopRunning } from './lib/running'
import { session } from './session'

const router = useRouter()

watch(
  () => session.me?.name,
  (name) => {
    if (!name) {
      stopRunning()
      return
    }
    startRunning(name, {
      open: (id) => void router.push({ name: 'run', params: { id } }),
      current: () => {
        const r = router.currentRoute.value
        return r.name === 'run' ? String(r.params.id) : undefined
      },
    })
  },
  { immediate: true },
)
</script>

<template>
  <AppHeader v-if="session.me" />
  <main class="container">
    <RouterView />
  </main>
</template>
