import type { DecompressionAssessment } from '@/types/assessment'
import type { DivePlan } from '@/types/plan'

export const SUPERSEDED_STATUS = 'superseded'

export const isSuperseded = (assessment: DecompressionAssessment) => assessment.assessment_status === SUPERSEDED_STATUS

// A returned plan carries a rerun gate pinned to its current input version.
// Only an exposure-segment change (create/update/reorder) bumps the version
// past the gate and unlocks a new model run.
export const isAwaitingSegmentChange = (plan: DivePlan) =>
  plan.plan_status === 'draft' && plan.rerun_gate_version >= plan.version

export interface AvailableActions {
  editSegments: boolean
  runModel: boolean
  submit: boolean
  approve: boolean
  returnForRework: boolean
  awaitingSegmentChange: boolean
  /** True when this assessment can no longer be submitted or approved. */
  locked: boolean
  hints: string[]
}

// Derive the currently executable actions purely from server state so every
// refresh and every polled reload shows the same answer the API enforces.
export function availableActions(plan: DivePlan | undefined, assessment: DecompressionAssessment | null, role: { planner: boolean; supervisor: boolean }): AvailableActions {
  if (!plan || !assessment) {
    return { editSegments: false, runModel: false, submit: false, approve: false, returnForRework: false, awaitingSegmentChange: false, locked: !assessment, hints: assessment ? [] : ['Select an assessment to inspect its review state.'] }
  }
  const superseded = isSuperseded(assessment)
  const awaiting = isAwaitingSegmentChange(plan)
  const hints: string[] = []
  let editSegments = false
  let runModel = false
  let submit = false
  let approve = false
  let returnForRework = false

  if (superseded) {
    hints.push(assessment.superseded_by_id ? `Superseded by revision assessment #${assessment.superseded_by_id}; the preserved snapshot is read-only.` : 'Returned for rework; this assessment can no longer be submitted or approved.')
  }

  if (plan.plan_status === 'draft') {
    editSegments = role.planner
    if (awaiting) {
      hints.push('Change at least one exposure segment (add, edit, or reorder) before rerunning the model.')
    } else {
      runModel = role.planner
    }
    if (superseded && !assessment.superseded_by_id) {
      hints.push('After the new assessment is generated, the plan returns to the review queue.')
    }
  }
  if (!superseded) {
    if (plan.plan_status === 'modeled' && assessment.assessment_status === 'modeled') {
      submit = role.planner
      hints.push(submit ? 'Planner can submit this revision for supervisor review.' : 'Waiting for the planner to submit this revision.')
    }
    if (plan.plan_status === 'pending_supervisor_review' && assessment.assessment_status === 'pending_supervisor_review') {
      approve = role.supervisor
      returnForRework = role.supervisor
      hints.push(approve ? 'Supervisor can approve for training comparison or return it to draft with a reason.' : 'Waiting for supervisor review.')
    }
    if (plan.plan_status === 'approved_for_training') {
      hints.push('Approved for training comparison only; no operational clearance was issued.')
    }
  }
  return { editSegments, runModel, submit, approve, returnForRework, awaitingSegmentChange: awaiting, locked: superseded, hints }
}
