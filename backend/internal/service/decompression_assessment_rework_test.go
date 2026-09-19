package service_test

import (
	"context"
	"errors"
	"testing"

	"commercial-diving-decompression-control/backend/internal/audit"
	"commercial-diving-decompression-control/backend/internal/auth"
	"commercial-diving-decompression-control/backend/internal/config"
	"commercial-diving-decompression-control/backend/internal/constants"
	"commercial-diving-decompression-control/backend/internal/database"
	"commercial-diving-decompression-control/backend/internal/dto"
	"commercial-diving-decompression-control/backend/internal/model"
	"commercial-diving-decompression-control/backend/internal/repository"
	"commercial-diving-decompression-control/backend/internal/service"
	"commercial-diving-decompression-control/backend/internal/util"
	"gorm.io/gorm"
)

type reworkFixture struct {
	db          *gorm.DB
	plan        model.DivePlan
	planner     audit.Entry
	supervisor  audit.Entry
	service     *service.DecompressionAssessmentService
	plans       *repository.DivePlanRepository
	segments    *repository.ExposureSegmentRepository
	assessments *repository.DecompressionAssessmentRepository
}

func setupReworkFixture(t *testing.T) reworkFixture {
	t.Helper()
	cfg := config.Config{DBDriver: "sqlite", DBDSN: "file:rework-loop-test?mode=memory&cache=shared", DBAutoMigrate: true, JWTSecret: "rework-loop-test-secret-at-least-32-bytes-long", ModelVersion: "training-compartment-v1", MaxSegments: 24, RateLimitPerMinute: 100}
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	var plannerUser, supervisorUser auth.User
	if err := db.Where("username = ?", "planner").First(&plannerUser).Error; err != nil {
		t.Fatalf("load planner seed user: %v", err)
	}
	if err := db.Where("username = ?", "supervisor").First(&supervisorUser).Error; err != nil {
		t.Fatalf("load supervisor seed user: %v", err)
	}
	auditRepo := audit.NewRepository(db)
	planRepo := repository.NewDivePlanRepository(db, auditRepo)
	segmentRepo := repository.NewExposureSegmentRepository(db, auditRepo)
	assessmentRepo := repository.NewDecompressionAssessmentRepository(db, auditRepo)
	profileRepo := repository.NewDiverProfileRepository(db, auditRepo)
	assessmentService := service.NewDecompressionAssessmentService(assessmentRepo, planRepo, profileRepo, segmentRepo, cfg.ModelVersion, cfg.MaxSegments)

	plan := model.DivePlan{}
	if err := db.Where("plan_code = ?", "TRAIN-30A").First(&plan).Error; err != nil {
		t.Fatalf("load seeded plan: %v", err)
	}
	return reworkFixture{
		db: db, plan: plan,
		planner:     audit.Entry{RequestID: "test-planner", ActorID: plannerUser.ID, ActorUsername: plannerUser.Username},
		supervisor:  audit.Entry{RequestID: "test-supervisor", ActorID: supervisorUser.ID, ActorUsername: supervisorUser.Username},
		service:     assessmentService,
		plans:       planRepo,
		segments:    segmentRepo,
		assessments: assessmentRepo,
	}
}

func actorAt(f reworkFixture, actor audit.Entry, requestID string) audit.Entry {
	actor.RequestID = requestID
	return actor
}

func requireAppErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var appErr *util.AppError
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", code)
	}
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.Code != code {
		t.Fatalf("expected error code %s, got %s (%s)", code, appErr.Code, appErr.Message)
	}
}

func auditCount(t *testing.T, f reworkFixture) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(&audit.Event{}).Count(&count).Error; err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	return count
}

