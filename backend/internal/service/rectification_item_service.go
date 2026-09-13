package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"hazop-safeguard-coverage/backend/internal/constants"
	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/repository"
	"hazop-safeguard-coverage/backend/internal/util"
	"net/http"
	"strings"
	"time"
)

type RectificationItemService interface {
	Generate(context.Context, dto.GenerateRectificationRequest, util.Actor) (dto.GenerateRectificationResponse, error)
	Get(context.Context, uint) (dto.RectificationItemResponse, error)
	List(context.Context, dto.RectificationQuery) (dto.RectificationListResponse, error)
	Summary(context.Context) (dto.RectificationSummaryResponse, error)
	Update(context.Context, uint, dto.UpdateRectificationRequest, util.Actor) (dto.RectificationItemResponse, error)
	Transition(context.Context, uint, dto.TransitionRectificationRequest, util.Actor) (dto.RectificationItemResponse, error)
	Complete(context.Context, uint, dto.CompleteRectificationRequest, util.Actor) (dto.RectificationItemResponse, error)
	Return(context.Context, uint, dto.ReturnRectificationRequest, util.Actor) (dto.RectificationItemResponse, error)
}

type rectificationItemService struct {
	items       repository.RectificationItemRepository
	evaluations repository.CoverageEvaluationRepository
	safeguards  repository.SafeguardRepository
	audits      repository.AuditRepository
	now         func() time.Time
}

func NewRectificationItemService(
	items repository.RectificationItemRepository,
	evaluations repository.CoverageEvaluationRepository,
	safeguards repository.SafeguardRepository,
	audits repository.AuditRepository,
) RectificationItemService {
	return &rectificationItemService{
		items: items, evaluations: evaluations, safeguards: safeguards, audits: audits,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func gapFingerprint(scenarioID uint, cause string, consequence string) string {
	normalized := fmt.Sprintf("%d|%s|%s", scenarioID, strings.TrimSpace(cause), strings.TrimSpace(consequence))
	return util.HashString(normalized)
}

func (s *rectificationItemService) Generate(
	ctx context.Context,
	request dto.GenerateRectificationRequest,
	actor util.Actor,
) (dto.GenerateRectificationResponse, error) {
	request.Normalize()
	evaluation, err := s.evaluations.GetByID(ctx, request.EvaluationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.GenerateRectificationResponse{}, util.NotFound("coverage evaluation")
		}
		return dto.GenerateRectificationResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to load coverage evaluation", err)
	}
	state := constants.CoverageState(evaluation.EvaluationState)
	if state != constants.CoverageCompleted && state != constants.CoverageConfirmed {
		return dto.GenerateRectificationResponse{}, util.NewError(
			http.StatusConflict, util.CodeConflict,
			"only a completed or confirmed evaluation can generate rectification items",
		)
	}
	var paths []dto.CoveragePathResponse
	if err := json.Unmarshal([]byte(evaluation.UncoveredPaths), &paths); err != nil {
		return dto.GenerateRectificationResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to decode uncovered paths", err)
	}
	now := s.now()
	response := dto.GenerateRectificationResponse{
		Created: make([]dto.RectificationItemResponse, 0, len(paths)),
		Skipped: make([]dto.RectificationSkippedResponse, 0),
	}
	for _, path := range paths {
		fingerprint := gapFingerprint(evaluation.ScenarioID, path.Cause, path.Consequence)
		if existing, findErr := s.items.FindByFingerprint(ctx, fingerprint); findErr == nil {
			response.Skipped = append(response.Skipped, dto.RectificationSkippedResponse{
				PathID: path.PathID, Cause: path.Cause, Consequence: path.Consequence,
				ExistingItemID: existing.ID,
				Reason:         fmt.Sprintf("同一缺口已存在整改项 #%d（状态 %s），重复生成已拦截", existing.ID, existing.State),
			})
			continue
		}
		item := model.RectificationItem{
			GapFingerprint: fingerprint, EvaluationID: evaluation.ID, ScenarioID: evaluation.ScenarioID,
			PathID: path.PathID, NodeCode: path.NodeCode, Cause: path.Cause, Consequence: path.Consequence,
			OwnerName: request.OwnerName, DueDate: request.DueDate, EvidenceNote: "",
			State:       string(constants.RectificationPending),
			GeneratedBy: actor.UserID, GeneratedByName: actor.Username,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.items.Create(ctx, &item); err != nil {
			if uniqueViolation(err) {
				existing, findErr := s.items.FindByFingerprint(ctx, fingerprint)
				if findErr == nil {
					response.Skipped = append(response.Skipped, dto.RectificationSkippedResponse{
						PathID: path.PathID, Cause: path.Cause, Consequence: path.Consequence,
						ExistingItemID: existing.ID,
						Reason:         fmt.Sprintf("同一缺口已存在整改项 #%d（状态 %s），重复生成已拦截", existing.ID, existing.State),
					})
					continue
				}
			}
			return dto.GenerateRectificationResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to create rectification item", err)
		}
		if err := s.recordAudit(ctx, actor, item.ID, "generate", nil, item, "从评估缺口生成整改项"); err != nil {
			return dto.GenerateRectificationResponse{}, err
		}
		response.Created = append(response.Created, dto.NewRectificationItemResponse(item, now))
	}
	return response, nil
}

func (s *rectificationItemService) Get(ctx context.Context, id uint) (dto.RectificationItemResponse, error) {
	item, err := s.items.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.RectificationItemResponse{}, util.NotFound("rectification item")
		}
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to load rectification item", err)
	}
	return dto.NewRectificationItemResponse(item, s.now()), nil
}

