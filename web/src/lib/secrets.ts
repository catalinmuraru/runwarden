// The Secrets page of the Admin area: which declared secrets are available where.
import type { SecretsOverview } from '../api'

export type SecretCell = { state: 'ok' } | { state: 'missing'; names: string[] } | { state: 'n/a' }

/** Columns of the matrix: the environments, or "-" for scripts without environment parameters. */
export function secretColumns(o: SecretsOverview): string[] {
  return [...o.environments, '-']
}

/** Whether a script has all its declared secrets in a column. */
export function secretCell(s: SecretsOverview['scripts'][number], column: string): SecretCell {
  if (s.envless !== (column === '-')) return { state: 'n/a' }
  const missing = s.missing[column] ?? []
  return missing.length ? { state: 'missing', names: missing } : { state: 'ok' }
}

/** Number of script × environment combinations that cannot run for lack of a secret. */
export function gapCount(o: SecretsOverview): number {
  return o.scripts.reduce((n, s) => n + Object.values(s.missing).filter((m) => m.length).length, 0)
}

/** Number of Kubernetes runner × environment combinations that cannot run for lack of a usable kubeconfig. */
export function kubeGapCount(o: SecretsOverview): number {
  return o.kubernetes.scripts.reduce((n, s) => n + s.missing.length, 0)
}
