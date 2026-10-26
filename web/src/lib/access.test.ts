import { describe, expect, it } from 'vitest'
import type { Param, ScriptDetail } from '../api'
import { accessSummary, decorateOptions, startBlocker } from './access'

const env: Param = { name: 'Environment', type: 'list', required: true, environment: 'target' }
const src: Param = { name: 'Source', type: 'list', required: true, environment: 'source' }
const opts = [
  { value: 'dev', label: 'dev', environment: 'development', access: 'run' as const },
  { value: 'prod', label: 'Production', environment: 'production', access: 'view' as const },
]

function script(parameters: Param[], access: ScriptDetail['access']): ScriptDetail {
  return { name: 'S', requires_terminal: false, timeout_seconds: 0, parameters, access }
}

describe('decorateOptions', () => {
  it('marks view-only targets', () => {
    expect(decorateOptions(env, opts)?.map((o) => o.label)).toEqual(['dev', 'Production (view only)'])
  })
  it('leaves sources and other parameters alone', () => {
    expect(decorateOptions(src, opts)).toBe(opts)
    expect(decorateOptions({ name: 'X', type: 'list', required: false }, opts)).toBe(opts)
  })
})

describe('startBlocker', () => {
  const s = script([env, src], { development: 'run', production: 'view' })
  it('allows run environments', () => {
    expect(startBlocker(s, { Environment: 'dev', Source: 'prod' }, { Environment: opts, Source: opts })).toBe('')
  })
  it('blocks view-only targets', () => {
    expect(startBlocker(s, { Environment: 'prod' }, { Environment: opts })).toContain('view-only access to production')
  })
  it('leaves unknown values to the server', () => {
    expect(startBlocker(s, { Environment: 'staging' }, { Environment: opts })).toBe('')
  })
  it('checks scripts without environments', () => {
    expect(startBlocker(script([], { '-': 'view' }), {}, {})).toContain('view-only access to this script')
    expect(startBlocker(script([], { '-': 'run' }), {}, {})).toBe('')
  })
})

describe('accessSummary', () => {
  it('groups environments', () => {
    expect(accessSummary(script([env], { development: 'run', production: 'view', '-': 'run' }))).toEqual({
      run: ['development'],
      view: ['production'],
    })
  })
})
