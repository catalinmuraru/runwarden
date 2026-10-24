// Typed client for the Runwarden API.

export interface Me {
  name: string
  groups: string[]
  admin: boolean
  auth_type: string
}

export interface ScriptSummary {
  name: string
  group?: string
  description?: string
}

export interface Option {
  value: string
  label: string
}

export interface Param {
  name: string
  type: string // parameter type, or "flag"
  description?: string
  required: boolean
  default?: string | string[] | boolean
  has_default?: boolean // secure default exists but is not sent
  constant?: boolean
  secure?: boolean
  values?: Option[]
  dynamic_values?: boolean
  depends_on?: string[]
  min?: number
  max?: number
  max_length?: number
  regex_description?: string
  file_recursive?: boolean
  file_type?: string
  file_extensions?: string[]
}

export interface ScriptDetail extends ScriptSummary {
  requires_terminal: boolean
  parameters: Param[]
}

export interface RunMeta {
  id: string
  script: string
  user: string
  args: string[] | null // null in records written before args were always a list
  values?: Record<string, unknown>
  status: string
  exit_code?: number
  started: string
  finished?: string
  output_truncated?: boolean
  files?: string[]
}

export interface FileEntry {
  name: string
  dir: boolean
}

export class ApiError extends Error {
  readonly status: number
  readonly details: string[]

  constructor(status: number, message: string, details: string[] = []) {
    super(message)
    this.status = status
    this.details = details
  }
}

let onUnauthorized: () => void = () => {}

/** Called on every 401, e.g. to send the user to the sign-in page. */
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin', headers: {} }
  if (body instanceof FormData) {
    init.body = body
  } else if (body !== undefined) {
    init.body = JSON.stringify(body)
    ;(init.headers as Record<string, string>)['Content-Type'] = 'application/json'
  }
  const resp = await fetch(path, init)
  if (resp.status === 401) {
    onUnauthorized()
  }
  if (!resp.ok) {
    let message = resp.statusText
    let details: string[] = []
    try {
      const err = await resp.json()
      message = err.error ?? message
      details = err.details ?? []
    } catch {
      // not JSON
    }
    throw new ApiError(resp.status, message, details)
  }
  if (resp.status === 204) {
    return undefined as T
  }
  return (await resp.json()) as T
}

const enc = encodeURIComponent

function query(params: Record<string, string | string[] | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined) continue
    for (const item of Array.isArray(v) ? v : [v]) q.append(k, item)
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

export const api = {
  authConfig: () => request<{ type: string }>('GET', '/auth/config'),
  me: () => request<Me>('GET', '/api/me'),
  loginPassword: (username: string, password: string) =>
    request<Me>('POST', '/auth/login', { username, password }),
  logout: () => request<{ logout_url?: string }>('POST', '/auth/logout'),

  scripts: async () => (await request<{ scripts: ScriptSummary[] }>('GET', '/api/scripts')).scripts,
  script: (name: string) => request<ScriptDetail>('GET', `/api/scripts/${enc(name)}`),
  values: async (script: string, param: string, deps: Record<string, string | string[]>) =>
    (await request<{ values: Option[] }>('GET', `/api/scripts/${enc(script)}/parameters/${enc(param)}/values${query(deps)}`)).values,
  files: async (script: string, param: string, path: string[]) =>
    (await request<{ entries: FileEntry[] }>('GET', `/api/scripts/${enc(script)}/parameters/${enc(param)}/files${query({ path: path.join('/') })}`)).entries,
  startRun: (script: string, body: Record<string, unknown> | FormData) =>
    request<{ id: string }>('POST', `/api/scripts/${enc(script)}/runs`, body instanceof FormData ? body : { values: body }),

  runs: async (script?: string) => (await request<{ runs: RunMeta[] }>('GET', `/api/runs${query({ script })}`)).runs,
  run: (id: string) => request<RunMeta>('GET', `/api/runs/${enc(id)}`),
  input: (id: string, line: string) => request<void>('POST', `/api/runs/${enc(id)}/input`, { line }),
  closeInput: (id: string) => request<void>('POST', `/api/runs/${enc(id)}/input`, { close: true }),
  stop: (id: string) => request<void>('POST', `/api/runs/${enc(id)}/stop`),
  kill: (id: string) => request<void>('POST', `/api/runs/${enc(id)}/kill`),
  fileURL: (id: string, name: string) => `/api/runs/${enc(id)}/files/${enc(name)}`,
  socketURL: (id: string, offset: number) => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${proto}//${location.host}/api/runs/${enc(id)}/ws?offset=${offset}`
  },
}
