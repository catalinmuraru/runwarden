import { describe, expect, it } from 'vitest'
import type { RunMeta } from '../api'
import { endedRuns, runEndedMessage } from './notify'

const run = (id: string, extra: Partial<RunMeta> = {}): RunMeta => ({
  id,
  script: 'Backup',
  user: 'alice',
  args: [],
  status: 'running',
  started: '2026-10-26T10:00:00Z',
  ...extra,
})

describe('endedRuns', () => {
  it('reports runs that left the running list, once', () => {
    const before = new Set(['a', 'b'])
    expect(endedRuns(before, [run('b'), run('c')])).toEqual(['a'])
    expect(endedRuns(new Set(['b', 'c']), [run('b'), run('c')])).toEqual([])
    expect(endedRuns(new Set(), [run('x')])).toEqual([])
  })
})

describe('runEndedMessage', () => {
  it('names the script, the result and the exit code of failures', () => {
    expect(runEndedMessage(run('r1', { status: 'finished', exit_code: 0 }))).toEqual({ title: 'Backup: finished', body: 'Run r1' })
    expect(runEndedMessage(run('r2', { status: 'failed', exit_code: 2 }))).toEqual({
      title: 'Backup: failed',
      body: 'Run r2 (exit code 2)',
    })
    expect(runEndedMessage(run('r3', { status: 'timed_out', exit_code: -1 })).title).toBe('Backup: timed out')
  })
})
