package model

import (
	"time"

	"commercial-diving-decompression-control/backend/internal/constants"
)

type DecompressionAssessment struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	PlanID uint `gorm:"not null;index" json:"plan_id"`
	// Revision numbers assessments of one plan starting at 1 so the page can
	// show version ordering without relying on timestamps.
	Revision             uint               `gorm:"not null;default:1" json:"revision"`
	AssessmentStatus     string             `gorm:"size:40;not null;index;check:assessment_status IN ('modeled','pending_supervisor_review','approved_for_training','archived','superseded')" json:"assessment_status"`
	AlgorithmVersion     string             `gorm:"size:48;not null;index" json:"algorithm_version"`
	InputSnapshotJSON    string             `gorm:"type:text;not null" json:"input_snapshot_json"`
	CompartmentLoadsJSON string             `gorm:"type:text;not null" json:"compartment_loads_json"`
	RiskFlagsJSON        string             `gorm:"type:text;not null" json:"risk_flags_json"`
	HighestRiskBand      constants.RiskBand `gorm:"size:20;not null;default:'informational';index;check:highest_risk_band IN ('informational','caution','elevated','invalid')" json:"highest_risk_band"`
	ComparativeScore     float64            `gorm:"not null" json:"comparative_score"`
	AssumptionsJSON      string             `gorm:"type:text;not null" json:"assumptions_json"`
	// SupersededByID links a returned assessment to the assessment that
	// replaced it. Zero for the current open assessment and for the first
	// revision until a newer one is created.
	SupersededByID *uint `gorm:"index" json:"superseded_by_id"`
	// ReturnedReason captures the mandatory supervisor reason when a pending
	// assessment is sent back to draft; the snapshot itself is untouched.
	ReturnedReason string     `gorm:"type:text;not null;default:''" json:"returned_reason"`
	ReturnedBy     *uint      `gorm:"index" json:"returned_by"`
	ReturnedAt     *time.Time `json:"returned_at"`
	CreatedAt      time.Time  `gorm:"not null;index" json:"created_at"`
	ReviewedAt     *time.Time `json:"reviewed_at"`
}

func (DecompressionAssessment) TableName() string { return "decompression_assessments" }
