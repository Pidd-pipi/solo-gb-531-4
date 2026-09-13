package dto

import (
	"hazop-safeguard-coverage/backend/internal/model"
	"strings"
	"time"
)

type GenerateRectificationRequest struct {
	EvaluationID uint       `json:"evaluation_id" binding:"required"`
	OwnerName    string     `json:"owner_name" binding:"omitempty,max=120"`
	DueDate      *time.Time `json:"due_date"`
}

func (r *GenerateRectificationRequest) Normalize() {
	r.OwnerName = strings.TrimSpace(r.OwnerName)
}

type UpdateRectificationRequest struct {
	OwnerName    *string    `json:"owner_name" binding:"omitempty,max=120"`
	DueDate      *time.Time `json:"due_date"`
	EvidenceNote *string    `json:"evidence_note" binding:"omitempty,max=4000"`
}

func (r *UpdateRectificationRequest) Normalize() {
	r.OwnerName = trimPointer(r.OwnerName)
	r.EvidenceNote = trimPointer(r.EvidenceNote)
}

type TransitionRectificationRequest struct {
	ToState string `json:"to_state" binding:"required,oneof=in_progress pending_review voided"`
	Reason  string `json:"reason" binding:"omitempty,max=1000"`
}

type CompleteRectificationRequest struct {
	SafeguardIDs []uint `json:"safeguard_ids" binding:"omitempty,max=32,dive,gt=0"`
}

type ReturnRectificationRequest struct {
	Reason string `json:"reason" binding:"required,min=3,max=1000"`
}

type RectificationQuery struct {
	ScenarioID uint
	State      string
	Page       int
	PageSize   int
}

type RectificationBindingResponse struct {
	SafeguardID   uint      `json:"safeguard_id"`
	SafeguardName string    `json:"safeguard_name"`
	BoundBy       uint      `json:"bound_by"`
	BoundByName   string    `json:"bound_by_name"`
	BoundAt       time.Time `json:"bound_at"`
}

type RectificationItemResponse struct {
	ID              uint                           `json:"id"`
	GapFingerprint  string                         `json:"gap_fingerprint"`
	EvaluationID    uint                           `json:"evaluation_id"`
	ScenarioID      uint                           `json:"scenario_id"`
	ScenarioLabel   string                         `json:"scenario_label"`
	PathID          string                         `json:"path_id"`
	NodeCode        string                         `json:"node_code"`
	Cause           string                         `json:"cause"`
	Consequence     string                         `json:"consequence"`
	OwnerName       string                         `json:"owner_name"`
	DueDate         *time.Time                     `json:"due_date,omitempty"`
	Overdue         bool                           `json:"overdue"`
	EvidenceNote    string                         `json:"evidence_note"`
	State           string                         `json:"state"`
	GeneratedBy     uint                           `json:"generated_by"`
	GeneratedByName string                         `json:"generated_by_name"`
	CompletedBy     *uint                          `json:"completed_by,omitempty"`
	CompletedAt     *time.Time                     `json:"completed_at,omitempty"`
	VoidReason      string                         `json:"void_reason,omitempty"`
	Bindings        []RectificationBindingResponse `json:"bindings"`
	CreatedAt       time.Time                      `json:"created_at"`
	UpdatedAt       time.Time                      `json:"updated_at"`
}

type RectificationListResponse struct {
	Items []RectificationItemResponse `json:"items"`
	Total int64                       `json:"total"`
	Page  int                         `json:"page"`
	Size  int                         `json:"page_size"`
}

type RectificationSkippedResponse struct {
	PathID         string `json:"path_id"`
	Cause          string `json:"cause"`
	Consequence    string `json:"consequence"`
	ExistingItemID uint   `json:"existing_item_id"`
	Reason         string `json:"reason"`
}

type GenerateRectificationResponse struct {
	Created []RectificationItemResponse    `json:"created"`
	Skipped []RectificationSkippedResponse `json:"skipped"`
}

type RectificationSummaryResponse struct {
	ByState    map[string]int64 `json:"by_state"`
	Incomplete int64            `json:"incomplete"`
	Total      int64            `json:"total"`
}

func NewRectificationItemResponse(item model.RectificationItem, now time.Time) RectificationItemResponse {
	response := RectificationItemResponse{
		ID: item.ID, GapFingerprint: item.GapFingerprint, EvaluationID: item.EvaluationID,
		ScenarioID: item.ScenarioID, PathID: item.PathID, NodeCode: item.NodeCode,
		Cause: item.Cause, Consequence: item.Consequence,
		OwnerName: item.OwnerName, DueDate: item.DueDate, EvidenceNote: item.EvidenceNote,
		State: item.State, GeneratedBy: item.GeneratedBy, GeneratedByName: item.GeneratedByName,
		CompletedBy: item.CompletedBy, CompletedAt: item.CompletedAt, VoidReason: item.VoidReason,
		Bindings:  make([]RectificationBindingResponse, 0, len(item.Bindings)),
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	if item.Scenario.ID != 0 {
		response.ScenarioLabel = strings.ToUpper(item.Scenario.Guideword) + " " + item.Scenario.Parameter
	}
	if item.DueDate != nil && item.State != "completed" && item.State != "voided" && now.After(*item.DueDate) {
		response.Overdue = true
	}
	for _, binding := range item.Bindings {
		response.Bindings = append(response.Bindings, RectificationBindingResponse{
			SafeguardID: binding.SafeguardID, SafeguardName: binding.SafeguardName,
			BoundBy: binding.BoundBy, BoundByName: binding.BoundByName, BoundAt: binding.BoundAt,
		})
	}
	return response
}
