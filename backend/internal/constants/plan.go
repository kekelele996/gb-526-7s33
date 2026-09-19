package constants

type PlanStatus string

const (
	PlanDraft            PlanStatus = "draft"
	PlanModeled          PlanStatus = "modeled"
	PlanPendingReview    PlanStatus = "pending_supervisor_review"
	PlanApprovedTraining PlanStatus = "approved_for_training"
	PlanArchived         PlanStatus = "archived"
)

// AssessmentStatus mirrors the review state of an immutable assessment.
// AssessmentSuperseded marks an assessment whose input was returned to the
// planner for rework; its snapshot and results stay preserved and read-only.
const (
	AssessmentModeled          = "modeled"
	AssessmentPendingReview    = "pending_supervisor_review"
	AssessmentApprovedTraining = "approved_for_training"
	AssessmentArchived         = "archived"
	AssessmentSuperseded       = "superseded"
)

var planTransitions = map[PlanStatus]map[PlanStatus]bool{
	PlanDraft:            {PlanModeled: true},
	PlanModeled:          {PlanPendingReview: true},
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

// CanTransitionAssessment reports whether an immutable assessment may leave
// its current review state. A superseded assessment is terminal: it can never
// be submitted or approved again.
func CanTransitionAssessment(from string, to PlanStatus) bool {
	if from == AssessmentSuperseded {
		return false
	}
	return CanTransitionPlan(PlanStatus(from), to)
}
