<script setup lang="ts">
// Picks several values (scripts, tags, environments) with checkboxes; "all" clears the others.
import { computed } from 'vue'
import { ANY } from '../../lib/policy'

const props = defineProps<{
  legend: string
  modelValue: string[]
  groups: { title: string; options: { value: string; label: string }[] }[]
  allLabel: string
}>()
const emit = defineEmits<{ 'update:modelValue': [string[]] }>()

const all = computed(() => props.modelValue.includes(ANY))
const summary = computed(() => {
  if (all.value) return props.allLabel
  if (!props.modelValue.length) return 'nothing chosen'
  const labels = props.groups.flatMap((g) => g.options).filter((o) => props.modelValue.includes(o.value))
  return labels.map((o) => o.label).join(', ') || props.modelValue.join(', ')
})

function toggle(value: string, on: boolean) {
  if (value === ANY) {
    emit('update:modelValue', on ? [ANY] : [])
    return
  }
  const rest = props.modelValue.filter((v) => v !== ANY && v !== value)
  emit('update:modelValue', on ? [...rest, value] : rest)
}
</script>

<template>
  <fieldset class="field">
    <legend>{{ legend }}</legend>
    <details class="picker">
      <summary>{{ summary }}</summary>
      <div class="stack" style="padding: 0.5rem 0">
        <label class="row"><input type="checkbox" :checked="all" @change="toggle(ANY, ($event.target as HTMLInputElement).checked)" /> {{ allLabel }}</label>
        <div v-for="g in groups" v-show="g.options.length" :key="g.title">
          <div class="muted small">{{ g.title }}</div>
          <div class="checks">
            <label v-for="o in g.options" :key="o.value">
              <input
                type="checkbox"
                :checked="modelValue.includes(o.value)"
                :disabled="all"
                @change="toggle(o.value, ($event.target as HTMLInputElement).checked)"
              />
              {{ o.label }}
            </label>
          </div>
        </div>
      </div>
    </details>
  </fieldset>
</template>
