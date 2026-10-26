// The access policy as shown in the Admin area, shared by its tabs.
import { reactive } from 'vue'
import { api, ApiError, type PolicyView } from '../api'

export const policyState = reactive({
  view: null as PolicyView | null,
  error: '',
  details: [] as string[],
  conflict: false, // someone else changed the policy: reload before changing it again
  saving: false,
})

export async function loadPolicy(): Promise<void> {
  policyState.error = ''
  policyState.details = []
  policyState.conflict = false
  try {
    policyState.view = await api.policy()
  } catch (e) {
    policyState.error = e instanceof Error ? e.message : String(e)
  }
}

/**
 * Sends one change based on the revision shown, and shows the policy that results. Returns false
 * when the change was refused (the reason is in policyState).
 */
export async function changePolicy(send: (revision: number) => Promise<PolicyView>): Promise<boolean> {
  if (!policyState.view) return false
  policyState.saving = true
  policyState.error = ''
  policyState.details = []
  try {
    policyState.view = await send(policyState.view.policy.revision)
    return true
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      policyState.conflict = true
    } else if (e instanceof ApiError) {
      policyState.error = e.message
      policyState.details = e.details
    } else {
      policyState.error = String(e)
    }
    return false
  } finally {
    policyState.saving = false
  }
}
