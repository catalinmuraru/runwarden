import { describe, expect, it } from 'vitest'
import type { SecretsOverview } from '../api'
import { gapCount, isolationSummary, kubeGapCount, secretCell, secretColumns } from './secrets'

const overview: SecretsOverview = {
  configured: true,
  environments: ['development', 'production'],
  available: { development: ['DB'], common: ['TOKEN'] },
  unknown_dirs: [],
  scripts: [
    { name: 'Copy', secrets: ['DB', 'TOKEN'], missing: { production: ['DB'] }, envless: false },
    { name: 'Report', secrets: ['TOKEN'], missing: {}, envless: true },
  ],
  kubernetes: {
    configured: true,
    clusters: [{ environment: 'development', server: 'https://dev:6443' }],
    unknown_dirs: [],
    scripts: [
      { name: 'Deploy', missing: ['production'] },
      { name: 'Scale', missing: [] },
    ],
  },
  isolation: { mode: 'auto', active: true, abi: 7, signal_scoping: true },
}

describe('secrets matrix', () => {
  it('has a column per environment plus "no environment"', () => {
    expect(secretColumns(overview)).toEqual(['development', 'production', '-'])
  })
  it('marks available, missing and not applicable cells', () => {
    const [copy, report] = overview.scripts
    expect(secretCell(copy, 'development')).toEqual({ state: 'ok' })
    expect(secretCell(copy, 'production')).toEqual({ state: 'missing', names: ['DB'] })
    expect(secretCell(copy, '-')).toEqual({ state: 'n/a' })
    expect(secretCell(report, '-')).toEqual({ state: 'ok' })
    expect(secretCell(report, 'production')).toEqual({ state: 'n/a' })
  })
  it('counts the gaps', () => {
    expect(gapCount(overview)).toBe(1)
  })
  it('counts the Kubernetes gaps', () => {
    expect(kubeGapCount(overview)).toBe(1)
  })
})

describe('isolation summary', () => {
  it('rates the isolation', () => {
    expect(isolationSummary(overview.isolation).tone).toBe('ok')
    expect(isolationSummary({ mode: 'required', active: true, abi: 4, signal_scoping: false })).toEqual({
      tone: 'warn',
      text: 'Sandboxed with Landlock ABI 4; signals are not scoped (needs ABI 6, kernel 6.12)',
    })
    expect(isolationSummary({ mode: 'auto', active: false, abi: 0, signal_scoping: false, reason: 'landlock is not available' }).text).toBe(
      'Not sandboxed (ISOLATION=auto): landlock is not available',
    )
  })
})
