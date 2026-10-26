import { describe, expect, it } from 'vitest'
import type { Policy } from '../api'
import {
  assignableRoles,
  endOfDay,
  environmentOf,
  exceptionProblems,
  lastExpiryDate,
  parseList,
  ruleProblems,
  ruleRoles,
  scriptLabel,
} from './policy'

const policy: Policy = {
  revision: 3,
  environments: [
    { name: 'development', match: ['dev*', 'test?'] },
    { name: 'acceptance', match: ['acc*', 'uat[0-9]'] },
    { name: 'production', match: ['prod*'] },
  ],
  roles: [{ name: 'guest' }, { name: 'developer' }, { name: 'consultant' }, { name: 'devops' }, { name: 'support' }],
  users: [],
  rules: [],
  exceptions: [],
}

describe('roles', () => {
  it('lists assignable and rule roles', () => {
    expect(assignableRoles(policy)).toEqual(['developer', 'consultant', 'devops', 'support'])
    expect(ruleRoles(policy)).toEqual(['developer', 'consultant', 'support'])
  })
})

describe('environmentOf', () => {
  it('matches names and patterns like the server', () => {
    expect(environmentOf(policy, 'Development')).toBe('development')
    expect(environmentOf(policy, 'dev-01')).toBe('development')
    expect(environmentOf(policy, 'test1')).toBe('development')
    expect(environmentOf(policy, 'test12')).toBe('production') // ? is one character: unknown → last
    expect(environmentOf(policy, 'uat3')).toBe('acceptance')
    expect(environmentOf(policy, 'PROD-west')).toBe('production')
    expect(environmentOf(policy, ' ')).toBe('')
    expect(environmentOf({ ...policy, unknown_environment: 'acceptance' }, 'staging')).toBe('acceptance')
    expect(environmentOf({ environments: [] }, 'x')).toBe('-')
  })
  it('treats regex characters literally', () => {
    expect(environmentOf({ environments: [{ name: 'a', match: ['a.b'] }, { name: 'z' }] }, 'axb')).toBe('z')
  })
})

describe('drafts', () => {
  it('checks rules', () => {
    expect(ruleProblems({ role: '', scripts: [], environments: [], access: '' })).toHaveLength(4)
    expect(ruleProblems({ role: 'developer', scripts: ['tag:dev'], environments: ['*'], access: 'run' })).toEqual([])
  })
  it('checks exceptions', () => {
    const now = new Date(2026, 9, 26, 12, 0)
    const base = { user: 'ann', reason: 'INC-1', scripts: ['*'], environments: ['production'], access: 'run' as const }
    expect(exceptionProblems({ ...base, expires: '2026-10-30' }, now, 90)).toEqual([])
    expect(exceptionProblems({ ...base, expires: '2026-10-25' }, now, 90)).toEqual(['The end must be in the future.'])
    expect(exceptionProblems({ ...base, expires: '2027-03-01' }, now, 90)[0]).toContain('at most 90 days')
    expect(exceptionProblems({ ...base, reason: ' ', expires: '' }, now, 90)).toHaveLength(2)
  })
  it('computes dates', () => {
    expect(endOfDay('2026-10-30').getHours()).toBe(23)
    // The end of the last allowed day stays within the limit.
    const now = new Date(2026, 9, 26, 12, 0)
    const last = lastExpiryDate(now, 7)
    expect(last).toBe('2026-11-01')
    expect(exceptionProblems({ user: 'a', reason: 'r', scripts: ['*'], environments: ['*'], access: 'run', expires: last }, now, 7)).toEqual([])
  })
})

describe('labels', () => {
  it('formats selectors and lists', () => {
    expect(scriptLabel('*')).toBe('all scripts')
    expect(scriptLabel('tag:ops')).toBe('tag ops')
    expect(parseList(' a, ,b ')).toEqual(['a', 'b'])
  })
})
