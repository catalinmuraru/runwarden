// Browser notifications when the user's own runs finish (opt-in, per browser).
import { reactive } from 'vue'
import type { RunMeta } from '../api'
import { statusLabel } from './format'

const storageKey = 'runwarden.notify'

function readPref(): boolean {
  try {
    return localStorage.getItem(storageKey) === 'on'
  } catch {
    return false
  }
}

function writePref(on: boolean) {
  try {
    localStorage.setItem(storageKey, on ? 'on' : 'off')
  } catch {
    // storage unavailable: the choice lasts until the page is reloaded
  }
}

export const notifications = reactive({
  supported: typeof window !== 'undefined' && 'Notification' in window && window.isSecureContext,
  enabled: false,
})
notifications.enabled = notifications.supported && readPref() && Notification.permission === 'granted'

/** Explains why notifications cannot be turned on, or returns '' when they can. */
export function unavailableReason(): string {
  if (!notifications.supported) return 'Notifications need a secure (HTTPS) connection and a browser that supports them.'
  if (Notification.permission === 'denied') return 'Notifications are blocked for this site in the browser settings.'
  return ''
}

export async function setNotifications(on: boolean): Promise<void> {
  if (on && notifications.supported && Notification.permission !== 'granted') {
    await Notification.requestPermission()
  }
  notifications.enabled = on && notifications.supported && Notification.permission === 'granted'
  writePref(notifications.enabled)
}

/** Returns the IDs that were running before and are no longer in the running list. */
export function endedRuns(before: Set<string>, running: RunMeta[]): string[] {
  const now = new Set(running.map((r) => r.id))
  return [...before].filter((id) => !now.has(id))
}

/** Text of the notification for a run that ended. */
export function runEndedMessage(run: RunMeta): { title: string; body: string } {
  const status = statusLabel(run.status).text.toLowerCase()
  const exit = run.exit_code !== undefined && run.status !== 'finished' ? ` (exit code ${run.exit_code})` : ''
  return { title: `${run.script}: ${status}`, body: `Run ${run.id}${exit}` }
}

/** Shows a notification for an ended run; clicking it opens the run. */
export function notifyRunEnded(run: RunMeta, open: (id: string) => void) {
  if (!notifications.enabled) return
  const { title, body } = runEndedMessage(run)
  const n = new Notification(title, { body, tag: `run-${run.id}` })
  n.onclick = () => {
    window.focus()
    open(run.id)
    n.close()
  }
}
