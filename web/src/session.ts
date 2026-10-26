// Who is signed in. Without a session the server serves the sign-in page instead of this app.
import { reactive } from 'vue'
import { api, ApiError, type Me } from './api'

export const session = reactive({
  me: null as Me | null,
  loaded: false,
})

/** The session ended (expired, signed out elsewhere, revoked): reload to get the sign-in page. */
export function sessionEnded() {
  session.me = null
  window.location.reload()
}

/** Loads the current user. */
export async function loadSession(): Promise<void> {
  try {
    session.me = await api.me()
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      sessionEnded()
      return
    }
    throw e
  }
  session.loaded = true
}

/** Signs out and returns the provider's sign-out URL, if it has one. */
export async function signOut(): Promise<string | undefined> {
  const { logout_url } = await api.logout()
  session.me = null
  return logout_url
}
