<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiError, type ImportSummary } from '../api'
import { changePolicy, loadPolicy, policyState } from '../lib/policyState'
import AccessTab from './admin/AccessTab.vue'
import ActivityTab from './admin/ActivityTab.vue'
import EnvironmentsTab from './admin/EnvironmentsTab.vue'
import ExceptionsTab from './admin/ExceptionsTab.vue'
import RolesTab from './admin/RolesTab.vue'
import RulesTab from './admin/RulesTab.vue'
import SecretsTab from './admin/SecretsTab.vue'
import UsersTab from './admin/UsersTab.vue'

const route = useRoute()
const router = useRouter()

const tabs = [
  { id: 'users', label: 'Users' },
  { id: 'roles', label: 'Roles' },
  { id: 'rules', label: 'Rules' },
  { id: 'exceptions', label: 'Exceptions' },
  { id: 'environments', label: 'Environments' },
  { id: 'access', label: 'Access' },
  { id: 'secrets', label: 'Secrets' },
  { id: 'activity', label: 'Activity' },
] as const
type Tab = (typeof tabs)[number]['id']

const q = (key: string) => (typeof route.query[key] === 'string' ? (route.query[key] as string) : undefined)
const tab = computed<Tab>(() => tabs.find((t) => t.id === q('tab'))?.id ?? 'users')

const users = ref<InstanceType<typeof UsersTab>>()
const activity = ref<InstanceType<typeof ActivityTab>>()
const importFile = ref<HTMLInputElement>()
const pending = ref<{ yaml: string; summary: ImportSummary; warnings: string[] } | null>(null)
const importError = ref('')

function show(t: Tab, extra: Record<string, string> = {}) {
  void router.replace({ query: t === 'users' ? extra : { tab: t, ...extra } })
}

async function refresh() {
  await loadPolicy()
  await users.value?.load()
  await activity.value?.load()
}

async function chooseImport(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  importError.value = ''
  try {
    const yaml = await file.text()
    pending.value = { yaml, ...(await api.previewImport(yaml)) }
  } catch (err) {
    importError.value = err instanceof ApiError ? [err.message, ...err.details].join(': ') : String(err)
  } finally {
    if (importFile.value) importFile.value.value = ''
  }
}

async function confirmImport() {
  if (!pending.value) return
  const yaml = pending.value.yaml
  if (await changePolicy((rev) => api.importPolicy(rev, yaml))) pending.value = null
}

onMounted(loadPolicy)
</script>

<template>
  <div class="row">
    <h1>Admin</h1>
    <span class="spacer" />
    <a class="btn" :href="api.exportPolicyURL" download title="Download the access policy (environments, roles, users, rules, exceptions) as YAML">Export policy</a>
    <button class="btn" type="button" title="Replace the whole access policy with an exported file" @click="importFile?.click()">Import policy</button>
    <input ref="importFile" type="file" accept=".yaml,.yml,application/yaml,text/yaml" hidden @change="chooseImport" />
    <button class="btn" type="button" @click="refresh">Refresh</button>
  </div>

  <p v-if="importError" class="alert" role="alert">Cannot import: {{ importError }}</p>
  <div v-if="pending" class="card stack" role="dialog" aria-label="Import the access policy">
    <strong>Replace the whole access policy with the file?</strong>
    <p>
      It has {{ pending.summary.environments }} environments, {{ pending.summary.roles }} roles, {{ pending.summary.users }} users with
      roles, {{ pending.summary.rules }} rules and {{ pending.summary.exceptions }} exceptions.
      <template v-if="pending.summary.removed_rules || pending.summary.removed_exceptions || pending.summary.removed_users">
        <br /><span class="tone-warn">
          It removes {{ pending.summary.removed_rules }} rules, {{ pending.summary.removed_exceptions }} exceptions and the roles of
          {{ pending.summary.removed_users }} users that exist now.
        </span>
      </template>
    </p>
    <ul v-if="pending.warnings.length" class="small tone-warn">
      <li v-for="(w, i) in pending.warnings" :key="i">{{ w }}</li>
    </ul>
    <div class="row">
      <button class="btn btn-danger" type="button" :disabled="policyState.saving" @click="confirmImport">Replace</button>
      <button class="btn btn-quiet" type="button" @click="pending = null">Cancel</button>
    </div>
  </div>

  <div class="tabs" role="tablist">
    <button
      v-for="t in tabs"
      :key="t.id"
      role="tab"
      type="button"
      :aria-selected="tab === t.id"
      :class="{ active: tab === t.id }"
      @click="show(t.id)"
    >
      {{ t.label }}
    </button>
  </div>

  <p v-if="!policyState.view && policyState.error && tab !== 'activity'" class="alert" role="alert">{{ policyState.error }}</p>
  <UsersTab v-if="tab === 'users'" ref="users" @activity="(u) => show('activity', { user: u })" @access="(u) => show('access', { user: u })" />
  <template v-else-if="tab === 'activity'">
    <ActivityTab ref="activity" />
  </template>
  <p v-else-if="!policyState.view" class="muted">Loading…</p>
  <RolesTab v-else-if="tab === 'roles'" @access="(r) => show('access', { role: r })" @rules="(r) => show('rules', { role: r })" />
  <RulesTab v-else-if="tab === 'rules'" :role="q('role')" />
  <ExceptionsTab v-else-if="tab === 'exceptions'" :user="q('user')" />
  <EnvironmentsTab v-else-if="tab === 'environments'" />
  <SecretsTab v-else-if="tab === 'secrets'" />
  <AccessTab v-else-if="tab === 'access'" :user="q('user')" :role="q('role')" @select="(w) => show('access', w as Record<string, string>)" />
</template>
