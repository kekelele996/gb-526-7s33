package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"commercial-diving-decompression-control/backend/internal/audit"
	"commercial-diving-decompression-control/backend/internal/constants"
	"commercial-diving-decompression-control/backend/internal/model"
	"commercial-diving-decompression-control/backend/internal/util"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newAssessmentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.DivePlan{}, &model.DecompressionAssessment{}, &audit.Event{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test database handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func seedPendingReview(t *testing.T, db *gorm.DB, planVersion uint) (model.DivePlan, model.DecompressionAssessment) {
	t.Helper()
	plan := model.DivePlan{PlanCode: "RET-01", DiverProfileID: 1, WorksitePressureBar: 1, BreathingMixJSON: `{"o2":0.21,"he":0,"n2":0.79}`, PlanStatus: constants.PlanPendingReview, CreatedBy: 1, Version: planVersion, PlannedAt: time.Now().UTC()}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	assessment := model.DecompressionAssessment{PlanID: plan.ID, AssessmentStatus: string(constants.PlanPendingReview), AlgorithmVersion: "test-v1", InputSnapshotJSON: `{"plan":{}}`, CompartmentLoadsJSON: `[]`, RiskFlagsJSON: `[]`, HighestRiskBand: "informational", AssumptionsJSON: `{}`}
	if err := db.Create(&assessment).Error; err != nil {
		t.Fatalf("seed assessment: %v", err)
	}
	return plan, assessment
}

func testEntry(action string) audit.Entry {
	return audit.Entry{RequestID: "req-test", ActorID: 7, ActorUsername: "supervisor", Action: action, EntityType: "decompression_assessment"}
}

func countAuditEvents(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var total int64
	if err := db.Model(&audit.Event{}).Count(&total).Error; err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	return total
}

func TestReturnForRecalculationLoop(t *testing.T) {
	db := newAssessmentTestDB(t)
	repo := NewDecompressionAssessmentRepository(db, audit.NewRepository(db))
	ctx := context.Background()
	plan, assessment := seedPendingReview(t, db, 5)

	if err := repo.ReturnForRecalculation(ctx, plan, assessment, "bottom segment gas assumption needs revision", 7, testEntry("decompression_assessment.return")); err != nil {
		t.Fatalf("return for recalculation: %v", err)
	}

	var updatedPlan model.DivePlan
	if err := db.First(&updatedPlan, plan.ID).Error; err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if updatedPlan.PlanStatus != constants.PlanDraft || updatedPlan.Version != 6 {
		t.Fatalf("plan = %s v%d, want draft v6", updatedPlan.PlanStatus, updatedPlan.Version)
	}

	var returned model.DecompressionAssessment
	if err := db.First(&returned, assessment.ID).Error; err != nil {
		t.Fatalf("reload assessment: %v", err)
	}
	if returned.AssessmentStatus != constants.AssessmentSuperseded {
		t.Fatalf("assessment status = %s, want superseded", returned.AssessmentStatus)
	}
	if returned.ReturnReason != "bottom segment gas assumption needs revision" {
		t.Fatalf("return reason = %q", returned.ReturnReason)
	}
	if returned.ReturnedBy == nil || *returned.ReturnedBy != 7 || returned.ReturnedAt == nil {
		t.Fatalf("return metadata incomplete: %+v", returned)
	}
	if returned.ReturnPlanVersion != 6 {
		t.Fatalf("return plan version = %d, want 6", returned.ReturnPlanVersion)
	}
	if returned.InputSnapshotJSON != `{"plan":{}}` {
		t.Fatalf("input snapshot must be retained, got %q", returned.InputSnapshotJSON)
	}
	if got := countAuditEvents(t, db); got != 2 {
		t.Fatalf("audit events = %d, want 2", got)
	}

	// Planner edits segments, which advances the plan input version.
	if err := db.Model(&model.DivePlan{}).Where("id = ?", plan.ID).Update("version", 7).Error; err != nil {
		t.Fatalf("bump plan version: %v", err)
	}
	updatedPlan.Version = 7
	replacement := model.DecompressionAssessment{PlanID: plan.ID, AssessmentStatus: string(constants.PlanModeled), AlgorithmVersion: "test-v1", InputSnapshotJSON: `{"plan":{"v":7}}`, CompartmentLoadsJSON: `[]`, RiskFlagsJSON: `[]`, HighestRiskBand: "informational", AssumptionsJSON: `{}`}
	if err := repo.CreateModeled(ctx, updatedPlan, &replacement, &returned, testEntry("decompression_assessment.run")); err != nil {
		t.Fatalf("create replacement assessment: %v", err)
	}

	var reloadedOld model.DecompressionAssessment
	if err := db.First(&reloadedOld, assessment.ID).Error; err != nil {
		t.Fatalf("reload superseded assessment: %v", err)
	}
	if reloadedOld.SupersededByID == nil || *reloadedOld.SupersededByID != replacement.ID {
		t.Fatalf("old assessment superseded_by = %v, want %d", reloadedOld.SupersededByID, replacement.ID)
	}
	if replacement.SupersedesID == nil || *replacement.SupersedesID != assessment.ID {
		t.Fatalf("replacement supersedes = %v, want %d", replacement.SupersedesID, assessment.ID)
	}
	var finalPlan model.DivePlan
	if err := db.First(&finalPlan, plan.ID).Error; err != nil {
		t.Fatalf("reload final plan: %v", err)
	}
	if finalPlan.PlanStatus != constants.PlanModeled || finalPlan.Version != 8 {
		t.Fatalf("final plan = %s v%d, want modeled v8", finalPlan.PlanStatus, finalPlan.Version)
	}
}

func TestReturnForRecalculationConcurrency(t *testing.T) {
	db := newAssessmentTestDB(t)
	repo := NewDecompressionAssessmentRepository(db, audit.NewRepository(db))
	ctx := context.Background()
	plan, assessment := seedPendingReview(t, db, 3)

	var wait sync.WaitGroup
	results := make([]error, 2)
	for index := range results {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			results[i] = repo.ReturnForRecalculation(ctx, plan, assessment, "concurrent return attempt", 7, testEntry("decompression_assessment.return"))
		}(index)
	}
	wait.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		var appErr *util.AppError
		if !errors.As(err, &appErr) || appErr.Status != 409 {
			t.Fatalf("losing return must be a conflict, got %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent returns succeeded = %d, want exactly 1", succeeded)
	}

	var finalPlan model.DivePlan
	if err := db.First(&finalPlan, plan.ID).Error; err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if finalPlan.PlanStatus != constants.PlanDraft || finalPlan.Version != 4 {
		t.Fatalf("plan = %s v%d, want a single transition to draft v4", finalPlan.PlanStatus, finalPlan.Version)
	}
	var finalAssessment model.DecompressionAssessment
	if err := db.First(&finalAssessment, assessment.ID).Error; err != nil {
		t.Fatalf("reload assessment: %v", err)
	}
	if finalAssessment.AssessmentStatus != constants.AssessmentSuperseded || finalAssessment.ReturnPlanVersion != 4 {
		t.Fatalf("assessment = %s return_version=%d, want superseded v4", finalAssessment.AssessmentStatus, finalAssessment.ReturnPlanVersion)
	}
	if got := countAuditEvents(t, db); got != 2 {
		t.Fatalf("audit events = %d, want exactly 2 from the single winner", got)
	}
}