func (s *rectificationItemService) List(ctx context.Context, query dto.RectificationQuery) (dto.RectificationListResponse, error) {
	items, total, err := s.items.List(ctx, query)
	if err != nil {
		return dto.RectificationListResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to list rectification items", err)
	}
	now := s.now()
	response := dto.RectificationListResponse{
		Items: make([]dto.RectificationItemResponse, 0, len(items)),
		Total: total, Page: query.Page, Size: query.PageSize,
	}
	for _, item := range items {
		response.Items = append(response.Items, dto.NewRectificationItemResponse(item, now))
	}
	return response, nil
}

func (s *rectificationItemService) Summary(ctx context.Context) (dto.RectificationSummaryResponse, error) {
	counts, err := s.items.CountByState(ctx)
	if err != nil {
		return dto.RectificationSummaryResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to summarize rectification items", err)
	}
	summary := dto.RectificationSummaryResponse{ByState: map[string]int64{}}
	for _, state := range constants.RectificationStateValues() {
		summary.ByState[state] = counts[state]
		summary.Total += counts[state]
	}
	summary.Incomplete = counts[string(constants.RectificationPending)] +
		counts[string(constants.RectificationInProgress)] +
		counts[string(constants.RectificationPendingReview)]
	return summary, nil
}

func (s *rectificationItemService) Update(
	ctx context.Context,
	id uint,
	request dto.UpdateRectificationRequest,
	actor util.Actor,
) (dto.RectificationItemResponse, error) {
	request.Normalize()
	before, err := s.items.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.RectificationItemResponse{}, util.NotFound("rectification item")
		}
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to load rectification item", err)
	}
	updates := map[string]any{}
	if request.OwnerName != nil {
		updates["owner_name"] = *request.OwnerName
	}
	if request.DueDate != nil {
		updates["due_date"] = request.DueDate.UTC()
	}
	if request.EvidenceNote != nil {
		updates["evidence_note"] = *request.EvidenceNote
	}
	if len(updates) == 0 {
		return dto.RectificationItemResponse{}, util.NewError(http.StatusUnprocessableEntity, util.CodeValidation, "no editable fields were provided")
	}
	if err := s.items.Update(ctx, id, updates); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.RectificationItemResponse{}, util.NewError(http.StatusConflict, util.CodeStateTransition, "completed or voided rectification items cannot be edited")
		}
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to update rectification item", err)
	}
	after, err := s.items.GetByID(ctx, id)
	if err != nil {
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to reload rectification item", err)
	}
	if err := s.recordAudit(ctx, actor, id, "update", before, after, "更新责任人/截止日期/证据说明"); err != nil {
		return dto.RectificationItemResponse{}, err
	}
	return dto.NewRectificationItemResponse(after, s.now()), nil
}

