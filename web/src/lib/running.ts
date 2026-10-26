// Polls the runs in progress for the header badge, the Running page and notifications.
import { reactive } from 'vue'
import { api, type RunMeta } from '../api'
import { endedRuns, notifyRunEnded } from './notify'

const interval = 10_000

export const running = reactive({
  runs: [] as RunMeta[],
  loaded: false,
})

let timer: ReturnType<typeof setTimeout> | undefined
let mine = new Set<string>() // own runs seen running, to notify when they end
let me = ''
let options: { open: (id: string) => void; current: () => string | undefined } | undefined

async function poll() {
  try {
    running.runs = await api.runs({ status: 'running' })
    running.loaded = true
    for (const id of endedRuns(mine, running.runs)) {
      // Not when the user is already watching that run.
      if (document.visibilityState === 'visible' && options?.current() === id) continue
      void api.run(id).then((r) => notifyRunEnded(r, options!.open), () => {})
    }
    mine = new Set(running.runs.filter((r) => r.user.toLowerCase() === me.toLowerCase()).map((r) => r.id))
  } catch {
    // signed out or offline: try again later
  }
  schedule()
}

function schedule() {
  clearTimeout(timer)
  if (document.visibilityState === 'visible' || mine.size > 0) timer = setTimeout(poll, interval)
}

function onVisibility() {
  if (document.visibilityState === 'visible') void poll()
}

/** Starts polling for the signed-in user. */
export function startRunning(user: string, opts: NonNullable<typeof options>) {
  stopRunning()
  me = user
  options = opts
  document.addEventListener('visibilitychange', onVisibility)
  void poll()
}

/** Refreshes now, e.g. right after starting or stopping a run. */
export function refreshRunning() {
  if (options) void poll()
}

export function stopRunning() {
  clearTimeout(timer)
  document.removeEventListener('visibilitychange', onVisibility)
  running.runs = []
  running.loaded = false
  mine = new Set()
  options = undefined
}