func TestReturnFailureLeavesStateUntouched(t *testing.T) {
	db := newAssessmentTestDB(t)
	repo := NewDecompressionAssessmentRepository(db, audit.NewRepository(db))
	ctx := context.Background()
	plan, assessment := seedPendingReview(t, db, 3)
	plan.Version = 99 // stale version observed by the caller

	err := repo.ReturnForRecalculation(ctx, plan, assessment, "stale return attempt", 7, testEntry("decompression_assessment.return"))
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != "PLAN_VERSION_CONFLICT" {
		t.Fatalf("expected PLAN_VERSION_CONFLICT, got %v", err)
	}

	var finalPlan model.DivePlan
	if err := db.First(&finalPlan, plan.ID).Error; err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if finalPlan.PlanStatus != constants.PlanPendingReview || finalPlan.Version != 3 {
		t.Fatalf("plan mutated on failure: %s v%d", finalPlan.PlanStatus, finalPlan.Version)
	}
	var finalAssessment model.DecompressionAssessment
	if err := db.First(&finalAssessment, assessment.ID).Error; err != nil {
		t.Fatalf("reload assessment: %v", err)
	}
	if finalAssessment.AssessmentStatus != string(constants.PlanPendingReview) || finalAssessment.ReturnReason != "" || finalAssessment.ReturnPlanVersion != 0 {
		t.Fatalf("assessment mutated on failure: %+v", finalAssessment)
	}
	if got := countAuditEvents(t, db); got != 0 {
		t.Fatalf("audit events = %d, want 0 after failed return", got)
	}
}

