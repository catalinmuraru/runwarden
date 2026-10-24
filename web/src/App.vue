<script setup lang="ts">
import AppHeader from './components/AppHeader.vue'
import { session } from './session'
</script>

<template>
  <AppHeader v-if="session.me" />
  <main class="container">
    <RouterView />
  </main>
</template>
