package constants

import "testing"

func TestPlanTransitions(t *testing.T) {
	tests := []struct {
		name string
		from PlanStatus
		to   PlanStatus
		want bool
	}{
		{"model draft", PlanDraft, PlanModeled, true},
		{"submit modeled", PlanModeled, PlanPendingReview, true},
		{"approve review", PlanPendingReview, PlanApprovedTraining, true},
		{"return review to draft", PlanPendingReview, PlanDraft, true},
		{"archive approval", PlanApprovedTraining, PlanArchived, true},
		{"cannot skip review", PlanModeled, PlanApprovedTraining, false},
		{"cannot return a modeled plan", PlanModeled, PlanDraft, false},
		{"archive terminal", PlanArchived, PlanDraft, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanTransitionPlan(test.from, test.to); got != test.want {
				t.Fatalf("transition %s -> %s = %t, want %t", test.from, test.to, got, test.want)
			}
		})
	}
}

func TestAssessmentTransitions(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   PlanStatus
		want bool
	}{
		{"submit modeled assessment", AssessmentModeled, PlanPendingReview, true},
		{"approve pending assessment", AssessmentPendingReview, PlanApprovedTraining, true},
		{"superseded cannot submit", AssessmentSuperseded, PlanPendingReview, false},
		{"superseded cannot approve", AssessmentSuperseded, PlanApprovedTraining, false},
		{"approved cannot resubmit", AssessmentApprovedTraining, PlanPendingReview, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanTransitionAssessment(test.from, test.to); got != test.want {
				t.Fatalf("assessment transition %s -> %s = %t, want %t", test.from, test.to, got, test.want)
			}
		})
	}
}
