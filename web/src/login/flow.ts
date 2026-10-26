// Pure helpers of the sign-in page.

/** Where to go after signing in: the page that was asked for, without the error parameter. */
export function nextFrom(loc: { pathname: string; search: string }): string {
  const path = loc.pathname.startsWith('/') && !loc.pathname.startsWith('//') ? loc.pathname : '/'
  const params = new URLSearchParams(loc.search)
  if (!params.has('sign_in_error')) return path + loc.search // keep the query exactly as it was
  params.delete('sign_in_error')
  const query = params.toString()
  return query ? `${path}?${query}` : path
}

const messages: Record<string, string> = {
  expired: 'The sign-in expired or was started in another browser. Please try again.',
  refused: 'Sign-in was cancelled or refused by the identity provider.',
  failed: 'Sign-in failed. Please try again.',
  throttled: 'Too many failed sign-ins. Try again later.',
  'not-member': 'Your account is not allowed to use Runwarden. Ask an administrator for access.',
  unavailable: 'Sign-in is unavailable right now. Try again later.',
  'unknown-method': 'That sign-in method is not available.',
}

/** The message for a ?sign_in_error= code, or '' when there is none. */
export function errorMessage(search: string): string {
  const code = new URLSearchParams(search).get('sign_in_error')
  if (!code) return ''
  return messages[code] ?? messages.failed
}

/** URL that starts a redirect sign-in with a provider. */
export function redirectURL(method: string, next: string): string {
  return `/auth/login?method=${encodeURIComponent(method)}&next=${encodeURIComponent(next)}`
}