// TestAssessmentReworkClosedLoop walks the complete supervisor return,
// planner segment edit and rerun cycle and asserts every invariant.
func TestAssessmentReworkClosedLoop(t *testing.T) {
	f := setupReworkFixture(t)
	ctx := context.Background()

	// Initial assessment on the seeded draft plan (version 1).
	first, err := f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: f.plan.Version}, actorAt(f, f.planner, "run-1"))
	if err != nil {
		t.Fatalf("first model run: %v", err)
	}
	if first.Revision != 1 {
		t.Fatalf("first assessment revision = %d, want 1", first.Revision)
	}
	planAfterRun1, err := f.plans.Get(ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if planAfterRun1.PlanStatus != constants.PlanModeled || planAfterRun1.Version != 2 {
		t.Fatalf("plan after run = %s v%d, want modeled v2", planAfterRun1.PlanStatus, planAfterRun1.Version)
	}

	// Planner submits for supervisor review.
	first, err = f.service.Submit(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: 2, Reason: "Planner submits revision one for human review."}, actorAt(f, f.planner, "submit-1"))
	if err != nil {
		t.Fatalf("submit first assessment: %v", err)
	}
	if first.AssessmentStatus != string(constants.PlanPendingReview) {
		t.Fatalf("assessment status after submit = %s", first.AssessmentStatus)
	}

	// Returning without a reason is rejected before any state change.
	auditBeforeRejectedReturn := auditCount(t, f)
	_, err = f.service.ReturnForRework(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanDraft, Version: 3, Reason: "  "}, actorAt(f, f.supervisor, "return-rejected"))
	requireAppErrorCode(t, err, "RETURN_REASON_REQUIRED")
	if got := auditCount(t, f); got != auditBeforeRejectedReturn {
		t.Fatalf("rejected return wrote %d audit events, want 0", got-auditBeforeRejectedReturn)
	}

	// Supervisor returns the pending assessment to draft with a reason.
	returned, err := f.service.ReturnForRework(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanDraft, Version: 3, Reason: "Bottom phase assumption disagrees with the training risk evidence; adjust exposure."}, actorAt(f, f.supervisor, "return-1"))
	if err != nil {
		t.Fatalf("return first assessment: %v", err)
	}
	if returned.AssessmentStatus != constants.AssessmentSuperseded || returned.ReturnedReason == "" || returned.ReturnedAt == nil || returned.ReturnedBy == nil {
		t.Fatalf("returned assessment not marked superseded with reason: %+v", returned)
	}
	returnedPlan, err := f.plans.Get(ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("reload returned plan: %v", err)
	}
	if returnedPlan.PlanStatus != constants.PlanDraft || returnedPlan.Version != 4 || returnedPlan.RerunGateVersion != 4 {
		t.Fatalf("plan after return = %s v%d gate v%d, want draft v4 gate v4", returnedPlan.PlanStatus, returnedPlan.Version, returnedPlan.RerunGateVersion)
	}

	// The old assessment can no longer be submitted or approved.
	_, err = f.service.Submit(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: 4, Reason: "Attempt to resubmit the superseded revision."}, actorAt(f, f.planner, "submit-stale"))
	requireAppErrorCode(t, err, "ASSESSMENT_SUPERSEDED")
	_, err = f.service.Approve(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanApprovedTraining, Version: 4, Reason: "Attempt to approve the superseded revision."}, actorAt(f, f.supervisor, "approve-stale"))
	requireAppErrorCode(t, err, "ASSESSMENT_SUPERSEDED")

	// Rerun is blocked until an exposure segment changes.
	_, err = f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: 4}, actorAt(f, f.planner, "run-blocked"))
	requireAppErrorCode(t, err, "SEGMENT_CHANGE_REQUIRED")

	// A stale optimistic version also loses: planner cannot rerun against v3.
	_, err = f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: 3}, actorAt(f, f.planner, "run-stale"))
	requireAppErrorCode(t, err, "PLAN_VERSION_CONFLICT")

	// Planner changes an exposure segment (lengthens the bottom phase); the
	// segment transaction advances the plan input version past the gate.
	var segments []model.ExposureSegment
	if err := f.db.Where("plan_id = ?", f.plan.ID).Order("sequence_no ASC").Find(&segments).Error; err != nil {
		t.Fatalf("load segments: %v", err)
	}
	bottom := segments[1]
	editActor := actorAt(f, f.planner, "segment-edit")
	editActor.Action = "exposure_segment.update"
	editActor.EntityType = "exposure_segment"
	editActor.EntityID = bottom.ID
	if err := f.segments.Update(ctx, bottom, returnedPlan.Version, map[string]any{"duration_min": 26.0}, editActor); err != nil {
		t.Fatalf("planner segment edit: %v", err)
	}
	editedPlan, err := f.plans.Get(ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("reload edited plan: %v", err)
	}
	if editedPlan.Version != 5 {
		t.Fatalf("plan version after segment edit = %d, want 5", editedPlan.Version)
	}

	// Rerun still rejected for a client holding the pre-edit version.
	_, err = f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: 4}, actorAt(f, f.planner, "run-blocked-2"))
	requireAppErrorCode(t, err, "PLAN_VERSION_CONFLICT")

	// New assessment can run; it becomes revision 2 and the plan is modeled
	// again, still pending review workflow rather than approved.
	second, err := f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: 5}, actorAt(f, f.planner, "run-2"))
	if err != nil {
		t.Fatalf("second model run: %v", err)
	}
	if second.Revision != 2 || second.AssessmentStatus != constants.AssessmentModeled {
		t.Fatalf("second assessment = revision %d status %s, want revision 2 modeled", second.Revision, second.AssessmentStatus)
	}
	oldAssessment, err := f.assessments.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("reload first assessment: %v", err)
	}
	if oldAssessment.AssessmentStatus != constants.AssessmentSuperseded || oldAssessment.SupersededByID == nil || *oldAssessment.SupersededByID != second.ID {
		t.Fatalf("first assessment not linked to replacement: status=%s superseded_by=%v", oldAssessment.AssessmentStatus, oldAssessment.SupersededByID)
	}
	if oldAssessment.InputSnapshotJSON == "" || oldAssessment.ReturnedReason == "" {
		t.Fatalf("superseded assessment lost its snapshot or return reason")
	}
	rerunPlan, err := f.plans.Get(ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("reload plan after rerun: %v", err)
	}
	if rerunPlan.PlanStatus != constants.PlanModeled || rerunPlan.RerunGateVersion != 0 {
		t.Fatalf("plan after rerun = %s gate v%d, want modeled gate cleared", rerunPlan.PlanStatus, rerunPlan.RerunGateVersion)
	}

	// Submit revision 2; the plan returns to the supervisor review queue.
	second, err = f.service.Submit(ctx, second.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: 6, Reason: "Revision two addresses the returned bottom-phase evidence."}, actorAt(f, f.planner, "submit-2"))
	if err != nil {
		t.Fatalf("submit second assessment: %v", err)
	}
	if second.AssessmentStatus != string(constants.PlanPendingReview) {
		t.Fatalf("second assessment status = %s, want pending review", second.AssessmentStatus)
	}

	// Approve the current revision.
	approved, err := f.service.Approve(ctx, second.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanApprovedTraining, Version: 7, Reason: "Approved for training comparison only; no operational clearance."}, actorAt(f, f.supervisor, "approve-1"))
	if err != nil {
		t.Fatalf("approve second assessment: %v", err)
	}
	if approved.AssessmentStatus != string(constants.PlanApprovedTraining) {
		t.Fatalf("approved assessment status = %s", approved.AssessmentStatus)
	}
	finalPlan, err := f.plans.Get(ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("reload final plan: %v", err)
	}
	if finalPlan.PlanStatus != constants.PlanApprovedTraining || finalPlan.ReviewedBy == nil || *finalPlan.ReviewedBy != f.supervisor.ActorID {
		t.Fatalf("final plan = %s reviewed_by=%v, want approved by supervisor", finalPlan.PlanStatus, finalPlan.ReviewedBy)
	}

	// Both revisions remain queryable; the old snapshot is preserved.
	list, total, err := f.service.List(ctx, f.plan.ID, "", 1, 50)
	if err != nil {
		t.Fatalf("list assessments: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("assessment history = %d items (total %d), want 2 preserved revisions", len(list), total)
	}
	if list[0].Revision != 2 || list[1].Revision != 1 {
		t.Fatalf("revision ordering = %d then %d, want 2 then 1", list[0].Revision, list[1].Revision)
	}
}