func (s *rectificationItemService) Transition(
	ctx context.Context,
	id uint,
	request dto.TransitionRectificationRequest,
	actor util.Actor,
) (dto.RectificationItemResponse, error) {
	before, err := s.items.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.RectificationItemResponse{}, util.NotFound("rectification item")
		}
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to load rectification item", err)
	}
	from := constants.RectificationState(before.State)
	to := constants.RectificationState(request.ToState)
	if !to.Valid() || !constants.CanTransitionRectification(from, to) {
		return dto.RectificationItemResponse{}, util.NewError(
			http.StatusConflict, util.CodeStateTransition,
			fmt.Sprintf("rectification item cannot transition from %s to %s", before.State, request.ToState),
		)
	}
	updates := map[string]any{}
	action := "transition"
	if to == constants.RectificationVoided {
		reason := strings.TrimSpace(request.Reason)
		if len(reason) < 3 {
			return dto.RectificationItemResponse{}, util.NewError(http.StatusUnprocessableEntity, util.CodeValidation, "voiding a rectification item requires a reason of at least 3 characters")
		}
		updates["void_reason"] = util.CompactText(reason, 1000)
		action = "void"
	}
	changed, err := s.items.Transition(ctx, id, []string{before.State}, request.ToState, updates)
	if err != nil {
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to transition rectification item", err)
	}
	if !changed {
		return dto.RectificationItemResponse{}, util.NewError(http.StatusConflict, util.CodeStateTransition, "rectification item state changed concurrently")
	}
	after, err := s.items.GetByID(ctx, id)
	if err != nil {
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to reload rectification item", err)
	}
	if err := s.recordAudit(ctx, actor, id, action, before, after, strings.TrimSpace(request.Reason)); err != nil {
		return dto.RectificationItemResponse{}, err
	}
	return dto.NewRectificationItemResponse(after, s.now()), nil
}

func (s *rectificationItemService) Complete(
	ctx context.Context,
	id uint,
	request dto.CompleteRectificationRequest,
	actor util.Actor,
) (dto.RectificationItemResponse, error) {
	before, err := s.items.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.RectificationItemResponse{}, util.NotFound("rectification item")
		}
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to load rectification item", err)
	}
	if before.State != string(constants.RectificationPendingReview) {
		return dto.RectificationItemResponse{}, util.NewError(
			http.StatusConflict, util.CodeStateTransition,
			"only a rectification item pending review can be completed",
		)
	}
	if len(request.SafeguardIDs) == 0 {
		return dto.RectificationItemResponse{}, util.NewError(
			http.StatusUnprocessableEntity, util.CodeSafeguardBinding,
			"完成整改必须绑定至少一条新补录且有效的保护层",
		)
	}
	now := s.now()
	safeguards := make([]model.Safeguard, 0, len(request.SafeguardIDs))
	seen := map[uint]struct{}{}
	for _, safeguardID := range request.SafeguardIDs {
		if _, duplicate := seen[safeguardID]; duplicate {
			continue
		}
		seen[safeguardID] = struct{}{}
		safeguard, err := s.safeguards.GetByID(ctx, safeguardID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return dto.RectificationItemResponse{}, util.NotFound("safeguard")
			}
			return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to load safeguard", err)
		}
		if safeguard.TargetScenarioID != before.ScenarioID {
			return dto.RectificationItemResponse{}, util.NewError(
				http.StatusUnprocessableEntity, util.CodeSafeguardBinding,
				fmt.Sprintf("保护层 #%d 不属于该整改项的偏差场景", safeguardID),
			)
		}
		if !safeguard.IsEffectiveAt(now) {
			return dto.RectificationItemResponse{}, util.NewError(
				http.StatusUnprocessableEntity, util.CodeSafeguardBinding,
				fmt.Sprintf("保护层 #%d 当前无效或验证已过期，不能用于完成整改", safeguardID),
			)
		}
		if !safeguard.CreatedAt.After(before.CreatedAt) {
			return dto.RectificationItemResponse{}, util.NewError(
				http.StatusUnprocessableEntity, util.CodeSafeguardBinding,
				fmt.Sprintf("保护层 #%d 早于整改项创建时间，不属于新补录的保护层", safeguardID),
			)
		}
		safeguards = append(safeguards, safeguard)
	}
	bound, err := s.items.FindActiveBindings(ctx, request.SafeguardIDs)
	if err != nil {
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to check existing safeguard bindings", err)
	}
	for _, binding := range bound {
		if binding.ItemID == id {
			continue
		}
		return dto.RectificationItemResponse{}, util.NewError(
			http.StatusConflict, util.CodeSafeguardBound,
			fmt.Sprintf("保护层 #%d 已用于关闭整改项 #%d，不能重复绑定", binding.SafeguardID, binding.ItemID),
		)
	}
	completedAt := now
	err = s.items.WithTx(ctx, func(tx repository.RectificationItemRepository) error {
		for _, safeguard := range safeguards {
			binding := model.RectificationBinding{
				ItemID: id, SafeguardID: safeguard.ID, SafeguardName: safeguard.Name,
				BoundBy: actor.UserID, BoundByName: actor.Username, BoundAt: now,
			}
			if err := tx.CreateBinding(ctx, &binding); err != nil {
				if uniqueViolation(err) {
					conflicts, findErr := tx.FindActiveBindings(ctx, []uint{safeguard.ID})
					if findErr == nil && len(conflicts) > 0 {
						return util.NewError(http.StatusConflict, util.CodeSafeguardBound,
							fmt.Sprintf("保护层 #%d 已用于关闭整改项 #%d，不能重复绑定", safeguard.ID, conflicts[0].ItemID))
					}
					return util.NewError(http.StatusConflict, util.CodeSafeguardBound,
						fmt.Sprintf("保护层 #%d 已绑定到其他整改项，不能重复绑定", safeguard.ID))
				}
				return util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to bind safeguard", err)
			}
		}
		changed, err := tx.Transition(ctx, id,
			[]string{string(constants.RectificationPendingReview)},
			string(constants.RectificationCompleted),
			map[string]any{"completed_by": actor.UserID, "completed_at": completedAt},
		)
		if err != nil {
			return util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to complete rectification item", err)
		}
		if !changed {
			return util.NewError(http.StatusConflict, util.CodeStateTransition, "rectification item state changed concurrently")
		}
		return nil
	})
	if err != nil {
		return dto.RectificationItemResponse{}, err
	}
	after, err := s.items.GetByID(ctx, id)
	if err != nil {
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to reload rectification item", err)
	}
	if err := s.recordAudit(ctx, actor, id, "complete", before, after, fmt.Sprintf("绑定 %d 条新补录保护层后完成", len(safeguards))); err != nil {
		return dto.RectificationItemResponse{}, err
	}
	return dto.NewRectificationItemResponse(after, s.now()), nil
}

