import { describe, expect, it } from 'vitest'
import { migrateStorage } from './storage'

function memoryStorage(initial: Record<string, string> = {}) {
  const data: Record<string, string> = { ...initial }
  const s = {
    get length() {
      return Object.keys(data).length
    },
    key: (i: number) => Object.keys(data)[i] ?? null,
    getItem: (k: string) => data[k] ?? null,
    setItem: (k: string, v: string) => void (data[k] = v),
    removeItem: (k: string) => void delete data[k],
  } as unknown as Storage
  return { data, storage: () => s }
}

describe('migrateStorage', () => {
  it('moves runwarden.* keys to ward.* and removes the old ones', () => {
    const { data, storage } = memoryStorage({
      'runwarden.theme': 'dark',
      'runwarden.notify': 'on',
      'runwarden.groups': '["Ops"]',
      other: 'x',
    })
    migrateStorage(storage)
    expect(data).toEqual({ 'ward.theme': 'dark', 'ward.notify': 'on', 'ward.groups': '["Ops"]', other: 'x' })
  })

  it('keeps an existing new key', () => {
    const { data, storage } = memoryStorage({ 'runwarden.theme': 'dark', 'ward.theme': 'light' })
    migrateStorage(storage)
    expect(data).toEqual({ 'ward.theme': 'light' })
  })

  it('does nothing twice and survives blocked storage', () => {
    const { data, storage } = memoryStorage({ 'ward.theme': 'dark' })
    migrateStorage(storage)
    expect(data).toEqual({ 'ward.theme': 'dark' })
    expect(() =>
      migrateStorage(() => {
        throw new Error('storage disabled')
      }),
    ).not.toThrow()
  })
})
