<script setup lang="ts">
import { ref } from 'vue'
import type { RunMeta } from '../api'
import RunPanel from '../components/RunPanel.vue'

defineProps<{ id: string }>()
const script = ref('')

function loaded(meta: RunMeta) {
  script.value = meta.script
  document.title = `${meta.script} · Runwarden`
}
</script>

<template>
  <div class="page-run">
    <p class="muted small"><RouterLink to="/runs">History</RouterLink> / {{ id }}</p>
    <h1 v-if="script">
      <RouterLink :to="{ name: 'script', params: { name: script } }" class="plain">{{ script }}</RouterLink>
    </h1>
    <RunPanel :id="id" details @loaded="loaded" />
  </div>
</template>
