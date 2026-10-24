<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type FileEntry, type Param } from '../api'

const props = defineProps<{ param: Param; script: string; id: string; modelValue: string | string[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: string | string[]] }>()

const path = ref<string[]>([])
const entries = ref<FileEntry[]>([])
const loading = ref(false)
const error = ref('')
const pickDirs = computed(() => props.param.file_type === 'dir')
const recursive = computed(() => !!props.param.file_recursive)

const selected = computed(() =>
  Array.isArray(props.modelValue) ? props.modelValue.join('/') : props.modelValue,
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    entries.value = await api.files(props.script, props.param.name, path.value)
  } catch (e) {
    entries.value = []
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function choose(segments: string[]) {
  emit('update:modelValue', recursive.value ? segments : segments[segments.length - 1] ?? '')
}

function open(entry: FileEntry) {
  if (entry.dir && recursive.value) {
    path.value = [...path.value, entry.name]
    void load()
  } else {
    choose([...path.value, entry.name])
  }
}

function goTo(depth: number) {
  path.value = path.value.slice(0, depth)
  void load()
}

onMounted(load)
</script>

<template>
  <div :id="id" class="stack" style="--gap: 0.5rem">
    <div class="row">
      <span>Selected: <code>{{ selected || 'nothing' }}</code></span>
      <button v-if="selected" class="btn btn-quiet" type="button" @click="emit('update:modelValue', recursive ? [] : '')">
        Clear
      </button>
    </div>
    <nav v-if="recursive" class="crumbs" aria-label="Folder">
      <button type="button" @click="goTo(0)">{{ param.name }}</button>
      <template v-for="(seg, i) in path" :key="i">
        <span aria-hidden="true">/</span>
        <button type="button" @click="goTo(i + 1)">{{ seg }}</button>
      </template>
      <button
        v-if="pickDirs && path.length"
        class="btn btn-quiet"
        type="button"
        @click="choose(path)"
      >
        Select this folder
      </button>
    </nav>
    <div class="browser" role="listbox" :aria-label="`${param.name} files`">
      <p v-if="loading" class="muted" style="padding: 0.3rem 0.6rem">Loading…</p>
      <p v-else-if="error" class="error" style="padding: 0.3rem 0.6rem">{{ error }}</p>
      <p v-else-if="entries.length === 0" class="muted" style="padding: 0.3rem 0.6rem">
        {{ pickDirs ? 'No subfolders' : 'No matching files' }}
      </p>
      <button
        v-for="e in entries"
        :key="e.name"
        type="button"
        role="option"
        :aria-pressed="selected === [...path, e.name].join('/') || selected === e.name"
        @click="open(e)"
      >
        {{ e.dir ? '📁' : '📄' }} {{ e.name }}{{ e.dir && recursive ? '/' : '' }}
      </button>
    </div>
  </div>
</template>
