// The application colour theme, chosen by an administrator (Admin > Appearance) for everyone.
// "system" follows the OS (no data-theme attribute); styles.css reads html[data-theme].
import type { Theme } from '../api'

export const themes: readonly Theme[] = ['system', 'light', 'dark']

const storageKey = 'runwarden.theme'

const isTheme = (v: unknown): v is Theme => themes.includes(v as Theme)

/** Sets the theme on the page and remembers it for the next page load. */
export function applyTheme(t: Theme, root: HTMLElement = document.documentElement, storage: () => Storage = () => localStorage) {
  if (t === 'system') delete root.dataset.theme
  else root.dataset.theme = t
  try {
    storage().setItem(storageKey, t)
  } catch {
    // storage unavailable: the page fetches the theme on every load anyway
  }
}

export function cachedTheme(storage: () => Storage = () => localStorage): Theme {
  try {
    const v = storage().getItem(storageKey)
    return isTheme(v) ? v : 'system'
  } catch {
    return 'system'
  }
}

/**
 * Applies the cached theme at once (no flash of the wrong colours), then the one the server has.
 * A failed fetch keeps the cached theme.
 */
export async function initTheme(
  fetchTheme: () => Promise<{ theme: Theme }>,
  root: HTMLElement = document.documentElement,
  storage: () => Storage = () => localStorage,
) {
  applyTheme(cachedTheme(storage), root, storage)
  try {
    const { theme } = await fetchTheme()
    if (isTheme(theme)) applyTheme(theme, root, storage)
  } catch {
    // keep the cached theme
  }
}
