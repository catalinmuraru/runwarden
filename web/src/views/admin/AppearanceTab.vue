<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type Theme } from '../../api'
import { applyTheme } from '../../lib/theme'

const choices: { value: Theme; label: string; hint: string }[] = [
  { value: 'system', label: 'System', hint: "Follows each user's operating system setting" },
  { value: 'light', label: 'Light', hint: 'Light colours for everyone' },
  { value: 'dark', label: 'Dark', hint: 'Dark colours for everyone' },
]

const saved = ref<Theme | null>(null)
const theme = ref<Theme>('system')
const saving = ref(false)
const note = ref('')
const error = ref('')

async function load() {
  error.value = ''
  try {
    saved.value = (await api.getUiSettings()).theme
    theme.value = saved.value
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function save() {
  saving.value = true
  error.value = ''
  note.value = ''
  try {
    saved.value = (await api.putUiSettings({ theme: theme.value })).theme
    applyTheme(saved.value)
    note.value = 'Saved. Other users see the new theme the next time they load a page.'
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
defineExpose({ load })
</script>

<template>
  <p class="muted">The colour theme of the whole application, the sign-in page included, for every user.</p>
  <p v-if="error" class="alert" role="alert">{{ error }}</p>
  <p v-if="saved === null && !error" class="muted">Loading…</p>
  <form v-else-if="saved !== null" class="card stack" @submit.prevent="save">
    <fieldset class="field theme-choices">
      <legend>Theme</legend>
      <label v-for="c in choices" :key="c.value" class="theme-choice">
        <input v-model="theme" type="radio" name="theme" :value="c.value" @change="note = ''" />
        <span :class="['swatch', `swatch-${c.value}`]" aria-hidden="true"><span /></span>
        <span>
          <strong>{{ c.label }}</strong>
          <span class="hint">{{ c.hint }}</span>
        </span>
      </label>
    </fieldset>
    <div class="row">
      <button class="btn btn-primary" type="submit" :disabled="saving || theme === saved">Save</button>
      <span v-if="note" class="small tone-ok" role="status">{{ note }}</span>
    </div>
  </form>
</template>

<style scoped>
.theme-choices { border: 0; padding: 0; margin: 0; gap: 0.5rem; }
.theme-choice { display: flex; gap: 0.75rem; align-items: center; font-weight: normal; cursor: pointer; }
.theme-choice .hint { display: block; }
/* Fixed colours on purpose: each swatch previews its theme whatever the current one is. */
.swatch {
  display: inline-flex; align-items: flex-end; width: 3rem; height: 2rem; padding: 0.3rem;
  border-radius: var(--radius); border: 1px solid var(--border); overflow: hidden;
}
.swatch > span { display: block; width: 60%; height: 0.4rem; border-radius: 2px; background: #2f5fd0; }
.swatch-light { background: #f6f7f9; }
.swatch-dark { background: #14171d; }
.swatch-dark > span { background: #6e95f2; }
.swatch-system { background: linear-gradient(135deg, #f6f7f9 50%, #14171d 50%); }
</style>