// TestAssessmentReturnSingleWinner proves a second concurrent-style return
// cannot mutate anything after the first one wins, and that failure rolls the
// whole operation back (no partial plan/assessment/audit changes).
func TestAssessmentReturnSingleWinner(t *testing.T) {
	f := setupReworkFixture(t)
	ctx := context.Background()
	first, err := f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: f.plan.Version}, actorAt(f, f.planner, "run-a"))
	if err != nil {
		t.Fatalf("model run: %v", err)
	}
	if _, err := f.service.Submit(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: 2, Reason: "Submit for the single-winner return test."}, actorAt(f, f.planner, "submit-a")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	auditBefore := auditCount(t, f)
	if _, err := f.service.ReturnForRework(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanDraft, Version: 3, Reason: "First supervisor return wins the closed loop."}, actorAt(f, f.supervisor, "return-a")); err != nil {
		t.Fatalf("first return: %v", err)
	}
	if got := auditCount(t, f) - auditBefore; got != 2 {
		t.Fatalf("winning return wrote %d audit events, want 2 (assessment + plan)", got)
	}

	// A second return carrying the same stale version must fail and change
	// nothing: plan stays draft, assessment stays superseded, no audit rows.
	auditBeforeSecond := auditCount(t, f)
	_, err = f.service.ReturnForRework(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanDraft, Version: 3, Reason: "Duplicate return must lose and roll back entirely."}, actorAt(f, f.supervisor, "return-b"))
	requireAppErrorCode(t, err, "PLAN_VERSION_CONFLICT")
	if got := auditCount(t, f); got != auditBeforeSecond {
		t.Fatalf("losing return wrote %d audit events, want full rollback", got-auditBeforeSecond)
	}
	plan, err := f.plans.Get(ctx, f.plan.ID)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if plan.PlanStatus != constants.PlanDraft || plan.Version != 4 || plan.RerunGateVersion != 4 {
		t.Fatalf("plan after losing return = %s v%d gate v%d, want draft v4 gate v4", plan.PlanStatus, plan.Version, plan.RerunGateVersion)
	}
	stored, err := f.assessments.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("reload assessment: %v", err)
	}
	if stored.AssessmentStatus != constants.AssessmentSuperseded {
		t.Fatalf("assessment status after losing return = %s, want superseded", stored.AssessmentStatus)
	}
}

