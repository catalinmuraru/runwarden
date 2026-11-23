// Browser storage keys moved from the "runwarden." prefix to "ward." in v0.5. Remove in v0.6.
const legacyPrefix = 'runwarden.'
const prefix = 'ward.'

/**
 * Moves every "runwarden.*" key to "ward.*" once: an existing new key wins, and the old key is
 * removed either way. Blocked storage is ignored.
 */
export function migrateStorage(storage: () => Storage = () => localStorage) {
  try {
    const s = storage()
    const old: string[] = []
    for (let i = 0; i < s.length; i++) {
      const k = s.key(i)
      if (k?.startsWith(legacyPrefix)) old.push(k)
    }
    for (const k of old) {
      const target = prefix + k.slice(legacyPrefix.length)
      const v = s.getItem(k)
      if (s.getItem(target) === null && v !== null) s.setItem(target, v)
      s.removeItem(k)
    }
  } catch {
    // storage unavailable: nothing to migrate
  }
}
