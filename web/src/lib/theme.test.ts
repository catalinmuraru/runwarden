import { describe, expect, it } from 'vitest'
import type { Theme } from '../api'
import { applyTheme, cachedTheme, initTheme } from './theme'

const root = () => ({ dataset: {} as Record<string, string> }) as unknown as HTMLElement

function memoryStorage(initial: Record<string, string> = {}) {
  const data = { ...initial }
  const s = {
    getItem: (k: string) => data[k] ?? null,
    setItem: (k: string, v: string) => void (data[k] = v),
  } as unknown as Storage
  return { data, storage: () => s }
}

const broken = () => {
  throw new Error('storage disabled')
}

describe('applyTheme', () => {
  it('sets the attribute for light and dark and removes it for system', () => {
    const r = root()
    const { data, storage } = memoryStorage()
    applyTheme('dark', r, storage)
    expect(r.dataset.theme).toBe('dark')
    expect(data['runwarden.theme']).toBe('dark')
    applyTheme('light', r, storage)
    expect(r.dataset.theme).toBe('light')
    applyTheme('system', r, storage)
    expect(r.dataset.theme).toBeUndefined()
    expect(data['runwarden.theme']).toBe('system')
  })

  it('works without storage', () => {
    const r = root()
    applyTheme('dark', r, broken)
    expect(r.dataset.theme).toBe('dark')
  })
})

describe('cachedTheme', () => {
  it('reads a valid cached value and falls back to system', () => {
    expect(cachedTheme(memoryStorage({ 'runwarden.theme': 'light' }).storage)).toBe('light')
    expect(cachedTheme(memoryStorage({ 'runwarden.theme': 'purple' }).storage)).toBe('system')
    expect(cachedTheme(memoryStorage().storage)).toBe('system')
    expect(cachedTheme(broken)).toBe('system')
  })
})

describe('initTheme', () => {
  it('applies the cached theme before the fetch answers, then the server theme', async () => {
    const r = root()
    const { data, storage } = memoryStorage({ 'runwarden.theme': 'dark' })
    let answer: (v: { theme: Theme }) => void = () => {}
    const done = initTheme(() => new Promise((res) => (answer = res)), r, storage)
    expect(r.dataset.theme).toBe('dark')
    answer({ theme: 'light' })
    await done
    expect(r.dataset.theme).toBe('light')
    expect(data['runwarden.theme']).toBe('light')
  })

  it('keeps the cached theme when the fetch fails or returns nonsense', async () => {
    const r = root()
    const { storage } = memoryStorage({ 'runwarden.theme': 'dark' })
    await initTheme(() => Promise.reject(new Error('offline')), r, storage)
    expect(r.dataset.theme).toBe('dark')
    await initTheme(() => Promise.resolve({ theme: 'purple' as Theme }), r, storage)
    expect(r.dataset.theme).toBe('dark')
  })
})
