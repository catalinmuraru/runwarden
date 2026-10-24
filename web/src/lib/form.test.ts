import { describe, expect, it } from 'vitest'
import type { Param } from '../api'
import { buildRunBody, dependencyQuery, fieldErrors, initialValue } from './form'

const p = (over: Partial<Param>): Param => ({ name: 'p', type: 'text', required: false, ...over })

describe('initialValue', () => {
  it('uses defaults by type', () => {
    expect(initialValue(p({ type: 'flag', default: true }))).toBe(true)
    expect(initialValue(p({ type: 'flag' }))).toBe(false)
    expect(initialValue(p({ type: 'multiselect', default: ['a'] }))).toEqual(['a'])
    expect(initialValue(p({ type: 'multiselect', default: 'a' }))).toEqual(['a'])
    expect(initialValue(p({ type: 'server_file', file_recursive: true }))).toEqual([])
    expect(initialValue(p({ type: 'int', default: '6' }))).toBe('6')
    expect(initialValue(p({ type: 'file_upload' }))).toBeNull()
    expect(initialValue(p({}))).toBe('')
  })

  it('prefills from a previous run, except secure values', () => {
    expect(initialValue(p({ default: 'd' }), 'previous')).toBe('previous')
    expect(initialValue(p({ secure: true, default: 'd' }), '******')).toBe('d')
  })
})

describe('buildRunBody', () => {
  const params = [
    p({ name: 'Name' }),
    p({ name: 'Key', secure: true, has_default: true }),
    p({ name: 'Version', constant: true, default: '1' }),
    p({ name: 'Flag', type: 'flag' }),
    p({ name: 'Tags', type: 'multiselect' }),
    p({ name: 'Upload', type: 'file_upload' }),
  ]

  it('sends JSON values, skipping constants and untouched secure defaults', () => {
    const body = buildRunBody(params, { Name: '', Key: '', Version: 'x', Flag: true, Tags: ['a'], Upload: null }, new Set())
    expect(body).toEqual({ Name: '', Flag: true, Tags: ['a'] })
  })

  it('sends a touched secure field even when cleared', () => {
    const body = buildRunBody(params, { Key: '' }, new Set(['Key']))
    expect(body).toEqual({ Key: '' })
  })

  it('switches to multipart with uploads', () => {
    const file = new File(['hello'], 'list.csv')
    const body = buildRunBody(params, { Name: 'abc', Upload: file }, new Set())
    expect(body).toBeInstanceOf(FormData)
    const form = body as FormData
    expect(JSON.parse(form.get('values') as string)).toEqual({ Name: 'abc' })
    expect((form.get('Upload') as File).name).toBe('list.csv')
  })
})

describe('fieldErrors', () => {
  it('maps parameter messages to fields', () => {
    expect(
      fieldErrors([
        'parameter "Name": is required',
        'parameter "Na\\"me": must match x',
        'parameter "Name": too long',
        'unknown parameter "X"',
      ]),
    ).toEqual({
      fields: { Name: 'is required; too long', 'Na"me': 'must match x' },
      other: ['unknown parameter "X"'],
    })
  })
})

describe('dependencyQuery', () => {
  const target = p({ name: 'Target', depends_on: ['Env', 'Tags'] })
  it('waits until dependencies have values', () => {
    expect(dependencyQuery(target, { Env: '', Tags: ['a'] })).toBeNull()
    expect(dependencyQuery(target, { Env: 'prod', Tags: [] })).toBeNull()
    expect(dependencyQuery(target, { Env: 'prod', Tags: ['a', 'b'] })).toEqual({ Env: 'prod', Tags: ['a', 'b'] })
    expect(dependencyQuery(p({}), {})).toEqual({})
  })
})
