// Who is signed in, and how sign-in works on this server.
import { reactive } from 'vue'
import { api, ApiError, type Me } from './api'

export const session = reactive({
  me: null as Me | null,
  authType: '',
  loaded: false,
})

/** Loads the auth type and the current user; me stays null when signed out. */
export async function loadSession(): Promise<void> {
  try {
    session.authType = (await api.authConfig()).type
  } catch {
    session.authType = ''
  }
  try {
    session.me = await api.me()
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) throw e
    session.me = null
  }
  session.loaded = true
}

/** Sends the browser to the provider for redirect-based sign-in. */
export function redirectToProvider(next: string) {
  window.location.assign(`/auth/login?next=${encodeURIComponent(next)}`)
}

export async function signOut(): Promise<string | undefined> {
  const { logout_url } = await api.logout()
  session.me = null
  return logout_url
}
