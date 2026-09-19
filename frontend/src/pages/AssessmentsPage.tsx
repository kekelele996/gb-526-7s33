import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, MenuItem, TextField } from '@mui/material'
import { ArrowRight, CheckCheck, GitCompareArrows, History, RotateCcw, Send, ShieldAlert, Undo2 } from 'lucide-react'
import { AssumptionPanel } from '@/components/common/AssumptionPanel'
import { PageHeader } from '@/components/common/PageHeader'
import { PlanStatusBadge } from '@/components/common/PlanStatusBadge'
import { getPlan } from '@/api/plan'
import { useAuth } from '@/hooks/useAuth'
import { useAssessmentPolling } from '@/hooks/useAssessmentPolling'
import { useAssessmentStore } from '@/stores/assessment'
import { usePlanStore } from '@/stores/plan'
import { availableActions, isSuperseded } from '@/utils/assessment'

type ReviewKind = 'submit' | 'approve' | 'return'

const defaultReason = 'Reviewed training assumptions and versioned model evidence.'

export function AssessmentsPage() {
  const { isPlanner, isSupervisor } = useAuth()
  const plans = usePlanStore()
  const assessments = useAssessmentStore()
  const [compareId, setCompareId] = useState<number>(0)
  const [reason, setReason] = useState(defaultReason)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  useEffect(() => { void assessments.load(); void plans.load() }, [assessments.load, plans.load])
  const reloadPlans = usePlanStore((state) => state.load)
  useAssessmentPolling(true, undefined, reloadPlans)
  const selected = assessments.selected
  const selectedPlan = useMemo(() => plans.items.find((plan) => plan.id === selected?.plan_id), [plans.items, selected?.plan_id])
  const superseded = selected ? isSuperseded(selected) : false
  const actions = availableActions(selectedPlan, selected, { planner: isPlanner, supervisor: isSupervisor })
  const samePlan = useMemo(() => {
    if (!selected) return []
    return assessments.items.filter((item) => item.plan_id === selected.plan_id)
  }, [assessments.items, selected])
  const replacement = useMemo(() => {
    if (!selected?.superseded_by_id) return null
    return assessments.items.find((item) => item.id === selected.superseded_by_id) ?? null
  }, [assessments.items, selected?.superseded_by_id])

  const transition = async (kind: ReviewKind) => {
    if (!selected) return
    setBusy(true); setLocalError(null); setNotice(null)
    try {
      const plan = await getPlan(selected.plan_id)
      const item = kind === 'submit'
        ? await assessments.submit(selected.id, plan.version, reason)
        : kind === 'approve'
          ? await assessments.approve(selected.id, plan.version, reason)
          : await assessments.returnForRework(selected.id, plan.version, reason)
      await plans.load()
      if (kind === 'return') {
        setNotice(`Assessment #${item.id} returned to draft with the recorded reason. The planner must change an exposure segment before the model can rerun.`)
        setReason(defaultReason)
      } else if (kind === 'submit') {
        setNotice('Assessment submitted for human supervisor review.')
      } else {
        setNotice('Assessment approved for training comparison; no operational clearance was issued.')
      }
    } catch (error) { setLocalError(error instanceof Error ? error.message : 'Review action failed') }
    finally { setBusy(false) }
  }

  return (
    <div className="page">
      <PageHeader eyebrow="IMMUTABLE MODEL RUNS" title="Assessment review" detail="Compare fixed snapshots, inspect risk evidence, record returns, and follow the current executable action." />
      {(assessments.error || plans.error || localError) && <Alert severity="error">{assessments.error ?? plans.error ?? localError}</Alert>}
      {notice && <Alert severity="success" onClose={() => setNotice(null)}>{notice}</Alert>}
      <div className="assessment-layout">
        <section className="assessment-queue">
          <div className="list-heading"><span>{assessments.items.length} RUNS</span><span>REVISION</span></div>
          {assessments.items.map((item) => <button key={item.id} className={`assessment-row ${selected?.id === item.id ? 'selected' : ''} ${isSuperseded(item) ? 'superseded' : ''}`} onClick={() => void assessments.select(item.id)}><div><strong>#{item.id} · {plans.items.find((plan) => plan.id === item.plan_id)?.plan_code ?? `Plan ${item.plan_id}`}</strong><span>{item.algorithm_version} · revision v{item.revision}</span></div><div><PlanStatusBadge status={item.assessment_status} /><b>{item.comparative_score.toFixed(1)}</b></div></button>)}
          {!assessments.items.length && <div className="empty-state">No immutable assessments recorded.</div>}
        </section>
        <section className="assessment-detail">
          {selected && selectedPlan ? <>
            <div className="assessment-title"><div><span className="eyebrow">ASSESSMENT #{selected.id} · REVISION V{selected.revision}</span><h2>{selectedPlan.plan_code}</h2><p>Created {new Date(selected.created_at).toLocaleString()} · input snapshot preserved and immutable</p></div><div className="score-dial"><span>COMPARATIVE INDEX</span><strong>{selected.comparative_score.toFixed(1)}</strong><small>{selected.highest_risk_band} · not a safety score</small></div></div>
            <div className="revision-strip"><History size={13} /><span>Plan input version <b>v{selectedPlan.version}</b></span><span>·</span><span>Revision order for this plan:</span>{samePlan.map((item) => <span key={item.id} className={selected.id === item.id ? '' : ''}><b>{selected.id === item.id ? `▶ v${item.revision}` : `v${item.revision}`}</b>{isSuperseded(item) ? ' (returned)' : ''}{item.superseded_by_id ? ` → #${item.superseded_by_id}` : ''}</span>)}</div>
            {superseded && <div className="return-banner"><Undo2 size={18} /><div><strong>Returned to drafting · assessment superseded</strong><p>Reason: {selected.returned_reason || 'No reason was recorded.'}</p><small>{selected.returned_at ? `Returned ${new Date(selected.returned_at).toLocaleString()}` : ''}{replacement ? ` · replaced by assessment #${replacement.id} (revision v${replacement.revision})` : ' · waiting for a segment change and a new model run'} · this assessment can no longer be submitted or approved</small></div></div>}
            <div className="action-panel"><ShieldAlert size={14} /><span>Current executable actions</span>
              {actions.submit && <Button size="small" variant="contained" startIcon={<Send size={15} />} disabled={busy || reason.trim().length < 3} onClick={() => void transition('submit')}>Submit for review</Button>}
              {actions.approve && <Button size="small" variant="contained" color="secondary" startIcon={<CheckCheck size={15} />} disabled={busy || reason.trim().length < 3} onClick={() => void transition('approve')}>Approve training</Button>}
              {actions.returnForRework && <Button size="small" variant="outlined" color="error" startIcon={<RotateCcw size={15} />} disabled={busy || reason.trim().length < 3} onClick={() => void transition('return')}>Return to draft</Button>}
              {actions.awaitingSegmentChange && <Button size="small" variant="outlined" disabled startIcon={<RotateCcw size={15} />}>Segment change required</Button>}
              <ul className="action-hints">{actions.hints.map((hint) => <li key={hint}>{hint}</li>)}</ul>
            </div>
            {(actions.submit || actions.approve || actions.returnForRework) && <div className="review-bar"><PlanStatusBadge status={selected.assessment_status} /><TextField label={actions.returnForRework && !actions.approve ? 'Mandatory return reason' : 'Review reason'} value={reason} onChange={(event) => setReason(event.target.value)} fullWidth required inputProps={{ minLength: 3, maxLength: 300 }} /><PlanStatusBadge status={selectedPlan.plan_status} /></div>}
            <section className="risk-section"><div className="subheading">Risk evidence <span>{selected.risk_flags.length}</span></div><div className="risk-list">{selected.risk_flags.map((flag) => <article className={`risk-row risk-${flag.band}`} key={flag.code}><ShieldAlert size={18} /><div><strong>{flag.code.replaceAll('_', ' ')}</strong><p>{flag.message}</p><small>{flag.evidence}</small></div><span>{flag.band}</span></article>)}</div></section>
            <div className="compartment-grid">{selected.compartment_loads.map((curve) => { const last = curve.points.at(-1); return <div key={curve.name}><span>{curve.name}</span><strong>{last?.total_inert_bar.toFixed(3)} bar</strong><small>N2 t½ {curve.n2_half_time_min} · He t½ {curve.he_half_time_min}</small></div> })}</div>
            <AssumptionPanel assumptions={selected.assumptions} />
            <section className="compare-panel"><div className="section-title"><GitCompareArrows size={18} /><div><strong>Compare immutable runs</strong><span>Difference is descriptive, not relative safety</span></div></div><TextField select label="Other assessment" value={compareId || ''} onChange={(event) => setCompareId(Number(event.target.value))} sx={{ minWidth: 220 }}>{assessments.items.filter((item) => item.id !== selected.id).map((item) => <MenuItem key={item.id} value={item.id}>#{item.id} · v{item.revision} · index {item.comparative_score.toFixed(1)}</MenuItem>)}</TextField><Button startIcon={<ArrowRight size={16} />} disabled={!compareId} onClick={() => void assessments.compare(selected.id, compareId)}>Compare</Button>{assessments.comparison && <div className="comparison-result"><strong>{assessments.comparison.score_delta >= 0 ? '+' : ''}{assessments.comparison.score_delta.toFixed(2)} index</strong><span>{assessments.comparison.flag_delta >= 0 ? '+' : ''}{assessments.comparison.flag_delta} flags</span><p>{assessments.comparison.summary.join(' ')}</p></div>}</section>
            <p className="disclaimer-line">{selected.safety_disclaimer}</p>
          </> : <div className="empty-state">Select an assessment to inspect its immutable evidence.</div>}
        </section>
      </div>
    </div>
  )
}
