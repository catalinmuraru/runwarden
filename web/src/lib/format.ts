// Display helpers for runs.

export type Tone = 'info' | 'ok' | 'warn' | 'bad'

const labels: Record<string, [string, Tone]> = {
  running: ['Running', 'info'],
  finished: ['Finished', 'ok'],
  failed: ['Failed', 'bad'],
  stopped: ['Stopped', 'warn'],
  killed: ['Killed', 'bad'],
  timed_out: ['Timed out', 'bad'],
  interrupted: ['Interrupted', 'warn'],
}

export function statusLabel(status: string): { text: string; tone: Tone } {
  const [text, tone] = labels[status] ?? [status, 'warn']
  return { text, tone }
}

export function formatDateTime(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString()
}

/** Duration between two times (end defaults to now), e.g. "2h 5m", "1m 3s", "4s". */
export function formatDuration(start: string, end?: string, now: number = Date.now()): string {
  const from = new Date(start).getTime()
  const to = end ? new Date(end).getTime() : now
  if (Number.isNaN(from) || Number.isNaN(to)) return ''
  let s = Math.max(0, Math.round((to - from) / 1000))
  const h = Math.floor(s / 3600)
  s -= h * 3600
  const m = Math.floor(s / 60)
  s -= m * 60
  if (h) return `${h}h ${m}m`
  if (m) return `${m}m ${s}s`
  return `${s}s`
}

/** Groups scripts by their group name; ungrouped scripts come last under "Other". */
export function groupScripts<T extends { name: string; group?: string }>(scripts: T[]): [string, T[]][] {
  const groups = new Map<string, T[]>()
  for (const s of scripts) {
    const g = s.group?.trim() || ''
    groups.set(g, [...(groups.get(g) ?? []), s])
  }
  return [...groups.entries()]
    .sort(([a], [b]) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)))
    .map(([g, list]) => [g || 'Other', list.sort((x, y) => x.name.localeCompare(y.name))])
}

/** A length of time in seconds, e.g. "4h", "1h 30m", "45m", "30s". */
export function formatSeconds(total: number): string {
  if (!Number.isFinite(total) || total <= 0) return ''
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = Math.floor(total % 60)
  return [h && `${h}h`, m && `${m}m`, s && `${s}s`].filter(Boolean).join(' ')
}
