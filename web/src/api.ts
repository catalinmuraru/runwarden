// Typed client for the Runwarden API.

export interface Me {
  name: string
  groups: string[]
  admin: boolean // devops: every script, every run and the Admin area
  roles: string[] // empty: guest
  auth_type: string
}

export interface SignInMethod {
  id: string // gitlab, oidc, ldap or local (built-in administrator)
  label: string
  kind: 'redirect' | 'password'
}

export interface ScriptSummary {
  name: string
  group?: string
  description?: string
  tags?: string[]
}

export type Access = 'view' | 'run'

export interface Option {
  value: string
  label: string
  // For values of environment parameters: the environment and the user's access there.
  environment?: string
  access?: Access
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
  environment?: 'target' | 'source' // the parameter selects an environment
}

export interface ScriptDetail extends ScriptSummary {
  requires_terminal: boolean
  timeout_seconds: number // the run is stopped after this long
  parameters: Param[]
  access: Record<string, Access> // per environment ("-": scripts without environments)
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
  terminal?: boolean // ran on a pseudo-terminal (input is echoed by the terminal)
  environments?: string[] // "-" for runs of scripts without environments
  can_control?: boolean // only on a single run: may send input, stop and kill
}

export interface RunFilter {
  script?: string
  user?: string
  status?: string
  limit?: number
}

export interface AdminUser {
  name: string
  groups?: string[]
  first_seen: string
  last_seen: string
  last_login?: string
  signed_out?: string
  last_ip?: string
  status: 'active' | 'signed-in' | 'signed-out' | 'never'
  runs: number
  running: number
  admin: boolean
  roles: string[] // empty: guest
  fixed?: boolean // devops through the deployment settings
  sessions: number // valid sign-in sessions
  methods?: string[]
  exceptions: number // active exceptions
}

export interface PolicyEnvironment {
  name: string
  match?: string[]
}

export interface Role {
  name: string
  description?: string
}

export interface Rule {
  id: string
  role: string
  scripts: string[] // "*", script names or "tag:<tag>"
  environments: string[] // "*", "-" or environment names
  access: Access
}

export interface PolicyException {
  id: string
  user: string
  scripts: string[]
  environments: string[]
  access: Access
  reason: string
  expires: string
  created_by: string
  created: string
}

export interface Policy {
  revision: number
  environments: PolicyEnvironment[]
  unknown_environment?: string
  roles: Role[]
  users: { name: string; roles: string[] }[]
  rules: Rule[]
  exceptions: PolicyException[]
}

export interface PolicyView {
  policy: Policy
  warnings: string[]
  bootstrap: string[]
  max_exception_days: number
  scripts: { name: string; group?: string; tags?: string[]; environment_parameters?: Record<string, string> }[]
  tags: string[]
}

export interface Grant {
  access: Access
  by: string
}

export interface EffectiveAccess {
  roles: string[]
  environments: string[]
  scripts: { name: string; group?: string; tags?: string[]; access: Record<string, Grant> }[]
}

export interface ImportSummary {
  environments: number
  roles: number
  users: number
  rules: number
  exceptions: number
  removed_rules: number
  removed_exceptions: number
  removed_users: number
}

export interface ActivityEvent {
  time: string
  action: string
  outcome: 'success' | 'failure'
  reason?: string
  message?: string
  user?: string
  roles?: string[]
  groups?: string[]
  source_ip?: string
  run_id?: string
  script?: string
  status?: string
  values?: Record<string, unknown>
  args?: string[]
  exit_code?: number
  duration_ms?: number
  count?: number
  environments?: string[]
  policy_object?: string
  policy_change?: string
}

export interface ActivityFilter {
  user?: string
  action?: string
  outcome?: string
  script?: string
  limit?: number
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

/** A request body sent as it is, with its content type. */
export class RawBody {
  readonly text: string
  readonly contentType: string

