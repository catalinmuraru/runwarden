// Parameter form state: initial values, request bodies, error mapping, dependencies.
import type { Param } from '../api'

/** A form field value: text, list (multiselect, recursive server file), flag, or upload. */
export type FieldValue = string | string[] | boolean | File | null

export function isList(p: Param): boolean {
  return p.type === 'multiselect' || (p.type === 'server_file' && !!p.file_recursive)
}

export function initialValue(p: Param, prefill?: unknown): FieldValue {
  const source = prefill !== undefined && !p.secure ? prefill : p.default
  if (p.type === 'flag') return source === true || source === 'true'
  if (p.type === 'file_upload') return null
  if (isList(p)) {
    if (Array.isArray(source)) return source.map(String)
    return typeof source === 'string' && source !== '' ? [source] : []
  }
  if (source === undefined || source === null) return ''
  return Array.isArray(source) ? source.join(',') : String(source)
}

/**
 * Builds the request for a run. Constants are never sent (the server uses their value), and an
 * untouched secure field with a default is left out so the server applies the default it never
 * revealed. With an upload the body is multipart, otherwise plain values for JSON.
 */
export function buildRunBody(
  params: Param[],
  values: Record<string, FieldValue>,
  touched: ReadonlySet<string>,
): Record<string, unknown> | FormData {
  const out: Record<string, unknown> = {}
  const files: [string, File][] = []
  for (const p of params) {
    const v = values[p.name]
    if (p.constant) continue
    if (p.type === 'file_upload') {
      if (v instanceof File) files.push([p.name, v])
      continue
    }
    if (p.secure && p.has_default && !touched.has(p.name) && (v === '' || v === null)) continue
    if (v === null || v === undefined) continue
    out[p.name] = v
  }
  if (files.length === 0) return out
  const form = new FormData()
  form.append('values', JSON.stringify(out))
  for (const [name, file] of files) form.append(name, file, file.name)
  return form
}

/** Splits server validation details into per-field messages and the rest. */
export function fieldErrors(details: string[]): { fields: Record<string, string>; other: string[] } {
  const fields: Record<string, string> = {}
  const other: string[] = []
  for (const d of details) {
    const m = /^parameter "((?:[^"\\]|\\.)*)": (.*)$/.exec(d)
    if (m) {
      const name = m[1].replace(/\\(.)/g, '$1')
      fields[name] = fields[name] ? `${fields[name]}; ${m[2]}` : m[2]
    } else {
      other.push(d)
    }
  }
  return { fields, other }
}

/**
 * The query for a parameter's dynamic values, built from the parameters it depends on, or null
 * while one of them has no value yet.
 */
export function dependencyQuery(
  p: Param,
  values: Record<string, FieldValue>,
): Record<string, string | string[]> | null {
  const q: Record<string, string | string[]> = {}
  for (const dep of p.depends_on ?? []) {
    const v = values[dep]
    if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)) return null
    if (v instanceof File) return null
    q[dep] = typeof v === 'boolean' ? String(v) : v
  }
  return q
}
