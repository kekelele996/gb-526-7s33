import { Chip } from '@mui/material'
import type { PlanStatus } from '@/types/plan'

const labels: Record<string, string> = {
  draft: 'DRAFT',
  modeled: 'MODELED',
  pending_supervisor_review: 'SUPERVISOR REVIEW',
  approved_for_training: 'TRAINING APPROVED',
  archived: 'ARCHIVED',
  superseded: 'SUPERSEDED',
}

export function PlanStatusBadge({ status }: { status: PlanStatus | string }) {
  const value = status as PlanStatus
  return <Chip size="small" className={`status-badge status-${value}`} label={labels[value] ?? status.replaceAll('_', ' ').toUpperCase()} />
}