  constructor(text: string, contentType: string) {
    this.text = text
    this.contentType = contentType
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin', headers: {} }
  if (body instanceof FormData) {
    init.body = body
  } else if (body instanceof RawBody) {
    init.body = body.text
    ;(init.headers as Record<string, string>)['Content-Type'] = body.contentType
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
  authConfig: () => request<{ methods: SignInMethod[]; no_auth: boolean }>('GET', '/auth/config'),
  me: () => request<Me>('GET', '/api/me'),
  loginPassword: (method: string, username: string, password: string) =>
    request<Me>('POST', '/auth/login', { method, username, password }),
  logout: () => request<{ logout_url?: string }>('POST', '/auth/logout'),

  scripts: async () => (await request<{ scripts: ScriptSummary[] }>('GET', '/api/scripts')).scripts,
  script: (name: string) => request<ScriptDetail>('GET', `/api/scripts/${enc(name)}`),
  values: async (script: string, param: string, deps: Record<string, string | string[]>) =>
    (await request<{ values: Option[] }>('GET', `/api/scripts/${enc(script)}/parameters/${enc(param)}/values${query(deps)}`)).values,
  files: async (script: string, param: string, path: string[]) =>
    (await request<{ entries: FileEntry[] }>('GET', `/api/scripts/${enc(script)}/parameters/${enc(param)}/files${query({ path: path.join('/') })}`)).entries,
  startRun: (script: string, body: Record<string, unknown> | FormData) =>
    request<{ id: string }>('POST', `/api/scripts/${enc(script)}/runs`, body instanceof FormData ? body : { values: body }),

  runs: async (f: RunFilter = {}) =>
    (await request<{ runs: RunMeta[] }>('GET', `/api/runs${query({ ...f, limit: f.limit?.toString() })}`)).runs,
  run: (id: string) => request<RunMeta>('GET', `/api/runs/${enc(id)}`),
  input: (id: string, line: string, secret = false) => request<void>('POST', `/api/runs/${enc(id)}/input`, { line, secret }),
  closeInput: (id: string) => request<void>('POST', `/api/runs/${enc(id)}/input`, { close: true }),
  stop: (id: string) => request<void>('POST', `/api/runs/${enc(id)}/stop`),
  kill: (id: string) => request<void>('POST', `/api/runs/${enc(id)}/kill`),
  fileURL: (id: string, name: string) => `/api/runs/${enc(id)}/files/${enc(name)}`,
  adminSignOut: (name: string) => request<{ revoked: number }>('POST', `/api/admin/users/${enc(name)}/sign-out`),
  adminUsers: async () => (await request<{ users: AdminUser[] }>('GET', '/api/admin/users')).users,
  activity: async (f: ActivityFilter = {}) =>
    (await request<{ events: ActivityEvent[] }>('GET', `/api/admin/activity${query({ ...f, limit: f.limit?.toString() })}`)).events,

  policy: () => request<PolicyView>('GET', '/api/admin/policy'),
  putEnvironments: (revision: number, environments: PolicyEnvironment[], unknown_environment: string) =>
    request<PolicyView>('PUT', '/api/admin/policy/environments', { revision, environments, unknown_environment }),
  createRole: (revision: number, role: Role) => request<PolicyView>('POST', '/api/admin/policy/roles', { revision, ...role }),
  updateRole: (revision: number, name: string, role: Role) =>
    request<PolicyView>('PUT', `/api/admin/policy/roles/${enc(name)}`, { revision, ...role }),
  deleteRole: (revision: number, name: string) =>
    request<PolicyView>('DELETE', `/api/admin/policy/roles/${enc(name)}${query({ revision: String(revision) })}`),
  setUserRoles: (revision: number, name: string, roles: string[]) =>
    request<PolicyView>('PUT', `/api/admin/policy/users/${enc(name)}`, { revision, roles }),
  createRule: (revision: number, rule: Omit<Rule, 'id'>) => request<PolicyView>('POST', '/api/admin/policy/rules', { revision, ...rule }),
  updateRule: (revision: number, rule: Rule) => request<PolicyView>('PUT', `/api/admin/policy/rules/${enc(rule.id)}`, { revision, ...rule }),
  deleteRule: (revision: number, id: string) =>
    request<PolicyView>('DELETE', `/api/admin/policy/rules/${enc(id)}${query({ revision: String(revision) })}`),
  createException: (revision: number, e: Omit<PolicyException, 'id' | 'created' | 'created_by'>) =>
    request<PolicyView>('POST', '/api/admin/policy/exceptions', { revision, ...e }),
  endException: (revision: number, id: string) =>
    request<PolicyView>('DELETE', `/api/admin/policy/exceptions/${enc(id)}${query({ revision: String(revision) })}`),
  effective: (who: { user?: string; role?: string }) => request<EffectiveAccess>('GET', `/api/admin/policy/effective${query(who)}`),
  exportPolicyURL: '/api/admin/policy/export',
  previewImport: (yaml: string) =>
    request<{ summary: ImportSummary; warnings: string[] }>('POST', '/api/admin/policy/import?dry_run=true', new RawBody(yaml, 'application/yaml')),
  importPolicy: (revision: number, yaml: string) =>
    request<PolicyView>('POST', `/api/admin/policy/import${query({ revision: String(revision) })}`, new RawBody(yaml, 'application/yaml')),

  socketURL: (id: string, offset: number) => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${proto}//${location.host}/api/runs/${enc(id)}/ws?offset=${offset}`
  },
}
