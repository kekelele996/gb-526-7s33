package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"commercial-diving-decompression-control/backend/internal/audit"
	"commercial-diving-decompression-control/backend/internal/constants"
	"commercial-diving-decompression-control/backend/internal/dto"
	"commercial-diving-decompression-control/backend/internal/model"
	"commercial-diving-decompression-control/backend/internal/repository"
	"commercial-diving-decompression-control/backend/internal/util"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type assessmentLoopFixture struct {
	service    *DecompressionAssessmentService
	segments   *repository.ExposureSegmentRepository
	db         *gorm.DB
	plan       model.DivePlan
	assessment model.DecompressionAssessment
	actor      audit.Entry
}

func newAssessmentLoopFixture(t *testing.T) *assessmentLoopFixture {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.DiverProfile{}, &model.DivePlan{}, &model.ExposureSegment{}, &model.DecompressionAssessment{}, &audit.Event{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test database handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	auditRepo := audit.NewRepository(db)
	assessmentRepo := repository.NewDecompressionAssessmentRepository(db, auditRepo)
	planRepo := repository.NewDivePlanRepository(db, auditRepo)
	profileRepo := repository.NewDiverProfileRepository(db, auditRepo)
	segmentRepo := repository.NewExposureSegmentRepository(db, auditRepo)
	service := NewDecompressionAssessmentService(assessmentRepo, planRepo, profileRepo, segmentRepo, "loop-v1", 8)

	profile := model.DiverProfile{ProfileCode: "LOOP-1", DisplayName: "Loop Profile", QualificationLevel: "commercial", DefaultO2Fraction: 0.21, ProfileStatus: "active", Version: 1}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	plan := model.DivePlan{PlanCode: "LOOP-PLAN", DiverProfileID: profile.ID, WorksitePressureBar: 1, BreathingMixJSON: `{"o2":0.21,"he":0,"n2":0.79}`, PlanStatus: constants.PlanPendingReview, CreatedBy: 1, Version: 4, PlannedAt: time.Now().UTC()}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	segment := model.ExposureSegment{PlanID: plan.ID, SequenceNo: 1, DepthM: 20, DurationMin: 10, GasMixJSON: `{"o2":0.21,"he":0,"n2":0.79}`, SegmentType: "bottom"}
	if err := db.Create(&segment).Error; err != nil {
		t.Fatalf("seed segment: %v", err)
	}
	assessment := model.DecompressionAssessment{PlanID: plan.ID, AssessmentStatus: string(constants.PlanPendingReview), AlgorithmVersion: "loop-v1", InputSnapshotJSON: `{"plan":{"version":4}}`, CompartmentLoadsJSON: `[]`, RiskFlagsJSON: `[]`, HighestRiskBand: "informational", AssumptionsJSON: `{}`}
	if err := db.Create(&assessment).Error; err != nil {
		t.Fatalf("seed assessment: %v", err)
	}
	return &assessmentLoopFixture{
		service: service, segments: segmentRepo, db: db, plan: plan, assessment: assessment,
		actor: audit.Entry{RequestID: "req-loop", ActorID: 2, ActorUsername: "supervisor"},
	}
}

func (f *assessmentLoopFixture) reloadPlan(t *testing.T) model.DivePlan {
	t.Helper()
	var plan model.DivePlan
	if err := f.db.First(&plan, f.plan.ID).Error; err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	return plan
}

func wantAppError(t *testing.T, err error, code string) {
	t.Helper()
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestReturnRerunClosedLoop(t *testing.T) {
	f := newAssessmentLoopFixture(t)
	ctx := context.Background()

	// Reason is mandatory for a supervisor return.
	if _, err := f.service.Return(ctx, f.assessment.ID, dto.ReturnAssessmentRequest{Version: 4, Reason: " "}, f.actor); err == nil {
		t.Fatal("return without a reason must be rejected")
	}

	// Supervisor returns the pending assessment to draft with a reason.
	returned, err := f.service.Return(ctx, f.assessment.ID, dto.ReturnAssessmentRequest{Version: 4, Reason: "bottom time assumption must be corrected"}, f.actor)
	if err != nil {
		t.Fatalf("supervisor return: %v", err)
	}
	if returned.AssessmentStatus != constants.AssessmentSuperseded || returned.ReturnReason != "bottom time assumption must be corrected" {
		t.Fatalf("unexpected returned assessment: %+v", returned)
	}
	plan := f.reloadPlan(t)
	if plan.PlanStatus != constants.PlanDraft || plan.Version != 5 {
		t.Fatalf("plan = %s v%d, want draft v5", plan.PlanStatus, plan.Version)
	}

	// The superseded assessment can no longer be submitted or approved.
	if _, err := f.service.Submit(ctx, f.assessment.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: 5, Reason: "retry old"}, f.actor); err == nil {
		t.Fatal("superseded assessment must not be submittable")
	}
	if _, err := f.service.Approve(ctx, f.assessment.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanApprovedTraining, Version: 5, Reason: "retry old"}, f.actor); err == nil {
		t.Fatal("superseded assessment must not be approvable")
	}

	// Re-running without touching the exposure segments is rejected.
	if _, err := f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: 5}, f.actor); err != nil {
		wantAppError(t, err, "SEGMENTS_UNCHANGED_AFTER_RETURN")
	} else {
		t.Fatal("re-run without segment changes must be rejected")
	}

	// Planner modifies the exposure segments, advancing the input version.
	segment := model.ExposureSegment{PlanID: f.plan.ID, SequenceNo: 2, DepthM: 10, DurationMin: 5, AscentRateMMin: 9, GasMixJSON: `{"o2":0.21,"he":0,"n2":0.79}`, SegmentType: "ascent"}
	if err := f.segments.Create(ctx, &segment, 5, audit.Entry{RequestID: "req-loop", ActorID: 3, ActorUsername: "planner", Action: "exposure_segment.create", EntityType: "exposure_segment"}); err != nil {
		t.Fatalf("planner segment edit: %v", err)
	}
	plan = f.reloadPlan(t)
	if plan.Version != 6 {
		t.Fatalf("plan version after segment edit = %d, want 6", plan.Version)
	}

	// Re-run now succeeds and links the version chain.
	replacement, err := f.service.Run(ctx, f.plan.ID, dto.RunAssessmentRequest{PlanVersion: 6}, f.actor)
	if err != nil {
		t.Fatalf("re-run after segment edit: %v", err)
	}
	if replacement.SupersedesID == nil || *replacement.SupersedesID != f.assessment.ID {
		t.Fatalf("replacement supersedes = %v, want %d", replacement.SupersedesID, f.assessment.ID)
	}
	reloadedOld, err := f.service.Get(ctx, f.assessment.ID)
	if err != nil {
		t.Fatalf("reload superseded assessment: %v", err)
	}
	if reloadedOld.SupersededByID == nil || *reloadedOld.SupersededByID != replacement.ID {
		t.Fatalf("old superseded_by = %v, want %d", reloadedOld.SupersededByID, replacement.ID)
	}
	if reloadedOld.InputSnapshot.Plan.Version != 0 && reloadedOld.ReturnReason == "" {
		t.Fatal("superseded snapshot and reason must be retained")
	}

	// The plan returns to pending review only through the new assessment.
	plan = f.reloadPlan(t)
	if _, err := f.service.Submit(ctx, replacement.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: plan.Version, Reason: "resubmit corrected inputs"}, f.actor); err != nil {
		t.Fatalf("submit replacement: %v", err)
	}
	plan = f.reloadPlan(t)
	if plan.PlanStatus != constants.PlanPendingReview {
		t.Fatalf("plan = %s, want pending_supervisor_review", plan.PlanStatus)
	}

	// The old superseded assessment stays locked out of the review track.
	if _, err := f.service.Submit(ctx, f.assessment.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanPendingReview, Version: plan.Version, Reason: "retry old"}, f.actor); err != nil {
		wantAppError(t, err, "INVALID_PLAN_TRANSITION")
	} else {
		t.Fatal("superseded assessment must stay unsubmittable after replacement")
	}
	if _, err := f.service.Approve(ctx, f.assessment.ID, dto.TransitionPlanRequest{TargetStatus: constants.PlanApprovedTraining, Version: plan.Version, Reason: "retry old"}, f.actor); err != nil {
		wantAppError(t, err, "ASSESSMENT_STATE_CONFLICT")
	} else {
		t.Fatal("superseded assessment must stay unapprovable after replacement")
	}
}

func TestReturnRejectsNonPendingStates(t *testing.T) {
	f := newAssessmentLoopFixture(t)
	ctx := context.Background()

	// A second return on the same plan version is a concurrency conflict.
	if _, err := f.service.Return(ctx, f.assessment.ID, dto.ReturnAssessmentRequest{Version: 4, Reason: "first return"}, f.actor); err != nil {
		t.Fatalf("first return: %v", err)
	}
	if _, err := f.service.Return(ctx, f.assessment.ID, dto.ReturnAssessmentRequest{Version: 4, Reason: "stale double return"}, f.actor); err != nil {
		wantAppError(t, err, "PLAN_VERSION_CONFLICT")
	} else {
		t.Fatal("stale double return must be rejected")
	}

	// A draft plan cannot be returned again.
	plan := f.reloadPlan(t)
	if _, err := f.service.Return(ctx, f.assessment.ID, dto.ReturnAssessmentRequest{Version: plan.Version, Reason: "return again"}, f.actor); err != nil {
		wantAppError(t, err, "INVALID_PLAN_TRANSITION")
	} else {
		t.Fatal("returning a draft plan must be rejected")
	}
}
