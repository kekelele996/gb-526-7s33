package model

import (
	"time"

	"commercial-diving-decompression-control/backend/internal/constants"
)

type DivePlan struct {
	ID                  uint                 `gorm:"primaryKey" json:"id"`
	PlanCode            string               `gorm:"size:40;not null;uniqueIndex" json:"plan_code"`
	DiverProfileID      uint                 `gorm:"not null;index" json:"diver_profile_id"`
	WorksitePressureBar float64              `gorm:"not null" json:"worksite_pressure_bar"`
	BreathingMixJSON    string               `gorm:"type:text;not null" json:"breathing_mix_json"`
	PlanStatus          constants.PlanStatus `gorm:"size:40;not null;index;check:plan_status IN ('draft','modeled','pending_supervisor_review','approved_for_training','archived')" json:"plan_status"`
	CreatedBy           uint                 `gorm:"not null;index" json:"created_by"`
	ReviewedBy          *uint                `gorm:"index" json:"reviewed_by"`
	Version             uint                 `gorm:"not null;default:1" json:"version"`
	// RerunGateVersion is set to the plan version at the moment a supervisor
	// returns an assessment for rework. A new assessment can only run after an
	// exposure-segment change bumps Version past this value. Zero means no
	// outstanding rework gate (initial draft or gate already cleared).
	RerunGateVersion uint      `gorm:"not null;default:0" json:"rerun_gate_version"`
	PlannedAt        time.Time `gorm:"not null;index" json:"planned_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (DivePlan) TableName() string { return "dive_plans" }

func (p DivePlan) Editable() bool { return p.PlanStatus == constants.PlanDraft }
