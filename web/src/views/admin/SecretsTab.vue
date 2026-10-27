<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type SecretsOverview } from '../../api'
import { executorSummary, gapCount, isolationSummary, kubeGapCount, secretCell, secretColumns } from '../../lib/secrets'

const data = ref<SecretsOverview | null>(null)
const error = ref('')

const columns = computed(() => (data.value ? secretColumns(data.value) : []))
const rows = computed(() => (data.value?.scripts ?? []).map((s) => ({ ...s, cells: columns.value.map((c) => ({ column: c, ...secretCell(s, c) })) })))
const isolation = computed(() => (data.value ? isolationSummary(data.value.isolation) : null))
const dirs = computed(() => Object.entries(data.value?.available ?? {}).sort(([a], [b]) => a.localeCompare(b)))

async function load() {
  error.value = ''
  try {
    data.value = await api.adminSecrets()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(load)
defineExpose({ load })
</script>

<template>
  <p class="muted">
    Secrets come from one Kubernetes Secret per environment (plus <code>common</code>), mounted read-only. A run receives only the
    secrets its runner config declares (<code>"secrets"</code>), from the environment it runs in, and they are masked in its output.
    Values are never shown here; change them in Kubernetes or your secret manager.
  </p>
  <p v-if="error" class="alert" role="alert">{{ error }}</p>
  <p v-else-if="!data" class="muted">Loading…</p>
  <template v-else>
    <h2>Isolation between runs</h2>
    <p class="muted">
      Each run is sandboxed: it cannot read other runs' files or environment, other users' workspaces, Runwarden's data or the
      mounted secrets. Set with <code>ISOLATION</code>.
    </p>
    <p v-if="isolation" :class="['badge', `tone-${isolation.tone}`]" data-testid="isolation">{{ isolation.text }}</p>

    <template v-if="data.executors.length">
      <h2>Executors</h2>
      <p class="muted">
        These environments run on their own executors (<code>EXECUTORS</code>), which hold their secrets and kubeconfig; this server
        never sees the values. Their secrets below are the names the executors report.
      </p>
      <div class="table-wrap">
        <table data-testid="executors">
          <thead>
            <tr>
              <th scope="col">Environment</th>
              <th scope="col">State</th>
              <th scope="col">Pods</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="x in data.executors" :key="x.environment">
              <th scope="row">{{ x.environment }}</th>
              <td>
                <span :class="['badge', `tone-${executorSummary(x).tone}`]">{{ executorSummary(x).text }}</span>
              </td>
              <td>
                <div v-for="p in x.pods" :key="p.pod" class="small">
                  {{ p.pod }}
                  <span class="muted">
                    · {{ p.version }} · {{ p.busy }}/{{ p.capacity }} busy ·
                    {{ p.isolation.active ? `Landlock ABI ${p.isolation.abi}` : 'not sandboxed' }} · since
                    {{ new Date(p.connected).toLocaleString() }}
                  </span>
                </div>
                <span v-if="!x.pods.length" class="muted">–</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <p v-if="!data.configured" class="alert">No secrets directory is configured (<code>SECRETS_DIR</code>).</p>
    <p v-if="data.unknown_dirs.length" class="alert" role="alert">
      Secrets for {{ data.unknown_dirs.join(', ') }}: no environment has that name, so no run receives them. Rename the environment or
      the mounted Secret (Helm <code>secrets.environments</code>).
    </p>

    <h2>Scripts</h2>
    <p v-if="!data.scripts.length" class="muted">No runner config declares secrets.</p>
    <template v-else>
      <p v-if="gapCount(data)" class="tone-warn">
        {{ gapCount(data) }} script/environment combination(s) cannot start: a declared secret is missing there.
      </p>
      <div class="table-wrap">
        <table class="matrix">
          <thead>
            <tr>
              <th scope="col">Script</th>
              <th v-for="c in columns" :key="c" scope="col">{{ c === '-' ? 'no environment' : c }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in rows" :key="s.name">
              <th scope="row">
                {{ s.name }}
                <div class="muted small">{{ s.secrets.join(', ') }}</div>
              </th>
              <td v-for="cell in s.cells" :key="cell.column">
                <span v-if="cell.state === 'ok'" class="badge tone-ok">✓</span>
                <span v-else-if="cell.state === 'missing'" class="badge tone-bad">missing {{ cell.names.join(', ') }}</span>
                <span v-else class="muted">–</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <h2>Available secrets</h2>
    <p v-if="!dirs.length" class="muted">None mounted.</p>
    <dl v-else class="meta card">
      <template v-for="[dir, names] in dirs" :key="dir">
        <dt>{{ dir }}</dt>
        <dd>{{ names.length ? names.join(', ') : '(empty)' }}</dd>
      </template>
    </dl>

    <h2>Kubernetes</h2>
    <p class="muted">
      Runners with <code>"kubernetes": true</code> get the kubeconfig of the environment they run in (one Kubernetes Secret per
      environment). Only the cluster server is shown here, never credentials.
    </p>
    <p v-if="!data.kubernetes.configured" class="alert">No kubeconfig directory is configured (<code>KUBE_DIR</code>).</p>
    <p v-if="data.kubernetes.unknown_dirs.length" class="alert" role="alert">
      Kubeconfig for {{ data.kubernetes.unknown_dirs.join(', ') }}: no environment has that name, so no run receives it. Rename the
      environment or the mounted Secret (Helm <code>kube.environments</code>).
    </p>
    <p v-if="!data.kubernetes.clusters.length" class="muted">No kubeconfig mounted.</p>
    <div v-else class="table-wrap">
      <table>
        <thead>
          <tr>
            <th scope="col">Environment</th>
            <th scope="col">Cluster server</th>
            <th scope="col">Namespace</th>
            <th scope="col">Context</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in data.kubernetes.clusters" :key="c.environment">
            <th scope="row">{{ c.environment }}</th>
            <td v-if="c.error" colspan="3"><span class="badge tone-bad">unusable</span> {{ c.error }}</td>
            <template v-else>
              <td><code>{{ c.server }}</code></td>
              <td>{{ c.namespace || '–' }}</td>
              <td class="muted">{{ c.context }}</td>
            </template>
          </tr>
        </tbody>
      </table>
    </div>
    <template v-if="data.kubernetes.scripts.length">
      <p v-if="kubeGapCount(data)" class="tone-warn">
        {{ kubeGapCount(data) }} script/environment combination(s) cannot start: no usable kubeconfig there.
      </p>
      <dl class="meta card">
        <template v-for="s in data.kubernetes.scripts" :key="s.name">
          <dt>{{ s.name }}</dt>
          <dd>
            <span v-if="!s.missing.length" class="badge tone-ok">✓ all environments</span>
            <span v-else class="badge tone-bad">missing {{ s.missing.join(', ') }}</span>
          </dd>
        </template>
      </dl>
    </template>
  </template>
</template>
