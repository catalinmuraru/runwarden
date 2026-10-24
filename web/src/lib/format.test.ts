import { describe, expect, it } from 'vitest'
import { formatDuration, groupScripts, statusLabel } from './format'

describe('formatDuration', () => {
  const t0 = '2026-10-24T10:00:00Z'
  it('formats spans', () => {
    expect(formatDuration(t0, '2026-10-24T10:00:04Z')).toBe('4s')
    expect(formatDuration(t0, '2026-10-24T10:01:03Z')).toBe('1m 3s')
    expect(formatDuration(t0, '2026-10-24T12:05:00Z')).toBe('2h 5m')
    expect(formatDuration(t0, undefined, Date.parse('2026-10-24T10:00:30Z'))).toBe('30s')
    expect(formatDuration('nope')).toBe('')
  })
})

describe('statusLabel', () => {
  it('labels statuses with a tone', () => {
    expect(statusLabel('timed_out')).toEqual({ text: 'Timed out', tone: 'bad' })
    expect(statusLabel('something_new')).toEqual({ text: 'something_new', tone: 'warn' })
  })
})

describe('groupScripts', () => {
  it('sorts groups and names, ungrouped last', () => {
    const grouped = groupScripts([
      { name: 'b' },
      { name: 'z', group: 'Ops' },
      { name: 'a', group: 'Ops' },
      { name: 'c', group: 'Admin' },
    ])
    expect(grouped.map(([g, list]) => [g, list.map((s) => s.name)])).toEqual([
      ['Admin', ['c']],
      ['Ops', ['a', 'z']],
      ['Other', ['b']],
    ])
  })
})
