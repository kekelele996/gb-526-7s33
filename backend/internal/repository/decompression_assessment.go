package repository

import (
	"context"
	"fmt"
	"time"

	"commercial-diving-decompression-control/backend/internal/audit"
	"commercial-diving-decompression-control/backend/internal/constants"
	"commercial-diving-decompression-control/backend/internal/model"
	"commercial-diving-decompression-control/backend/internal/util"
	"gorm.io/gorm"
)

type DecompressionAssessmentRepository struct {
	db    *gorm.DB
	audit *audit.Repository
}

func NewDecompressionAssessmentRepository(db *gorm.DB, auditRepo *audit.Repository) *DecompressionAssessmentRepository {
	return &DecompressionAssessmentRepository{db: db, audit: auditRepo}
}

func (r *DecompressionAssessmentRepository) List(ctx context.Context, planID uint, status string, page, size int) ([]model.DecompressionAssessment, int64, error) {
	query := r.db.WithContext(ctx).Model(&model.DecompressionAssessment{})
	if planID > 0 {
		query = query.Where("plan_id = ?", planID)
	}
	if status != "" {
		query = query.Where("assessment_status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count assessments: %w", err)
	}
	var items []model.DecompressionAssessment
	if err := query.Order("revision DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list assessments: %w", err)
	}
	return items, total, nil
}

func (r *DecompressionAssessmentRepository) Get(ctx context.Context, id uint) (model.DecompressionAssessment, error) {
	var item model.DecompressionAssessment
	if err := r.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return model.DecompressionAssessment{}, fmt.Errorf("get assessment %d: %w", id, err)
	}
	return item, nil
}

func (r *DecompressionAssessmentRepository) LatestByPlan(ctx context.Context, planID uint) (model.DecompressionAssessment, error) {
	var item model.DecompressionAssessment
	if err := r.db.WithContext(ctx).Where("plan_id = ?", planID).Order("revision DESC, id DESC").First(&item).Error; err != nil {
		return model.DecompressionAssessment{}, fmt.Errorf("get latest assessment for plan %d: %w", planID, err)
	}
	return item, nil
}

// CreateModeled atomically turns a draft plan into a modeled plan and appends
// the new immutable assessment as the next revision. A returned-to-draft plan
// is gated: the conditional plan update also requires that at least one
// exposure-segment change advanced the input version past rerun_gate_version.
// Every audit row is written inside the same transaction so a failure leaves
// plan, assessments and audit untouched.
func (r *DecompressionAssessmentRepository) CreateModeled(ctx context.Context, plan model.DivePlan, item *model.DecompressionAssessment, entry audit.Entry) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.DivePlan{}).
			Where("id = ? AND version = ? AND plan_status = ? AND rerun_gate_version < ?", plan.ID, plan.Version, constants.PlanDraft, plan.Version).
			Updates(map[string]any{"plan_status": constants.PlanModeled, "version": gorm.Expr("version + 1"), "rerun_gate_version": 0})
		if result.Error != nil {
			return fmt.Errorf("mark plan modeled: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return classifyModeledConflict(tx, plan)
		}
		revision, err := nextAssessmentRevision(tx, plan.ID)
		if err != nil {
			return err
		}
		item.Revision = revision
		if err := tx.Create(item).Error; err != nil {
			return fmt.Errorf("create immutable assessment: %w", err)
		}
		var predecessors []model.DecompressionAssessment
		if err := tx.Where("plan_id = ? AND superseded_by_id IS NULL AND id <> ?", plan.ID, item.ID).Find(&predecessors).Error; err != nil {
			return fmt.Errorf("load previous assessment revisions: %w", err)
		}
		for _, predecessor := range predecessors {
			patch := tx.Model(&model.DecompressionAssessment{}).
				Where("id = ? AND assessment_status = ? AND superseded_by_id IS NULL", predecessor.ID, constants.AssessmentSuperseded).
				Update("superseded_by_id", item.ID)
			if patch.Error != nil {
				return fmt.Errorf("link superseded assessment %d to revision %d: %w", predecessor.ID, revision, patch.Error)
			}
			if patch.RowsAffected != 1 {
				return util.Conflict("ASSESSMENT_STATE_CONFLICT", "an earlier assessment changed review state concurrently", nil)
			}
		}
		entry.EntityID = item.ID
		if err := r.audit.RecordWithDB(ctx, tx, entry); err != nil {
			return err
		}
		planEntry := entry
		planEntry.Action = "dive_plan.transition"
		planEntry.EntityType = "dive_plan"
		planEntry.EntityID = plan.ID
		planEntry.BeforeSummary = fmt.Sprintf("%s input_version=%d", constants.PlanDraft, plan.Version)
		planEntry.AfterSummary = fmt.Sprintf("%s assessment=%d revision=%d gate=cleared", constants.PlanModeled, item.ID, revision)
		return r.audit.RecordWithDB(ctx, tx, planEntry)
	})
	if err != nil {
		return fmt.Errorf("create modeled assessment transaction: %w", err)
	}
	return nil
}

