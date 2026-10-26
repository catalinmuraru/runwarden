// What the signed-in user may do with a script, from the access the server sends with it.
import type { Option, Param, ScriptDetail } from '../api'
import type { FieldValue } from './form'

/** Marks the values of environment parameters the user may only view. */
export function decorateOptions(p: Param, opts: Option[] | undefined): Option[] | undefined {
  if (!opts || p.environment !== 'target') return opts
  return opts.map((o) => (o.access === 'view' ? { ...o, label: `${o.label} (view only)` } : o))
}

function items(v: FieldValue | undefined): string[] {
  if (Array.isArray(v)) return v
  return typeof v === 'string' && v !== '' ? [v] : []
}

/**
 * Why the user cannot start the script with these values, or '' if nothing stops them (the server
 * checks again). options are the current values offered per parameter.
 */
export function startBlocker(s: ScriptDetail, values: Record<string, FieldValue>, options: Record<string, Option[] | undefined>): string {
  const envParams = s.parameters.filter((p) => p.environment)
  if (envParams.length === 0) {
    return s.access['-'] === 'run' ? '' : 'You have view-only access to this script.'
  }
  for (const p of envParams) {
    for (const value of items(values[p.name])) {
      const o = options[p.name]?.find((x) => x.value === value)
      if (!o?.environment) continue // free text or not loaded yet: the server decides
      if (p.environment === 'target' && o.access !== 'run') {
        return `You have view-only access to ${o.environment}. Choose another ${p.name} to run it.`
      }
    }
  }
  return ''
}

/** Environments grouped by access, for a short summary on the script page. */
export function accessSummary(s: ScriptDetail): { run: string[]; view: string[] } {
  const run: string[] = []
  const view: string[] = []
  for (const [env, a] of Object.entries(s.access)) {
    if (env === '-') continue
    ;(a === 'run' ? run : view).push(env)
  }
  return { run, view }
}
