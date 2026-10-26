import { describe, expect, it } from 'vitest'
import { errorMessage, nextFrom, redirectURL } from './flow'

describe('nextFrom', () => {
  it('returns to the page that was asked for', () => {
    expect(nextFrom({ pathname: '/runs/42', search: '' })).toBe('/runs/42')
    expect(nextFrom({ pathname: '/runs', search: '?script=Restart%20Service' })).toBe('/runs?script=Restart%20Service')
    expect(nextFrom({ pathname: '/runs', search: '?script=Backup&sign_in_error=expired' })).toBe('/runs?script=Backup')
    expect(nextFrom({ pathname: '/', search: '?sign_in_error=failed' })).toBe('/')
    expect(nextFrom({ pathname: '//evil.example', search: '' })).toBe('/')
  })
})

describe('errorMessage', () => {
  it('explains provider errors and ignores normal visits', () => {
    expect(errorMessage('')).toBe('')
    expect(errorMessage('?sign_in_error=not-member')).toContain('not allowed')
    expect(errorMessage('?sign_in_error=something-new')).toContain('Sign-in failed')
  })
})

describe('redirectURL', () => {
  it('encodes the method and the next page', () => {
    expect(redirectURL('gitlab', '/runs?script=A B')).toBe('/auth/login?method=gitlab&next=%2Fruns%3Fscript%3DA%20B')
  })
})
