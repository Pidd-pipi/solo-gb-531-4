package model

import "time"

type RectificationItem struct {
	ID              uint                   `gorm:"primaryKey" json:"id"`
	GapFingerprint  string                 `gorm:"size:64;not null;uniqueIndex" json:"gap_fingerprint"`
	EvaluationID    uint                   `gorm:"not null;index" json:"evaluation_id"`
	ScenarioID      uint                   `gorm:"not null;index" json:"scenario_id"`
	Scenario        DeviationScenario      `gorm:"foreignKey:ScenarioID" json:"scenario,omitempty"`
	PathID          string                 `gorm:"size:80;not null" json:"path_id"`
	NodeCode        string                 `gorm:"size:80;not null" json:"node_code"`
	Cause           string                 `gorm:"type:text;not null" json:"cause"`
	Consequence     string                 `gorm:"type:text;not null" json:"consequence"`
	OwnerName       string                 `gorm:"size:120;not null" json:"owner_name"`
	DueDate         *time.Time             `json:"due_date,omitempty"`
	EvidenceNote    string                 `gorm:"type:text;not null" json:"evidence_note"`
	State           string                 `gorm:"size:24;not null;index" json:"state"`
	GeneratedBy     uint                   `gorm:"not null;index" json:"generated_by"`
	GeneratedByName string                 `gorm:"size:80;not null" json:"generated_by_name"`
	CompletedBy     *uint                  `json:"completed_by,omitempty"`
	CompletedAt     *time.Time             `json:"completed_at,omitempty"`
	VoidReason      string                 `gorm:"type:text" json:"void_reason,omitempty"`
	Bindings        []RectificationBinding `gorm:"foreignKey:ItemID" json:"bindings,omitempty"`
	CreatedAt       time.Time              `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time              `gorm:"not null" json:"updated_at"`
}

func (RectificationItem) TableName() string { return "rectification_items" }

type RectificationBinding struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	ItemID        uint      `gorm:"not null;index" json:"item_id"`
	SafeguardID   uint      `gorm:"not null;uniqueIndex" json:"safeguard_id"`
	SafeguardName string    `gorm:"size:180;not null" json:"safeguard_name"`
	BoundBy       uint      `gorm:"not null" json:"bound_by"`
	BoundByName   string    `gorm:"size:80;not null" json:"bound_by_name"`
	BoundAt       time.Time `gorm:"not null" json:"bound_at"`
}

func (RectificationBinding) TableName() string { return "rectification_bindings" }
