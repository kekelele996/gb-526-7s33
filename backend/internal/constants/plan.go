package constants

type PlanStatus string

const (
	PlanDraft            PlanStatus = "draft"
	PlanModeled          PlanStatus = "modeled"
	PlanPendingReview    PlanStatus = "pending_supervisor_review"
	PlanApprovedTraining PlanStatus = "approved_for_training"
	PlanArchived         PlanStatus = "archived"
)

// AssessmentSuperseded marks an immutable assessment whose plan was returned
// to draft by a supervisor; the row and its input snapshot are preserved but
// can no longer be submitted or approved.
const AssessmentSuperseded = "superseded"

var planTransitions = map[PlanStatus]map[PlanStatus]bool{
	PlanDraft:            {PlanModeled: true},
	PlanModeled:          {PlanDraft: true, PlanPendingReview: true},
	PlanPendingReview:    {PlanDraft: true, PlanApprovedTraining: true},
	PlanApprovedTraining: {PlanArchived: true},
	PlanArchived:         {},
}

func ValidPlanStatus(status PlanStatus) bool {
	_, ok := planTransitions[status]
	return ok
}

func CanTransitionPlan(from, to PlanStatus) bool {
	return planTransitions[from][to]
}

func PlanStatuses() []PlanStatus {
	return []PlanStatus{PlanDraft, PlanModeled, PlanPendingReview, PlanApprovedTraining, PlanArchived}
}