func (s *rectificationItemService) Return(
	ctx context.Context,
	id uint,
	request dto.ReturnRectificationRequest,
	actor util.Actor,
) (dto.RectificationItemResponse, error) {
	before, err := s.items.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto.RectificationItemResponse{}, util.NotFound("rectification item")
		}
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to load rectification item", err)
	}
	reason := strings.TrimSpace(request.Reason)
	evidence := fmt.Sprintf("%s\n[return] %s", before.EvidenceNote, util.CompactText(reason, 1000))
	changed, err := s.items.Transition(ctx, id,
		[]string{string(constants.RectificationPendingReview)},
		string(constants.RectificationInProgress),
		map[string]any{"evidence_note": evidence},
	)
	if err != nil {
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to return rectification item", err)
	}
	if !changed {
		return dto.RectificationItemResponse{}, util.NewError(http.StatusConflict, util.CodeStateTransition, "only a rectification item pending review can be returned")
	}
	after, err := s.items.GetByID(ctx, id)
	if err != nil {
		return dto.RectificationItemResponse{}, util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to reload rectification item", err)
	}
	if err := s.recordAudit(ctx, actor, id, "return", before, after, reason); err != nil {
		return dto.RectificationItemResponse{}, err
	}
	return dto.NewRectificationItemResponse(after, s.now()), nil
}

func (s *rectificationItemService) recordAudit(
	ctx context.Context,
	actor util.Actor,
	entityID uint,
	action string,
	before any,
	after any,
	summary string,
) error {
	beforeJSON, err := snapshotJSON(before)
	if err != nil {
		return util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to serialize audit snapshot", err)
	}
	afterJSON, err := snapshotJSON(after)
	if err != nil {
		return util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to serialize audit snapshot", err)
	}
	log := model.AuditLog{
		RequestID: actor.RequestID, ActorID: actor.UserID, ActorName: actor.Username,
		ActorRole: actor.Role, EntityType: "rectification_item", EntityID: entityID, Action: action,
		BeforeSnapshot: beforeJSON, AfterSnapshot: afterJSON,
		ResultSummary: util.CompactText(summary, 1000), CreatedAt: s.now(),
	}
	if err := s.audits.Record(ctx, log); err != nil {
		return util.WrapError(http.StatusInternalServerError, util.CodeInternal, "unable to record write audit", err)
	}
	return nil
}
