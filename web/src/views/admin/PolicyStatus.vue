<script setup lang="ts">
import { loadPolicy, policyState } from '../../lib/policyState'

defineProps<{ warnings?: boolean }>()
</script>

<template>
  <div v-if="policyState.conflict" class="alert" role="alert">
    Someone else changed the access policy meanwhile. Your change was not saved.
    <button class="btn small" type="button" @click="loadPolicy">Reload</button>
  </div>
  <div v-else-if="policyState.error" class="alert" role="alert">
    {{ policyState.error }}
    <ul v-if="policyState.details.length">
      <li v-for="(d, i) in policyState.details" :key="i">{{ d }}</li>
    </ul>
  </div>
  <details v-if="warnings && policyState.view?.warnings.length" class="card small" style="margin-bottom: 1rem">
    <summary class="tone-warn">{{ policyState.view.warnings.length }} selector(s) match no script</summary>
    <ul>
      <li v-for="(w, i) in policyState.view.warnings" :key="i">{{ w }}</li>
    </ul>
  </details>
</template>
