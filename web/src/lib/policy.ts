// Helpers for the access policy pages in the Admin area.
import type { Access, Policy } from '../api'

export const ANY = '*'
export const NO_ENVIRONMENT = '-'
export const TAG = 'tag:'
export const BUILT_IN_ROLES = ['guest', 'developer', 'consultant', 'devops']

/** Roles that can be given to users (guest means "no role"). */
export function assignableRoles(p: Policy): string[] {
  return p.roles.map((r) => r.name).filter((r) => r !== 'guest')
}

/** Roles that can carry rules (devops has every right, guest none). */
export function ruleRoles(p: Policy): string[] {
  return p.roles.map((r) => r.name).filter((r) => r !== 'guest' && r !== 'devops')
}

/** How a script selector reads in a table. */
export function scriptLabel(sel: string): string {
  if (sel === ANY) return 'all scripts'
  if (sel.startsWith(TAG)) return `tag ${sel.slice(TAG.length)}`
  return sel
}

/** How an environment selector reads in a table. */
export function environmentLabel(sel: string): string {
  if (sel === ANY) return 'all environments'
  if (sel === NO_ENVIRONMENT) return 'scripts without environment'
  return sel
}

/** Splits a comma-separated list, dropping empty items. */
export function parseList(text: string): string[] {
  return text
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

export interface TargetDraft {
  scripts: string[]
  environments: string[]
  access: Access | ''
}

function targetProblems(d: TargetDraft): string[] {
  const out: string[] = []
  if (!d.scripts.length) out.push('Choose at least one script, tag or all scripts.')
  if (!d.environments.length) out.push('Choose at least one environment.')
  if (d.access !== 'view' && d.access !== 'run') out.push('Choose view or run.')
  return out
}

/** Problems of a rule before it is sent (the server checks again). */
export function ruleProblems(d: TargetDraft & { role: string }): string[] {
  return [...(d.role ? [] : ['Choose a role.']), ...targetProblems(d)]
}

export interface ExceptionDraft extends TargetDraft {
  user: string
  reason: string
  expires: string // yyyy-mm-dd, the end of that day (local time)
}

/** The moment an exception ending on a date (yyyy-mm-dd) expires: the end of that day. */
export function endOfDay(date: string): Date {
  const [y, m, d] = date.split('-').map(Number)
  return new Date(y, m - 1, d, 23, 59, 59)
}

/**
 * The last allowed expiry date (yyyy-mm-dd) for an exception created now: its end of day must
 * still be within maxDays.
 */
export function lastExpiryDate(now: Date, maxDays: number): string {
  const limit = now.getTime() + maxDays * 24 * 3600 * 1000
  const d = new Date(limit)
  if (endOfDay(toDateInput(d)).getTime() > limit) d.setDate(d.getDate() - 1)
  return toDateInput(d)
}

export function toDateInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** Problems of an exception before it is sent. */
export function exceptionProblems(d: ExceptionDraft, now: Date, maxDays: number): string[] {
  const out: string[] = []
  if (!d.user.trim()) out.push('Choose a user.')
  if (!d.reason.trim()) out.push('Give a reason (e.g. the ticket or incident).')
  if (!/^\d{4}-\d{2}-\d{2}$/.test(d.expires)) {
    out.push('Choose when it ends.')
  } else {
    const end = endOfDay(d.expires)
    if (end <= now) out.push('The end must be in the future.')
    else if (maxDays > 0 && end.getTime() - now.getTime() > maxDays * 24 * 3600 * 1000) out.push(`An exception may last at most ${maxDays} days.`)
  }
  return [...out, ...targetProblems(d)]
}

/** Converts a glob pattern (*, ?, [...]) to a regular expression, as the server matches values. */
export function globToRegExp(pattern: string): RegExp {
  let re = ''
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]
    if (c === '*') re += '.*'
    else if (c === '?') re += '.'
    else if (c === '[') {
      const end = pattern.indexOf(']', i + 1)
      if (end < 0) {
        re += '\\['
        continue
      }
      let cls = pattern.slice(i + 1, end)
      if (cls.startsWith('^')) cls = '\\' + cls
      re += `[${cls.replace(/\\/g, '\\\\')}]`
      i = end
    } else re += c.replace(/[.+^${}()|\\]/g, '\\$&')
  }
  return new RegExp(`^${re}$`)
}

/** The environment a parameter value belongs to, as the server decides it. */
export function environmentOf(p: Pick<Policy, 'environments' | 'unknown_environment'>, value: string): string {
  const v = value.trim().toLowerCase()
  if (!v) return ''
  for (const e of p.environments) {
    if (e.name.toLowerCase() === v) return e.name
    if ((e.match ?? []).some((m) => globToRegExp(m.toLowerCase()).test(v))) return e.name
  }
  if (p.unknown_environment) return p.unknown_environment
  return p.environments.at(-1)?.name ?? NO_ENVIRONMENT
}

/** Whether an exception still grants access. */
export function isActive(expires: string, now: Date): boolean {
  return new Date(expires) > now
}
