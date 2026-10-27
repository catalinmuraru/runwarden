// The scripts the user may see, loaded once for the sidebar and the start page.
import { reactive } from 'vue'
import { api, type ScriptSummary } from '../api'

export const scripts = reactive({
  list: [] as ScriptSummary[],
  loading: false,
  loaded: false,
  error: '',
})

export async function loadScripts(): Promise<void> {
  scripts.loading = true
  scripts.error = ''
  try {
    scripts.list = await api.scripts()
    scripts.loaded = true
  } catch (e) {
    scripts.error = e instanceof Error ? e.message : String(e)
  } finally {
    scripts.loading = false
  }
}

/** Scripts whose name, group, description or tags contain the query. */
export function filterScripts(list: ScriptSummary[], query: string): ScriptSummary[] {
  const q = query.trim().toLowerCase()
  if (!q) return list
  return list.filter((s) => `${s.name} ${s.group ?? ''} ${s.description ?? ''} ${(s.tags ?? []).join(' ')}`.toLowerCase().includes(q))
}

const openKey = 'runwarden.groups'

/** The sidebar groups the user left open. */
export function savedOpenGroups(): Set<string> {
  try {
    return new Set(JSON.parse(localStorage.getItem(openKey) ?? '[]') as string[])
  } catch {
    return new Set()
  }
}

export function saveOpenGroups(open: Set<string>) {
  try {
    localStorage.setItem(openKey, JSON.stringify([...open]))
  } catch {
    // storage blocked: the groups just don't stay open across reloads
  }
}