// classifyModeledConflict turns the failed conditional plan update into the
// precise client error: the optimistic version lost, or the rework gate is
// still blocking because the planner has not changed any exposure segment.
func classifyModeledConflict(tx *gorm.DB, plan model.DivePlan) error {
	var current model.DivePlan
	if err := tx.Select("id", "version", "plan_status", "rerun_gate_version").First(&current, plan.ID).Error; err != nil {
		return fmt.Errorf("reload plan after failed model transition: %w", err)
	}
	if current.PlanStatus != constants.PlanDraft {
		return util.Conflict("PLAN_NOT_DRAFT", "only a draft plan can run a new immutable assessment", nil)
	}
	if current.Version != plan.Version {
		return util.Conflict("PLAN_VERSION_CONFLICT", "plan must remain at the requested draft version", nil)
	}
	return util.Conflict("SEGMENT_CHANGE_REQUIRED", "change at least one exposure segment before rerunning a returned assessment", nil)
}

func nextAssessmentRevision(tx *gorm.DB, planID uint) (uint, error) {
	var count int64
	if err := tx.Model(&model.DecompressionAssessment{}).Where("plan_id = ?", planID).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count assessment revisions: %w", err)
	}
	return uint(count) + 1, nil
}

// ReturnForRework moves a pending-supervisor-review plan back to draft and
// marks the immutable assessment superseded with the mandatory supervisor
// reason. The rerun gate is pinned to the post-return plan version so a new
// assessment cannot be created until an exposure segment changes. Conditional
// updates make concurrent returns, approvals and segment edits single-winner:
// any loser rolls the whole transaction back.
func (r *DecompressionAssessmentRepository) ReturnForRework(ctx context.Context, plan model.DivePlan, assessment model.DecompressionAssessment, reason string, supervisorID uint, assessmentEntry, planEntry audit.Entry) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		returnedAt := time.Now().UTC()
		planResult := tx.Model(&model.DivePlan{}).
			Where("id = ? AND version = ? AND plan_status = ?", plan.ID, plan.Version, constants.PlanPendingReview).
			Updates(map[string]any{
				"plan_status":        constants.PlanDraft,
				"version":            gorm.Expr("version + 1"),
				"rerun_gate_version": gorm.Expr("version + 1"),
			})
		if planResult.Error != nil {
			return fmt.Errorf("return plan to draft: %w", planResult.Error)
		}
		if planResult.RowsAffected != 1 {
			return util.Conflict("PLAN_VERSION_CONFLICT", "plan state or version changed concurrently", nil)
		}
		assessmentResult := tx.Model(&model.DecompressionAssessment{}).
			Where("id = ? AND assessment_status = ?", assessment.ID, constants.AssessmentPendingReview).
			Updates(map[string]any{
				"assessment_status": constants.AssessmentSuperseded,
				"returned_reason":   reason,
				"returned_by":       supervisorID,
				"returned_at":       returnedAt,
			})
		if assessmentResult.Error != nil {
			return fmt.Errorf("mark assessment superseded: %w", assessmentResult.Error)
		}
		if assessmentResult.RowsAffected != 1 {
			return util.Conflict("ASSESSMENT_STATE_CONFLICT", "assessment review state changed concurrently", nil)
		}
		assessmentEntry.EntityID = assessment.ID
		if err := r.audit.RecordWithDB(ctx, tx, assessmentEntry); err != nil {
			return err
		}
		planEntry.EntityType = "dive_plan"
		planEntry.EntityID = plan.ID
		return r.audit.RecordWithDB(ctx, tx, planEntry)
	})
	if err != nil {
		return fmt.Errorf("return assessment for rework transaction: %w", err)
	}
	return nil
}

func (r *DecompressionAssessmentRepository) Transition(ctx context.Context, plan model.DivePlan, assessment model.DecompressionAssessment, target constants.PlanStatus, actorID uint, entry audit.Entry) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		planChanges := map[string]any{"plan_status": target, "version": gorm.Expr("version + 1")}
		assessmentChanges := map[string]any{"assessment_status": string(target)}
		if target == constants.PlanApprovedTraining {
			now := time.Now().UTC()
			planChanges["reviewed_by"] = actorID
			assessmentChanges["reviewed_at"] = now
		}
		planResult := tx.Model(&model.DivePlan{}).Where("id = ? AND version = ? AND plan_status = ?", plan.ID, plan.Version, plan.PlanStatus).Updates(planChanges)
		if planResult.Error != nil {
			return fmt.Errorf("transition assessment plan: %w", planResult.Error)
		}
		if planResult.RowsAffected != 1 {
			return util.Conflict("PLAN_VERSION_CONFLICT", "plan state or version changed concurrently", nil)
		}
		assessmentResult := tx.Model(&model.DecompressionAssessment{}).Where("id = ? AND assessment_status = ?", assessment.ID, assessment.AssessmentStatus).Updates(assessmentChanges)
		if assessmentResult.Error != nil {
			return fmt.Errorf("transition assessment metadata: %w", assessmentResult.Error)
		}
		if assessmentResult.RowsAffected != 1 {
			return util.Conflict("ASSESSMENT_STATE_CONFLICT", "assessment review state changed concurrently", nil)
		}
		entry.EntityID = assessment.ID
		if err := r.audit.RecordWithDB(ctx, tx, entry); err != nil {
			return err
		}
		planEntry := entry
		planEntry.EntityType = "dive_plan"
		planEntry.EntityID = plan.ID
		planEntry.Action = "dive_plan.transition"
		return r.audit.RecordWithDB(ctx, tx, planEntry)
	})
	if err != nil {
		return fmt.Errorf("transition assessment transaction: %w", err)
	}
	return nil
}
