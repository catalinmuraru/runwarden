// Choices offered by the rule and exception forms.
import type { PolicyView } from '../../api'
import { NO_ENVIRONMENT, TAG } from '../../lib/policy'

export function scriptGroups(v: PolicyView) {
  return [
    { title: 'Tags', options: v.tags.map((t) => ({ value: TAG + t, label: `tag ${t}` })) },
    { title: 'Scripts', options: v.scripts.map((s) => ({ value: s.name, label: s.name })) },
  ]
}

export function environmentGroups(v: PolicyView) {
  return [
    {
      title: 'Environments',
      options: [
        ...v.policy.environments.map((e) => ({ value: e.name, label: e.name })),
        { value: NO_ENVIRONMENT, label: 'scripts without environment' },
      ],
    },
  ]
}
