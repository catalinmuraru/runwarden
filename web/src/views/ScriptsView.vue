<script setup lang="ts">
import { scripts } from '../lib/scripts'
import { session } from '../session'
</script>

<template>
  <div class="empty-state">
    <div v-if="scripts.loaded && !scripts.list.length && !session.me?.roles.length" class="card stack">
      <strong>You have no access yet.</strong>
      <span class="muted">You are signed in as {{ session.me?.name }}. Ask an administrator to give you a role.</span>
    </div>
    <p v-else-if="scripts.loaded && !scripts.list.length" class="muted">No scripts are available to you.</p>
    <p v-else class="muted">Choose a script from the list.</p>
  </div>
</template>
