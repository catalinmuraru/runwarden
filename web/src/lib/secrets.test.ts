import { describe, expect, it } from 'vitest'
import type { SecretsOverview } from '../api'
import { gapCount, secretCell, secretColumns } from './secrets'

const overview: SecretsOverview = {
  configured: true,
  environments: ['development', 'production'],
  available: { development: ['DB'], common: ['TOKEN'] },
  unknown_dirs: [],
  scripts: [
    { name: 'Copy', secrets: ['DB', 'TOKEN'], missing: { production: ['DB'] }, envless: false },
    { name: 'Report', secrets: ['TOKEN'], missing: {}, envless: true },
  ],
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
})