func TestCreateModeledSupersedeConflictRollsBack(t *testing.T) {
	db := newAssessmentTestDB(t)
	repo := NewDecompressionAssessmentRepository(db, audit.NewRepository(db))
	ctx := context.Background()
	plan, assessment := seedPendingReview(t, db, 5)
	if err := repo.ReturnForRecalculation(ctx, plan, assessment, "needs rework", 7, testEntry("decompression_assessment.return")); err != nil {
		t.Fatalf("return for recalculation: %v", err)
	}
	auditBefore := countAuditEvents(t, db)

	var returned model.DecompressionAssessment
	if err := db.First(&returned, assessment.ID).Error; err != nil {
		t.Fatalf("reload assessment: %v", err)
	}
	// Simulate the returned assessment already being replaced by another run.
	if err := db.Model(&model.DecompressionAssessment{}).Where("id = ?", assessment.ID).Update("superseded_by_id", 4242).Error; err != nil {
		t.Fatalf("mark assessment replaced: %v", err)
	}

	draftPlan := model.DivePlan{}
	if err := db.First(&draftPlan, plan.ID).Error; err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	replacement := model.DecompressionAssessment{PlanID: plan.ID, AssessmentStatus: string(constants.PlanModeled), AlgorithmVersion: "test-v1", InputSnapshotJSON: `{}`, CompartmentLoadsJSON: `[]`, RiskFlagsJSON: `[]`, HighestRiskBand: "informational", AssumptionsJSON: `{}`}
	err := repo.CreateModeled(ctx, draftPlan, &replacement, &returned, testEntry("decompression_assessment.run"))
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != "ASSESSMENT_STATE_CONFLICT" {
		t.Fatalf("expected ASSESSMENT_STATE_CONFLICT, got %v", err)
	}

	var finalPlan model.DivePlan
	if err := db.First(&finalPlan, plan.ID).Error; err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if finalPlan.PlanStatus != constants.PlanDraft || finalPlan.Version != 6 {
		t.Fatalf("plan mutated on failed rerun: %s v%d", finalPlan.PlanStatus, finalPlan.Version)
	}
	var assessmentCount int64
	if err := db.Model(&model.DecompressionAssessment{}).Where("plan_id = ?", plan.ID).Count(&assessmentCount).Error; err != nil {
		t.Fatalf("count assessments: %v", err)
	}
	if assessmentCount != 1 {
		t.Fatalf("assessments = %d, want only the preserved superseded row", assessmentCount)
	}
	if got := countAuditEvents(t, db); got != auditBefore {
		t.Fatalf("audit events = %d, want unchanged %d", got, auditBefore)
	}
}