// TestAssessmentReturnRequiresPendingReview guards the state machine: modeled
// and approved assessments cannot be returned.
func TestAssessmentReturnRequiresPendingReview(t *testing.T) {
	f := setupReworkFixture(t)
	ctx := context.Background()
	first, err := f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: f.plan.Version}, actorAt(f, f.planner, "run-modeled"))
	if err != nil {
		t.Fatalf("model run: %v", err)
	}
	_, err = f.service.ReturnForRework(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanDraft, Version: 2, Reason: "Supervisor cannot return a modeled-only assessment."}, actorAt(f, f.supervisor, "return-modeled"))
	requireAppErrorCode(t, err, "INVALID_PLAN_TRANSITION")

	if _, err := f.service.Submit(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: 2, Reason: "Move to review so it can be approved for the guard test."}, actorAt(f, f.planner, "submit-modeled")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := f.service.Approve(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanApprovedTraining, Version: 3, Reason: "Approve so the assessment becomes terminal."}, actorAt(f, f.supervisor, "approve-terminal")); err != nil {
		t.Fatalf("approve: %v", err)
	}
	_, err = f.service.ReturnForRework(ctx, first.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanDraft, Version: 4, Reason: "Supervisor cannot return an approved assessment."}, actorAt(f, f.supervisor, "return-approved"))
	requireAppErrorCode(t, err, "INVALID_PLAN_TRANSITION")

	// The failed attempts changed nothing: one approved assessment, no return
	// metadata, and an approved plan.
	stored, err := f.assessments.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("reload assessment: %v", err)
	}
	if stored.AssessmentStatus != constants.AssessmentApprovedTraining || stored.ReturnedAt != nil {
		t.Fatalf("assessment after failed returns = %s returned_at=%v, want approved with no return metadata", stored.AssessmentStatus, stored.ReturnedAt)
	}
}
