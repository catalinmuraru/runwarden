<script setup lang="ts">
import { computed } from 'vue'
import type { Option, Param } from '../api'
import type { FieldValue } from '../lib/form'
import { renderMarkdown } from '../lib/markdown'
import ServerFileField from './ServerFileField.vue'

const props = defineProps<{
  param: Param
  script: string
  modelValue: FieldValue
  options?: Option[]
  optionsHint?: string
  error?: string
}>()
const emit = defineEmits<{ 'update:modelValue': [value: FieldValue] }>()

const id = computed(() => `param-${props.param.name.replace(/[^\w-]/g, '_')}`)
const hintId = computed(() => `${id.value}-hint`)
const value = computed({
  get: () => props.modelValue,
  set: (v: FieldValue) => emit('update:modelValue', v),
})
const text = computed({
  get: () => (typeof props.modelValue === 'string' ? props.modelValue : ''),
  set: (v: string) => emit('update:modelValue', v),
})
const list = computed(() => (Array.isArray(props.modelValue) ? props.modelValue : []))
const choices = computed<Option[]>(() => {
  const opts = props.options ?? []
  // Keep a prefilled value visible even if it isn't (yet) among the loaded options.
  const current = typeof props.modelValue === 'string' ? props.modelValue : ''
  return current && !opts.some((o) => o.value === current) ? [{ value: current, label: current }, ...opts] : opts
})
const wide = computed(() => ['multiline_text', 'multiselect', 'server_file'].includes(props.param.type))
const kind = computed(() => {
  const p = props.param
  if (p.constant) return 'constant'
  if (p.type === 'text' || p.type === 'ip' || p.type === 'ip4' || p.type === 'ip6') return p.secure ? 'password' : 'text'
  return p.type
})

const hints = computed(() => {
  const p = props.param
  const out: string[] = []
  if (p.min !== undefined || p.max !== undefined) out.push(`Range ${p.min ?? '…'}–${p.max ?? '…'}`)
  if (p.max_length) out.push(`At most ${p.max_length} characters`)
  if (p.regex_description) out.push(`Format: ${p.regex_description}`)
  if (p.file_extensions?.length) out.push(`Allowed: ${p.file_extensions.join(', ')}`)
  if (p.type === 'ip4') out.push('IPv4 address')
  if (p.type === 'ip6') out.push('IPv6 address')
  if (p.secure && p.has_default) out.push('Leave empty to use the configured default')
  if (props.optionsHint) out.push(props.optionsHint)
  return out.join(' · ')
})

function toggle(option: string, checked: boolean) {
  const next = list.value.filter((v) => v !== option)
  if (checked) next.push(option)
  emit('update:modelValue', next)
}

function pickFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  emit('update:modelValue', input.files?.[0] ?? null)
}
</script>

<template>
  <div class="field" :class="{ wide }">
    <template v-if="kind === 'flag'">
      <label class="row" style="font-weight: 600">
        <input :id="id" v-model="value" type="checkbox" :aria-describedby="hintId" />
        {{ param.name }}
      </label>
    </template>
    <template v-else>
      <label v-if="kind !== 'multiselect'" :for="id">
        {{ param.name }}<span v-if="param.required" class="required" aria-hidden="true"> *</span>
      </label>
      <span v-else :id="`${id}-label`" style="font-weight: 600">
        {{ param.name }}<span v-if="param.required" class="required" aria-hidden="true"> *</span>
      </span>

      <input v-if="kind === 'constant'" :id="id" :value="String(param.default ?? '')" type="text" readonly />

      <input
        v-else-if="kind === 'text' || kind === 'password'"
        :id="id"
        v-model="text"
        :type="kind"
        :autocomplete="kind === 'password' ? 'off' : undefined"
        :maxlength="param.max_length"
        :required="param.required"
        :aria-invalid="!!error"
        :aria-describedby="hintId"
      />

      <input
        v-else-if="kind === 'int'"
        :id="id"
        v-model="text"
        type="number"
        step="1"
        :min="param.min"
        :max="param.max"
        :required="param.required"
        :aria-invalid="!!error"
        :aria-describedby="hintId"
      />

      <textarea
        v-else-if="kind === 'multiline_text'"
        :id="id"
        v-model="text"
        :required="param.required"
        :aria-invalid="!!error"
        :aria-describedby="hintId"
      />

      <select
        v-else-if="kind === 'list'"
        :id="id"
        v-model="text"
        :required="param.required"
        :aria-invalid="!!error"
        :aria-describedby="hintId"
      >
        <option value="">{{ param.required ? '— choose —' : '— none —' }}</option>
        <option v-for="o in choices" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select>

      <template v-else-if="kind === 'editable_list'">
        <input
          :id="id"
          v-model="text"
          type="text"
          :list="`${id}-options`"
          :required="param.required"
          :aria-invalid="!!error"
          :aria-describedby="hintId"
        />
        <datalist :id="`${id}-options`">
          <option v-for="o in choices" :key="o.value" :value="o.value">{{ o.label }}</option>
        </datalist>
      </template>

      <fieldset v-else-if="kind === 'multiselect'" :aria-labelledby="`${id}-label`" :aria-describedby="hintId">
        <div class="checks">
          <label v-for="o in options ?? []" :key="o.value">
            <input
              type="checkbox"
              :checked="list.includes(o.value)"
              @change="toggle(o.value, ($event.target as HTMLInputElement).checked)"
            />
            {{ o.label }}
          </label>
          <span v-if="!(options ?? []).length" class="muted">No options</span>
        </div>
      </fieldset>

      <input
        v-else-if="kind === 'file_upload'"
        :id="id"
        type="file"
        :accept="param.file_extensions?.map((e) => (e.startsWith('.') ? e : `.${e}`)).join(',')"
        :required="param.required"
        :aria-invalid="!!error"
        :aria-describedby="hintId"
        @change="pickFile"
      />

      <ServerFileField
        v-else-if="kind === 'server_file'"
        :id="id"
        :model-value="value as string | string[]"
        :param="param"
        :script="script"
        @update:model-value="value = $event"
      />

      <input v-else :id="id" v-model="text" type="text" :aria-invalid="!!error" :aria-describedby="hintId" />
    </template>

    <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes everything first -->
    <span v-if="param.description" class="hint description" v-html="renderMarkdown(param.description)" />
    <span :id="hintId" class="hint">{{ hints }}</span>
    <span v-if="error" class="error" role="alert">{{ error }}</span>
  </div>
</template>
