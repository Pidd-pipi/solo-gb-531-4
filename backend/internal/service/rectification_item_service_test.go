package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"hazop-safeguard-coverage/backend/internal/algorithm"
	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/repository"
	"hazop-safeguard-coverage/backend/internal/util"
)

type rectificationFixture struct {
	db          *gorm.DB
	items       repository.RectificationItemRepository
	evaluations repository.CoverageEvaluationRepository
	safeguards  repository.SafeguardRepository
	scenarios   repository.DeviationScenarioRepository
	service     RectificationItemService
	scenario    model.DeviationScenario
	evaluation  model.CoverageEvaluation
	engineer    util.Actor
	reviewer    util.Actor
}

func strPtr(value string) *string { return &value }

func newRectificationFixture(t *testing.T) rectificationFixture {
	t.Helper()
	db := testDB(t)
	nodeRepo := repository.NewProcessNodeRepository(db)
	scenarioRepo := repository.NewDeviationScenarioRepository(db)
	safeguardRepo := repository.NewSafeguardRepository(db)
	evaluationRepo := repository.NewCoverageEvaluationRepository(db)
	itemRepo := repository.NewRectificationItemRepository(db)
	auditRepo := repository.NewAuditRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	node := model.ProcessNode{
		NodeCode: "R-201", Name: "Rectification Test Node", UnitName: "Test Unit", Medium: "solvent",
		DesignPressure: 1.5, DesignTemperature: 120, OwnerTeam: "test", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := nodeRepo.Create(context.Background(), &node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	scenario := model.DeviationScenario{
		ProcessNodeID: node.ID, Guideword: "more", Parameter: "pressure",
		Cause: "blocked outlet", Consequence: "vessel overpressure", Likelihood: 3, Severity: 5,
		ScenarioState: "analyzed", Version: 1, CreatedBy: 10, CreatedByName: "engineer",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := scenarioRepo.Create(context.Background(), &scenario); err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	uncovered := `[{"path_id":"P-1","node_code":"R-201","cause":"blocked outlet","consequence":"vessel overpressure","safeguard_ids":[],"independence_keys":[],"combined_protection":0,"covered":false,"reason":"no effective safeguard"}]`
	evaluation := model.CoverageEvaluation{
		ScenarioID: scenario.ID, AlgorithmVersion: algorithm.Version,
		InputSnapshot: "{}", InputHash: util.HashString("rectification-test"),
		CoverageScore: 0, UncoveredPaths: uncovered, DeduplicatedSafeguards: "[]",
		RiskRankBefore: "high", RiskRankAfter: "high",
		EvaluationState: "completed", Explanation: "{}",
		EvaluatedBy: 10, EvaluatedByName: "engineer", EvaluatedAt: now,
		IdempotencyKey: "rectification-test-key", CreatedAt: now, UpdatedAt: now,
	}
	if err := evaluationRepo.Create(context.Background(), &evaluation); err != nil {
		t.Fatalf("create evaluation: %v", err)
	}
	return rectificationFixture{
		db: db, items: itemRepo, evaluations: evaluationRepo, safeguards: safeguardRepo, scenarios: scenarioRepo,
		service:  NewRectificationItemService(itemRepo, evaluationRepo, safeguardRepo, auditRepo),
		scenario: scenario, evaluation: evaluation,
		engineer: util.Actor{UserID: 10, Username: "engineer", Role: "process_engineer", RequestID: "req-engineer"},
		reviewer: util.Actor{UserID: 20, Username: "reviewer", Role: "safety_reviewer", RequestID: "req-reviewer"},
	}
}

func (f rectificationFixture) generateOne(t *testing.T) dto.RectificationItemResponse {
	t.Helper()
	result, err := f.service.Generate(context.Background(), dto.GenerateRectificationRequest{
		EvaluationID: f.evaluation.ID, OwnerName: "张工",
	}, f.engineer)
	if err != nil {
		t.Fatalf("generate rectification items: %v", err)
	}
	if len(result.Created) != 1 || len(result.Skipped) != 0 {
		t.Fatalf("expected 1 created and 0 skipped, got %d created %d skipped", len(result.Created), len(result.Skipped))
	}
	return result.Created[0]
}

func (f rectificationFixture) addSafeguard(t *testing.T, name string, scenarioID uint, createdAt time.Time, verifiedAt *time.Time, lifecycle string) model.Safeguard {
	t.Helper()
	safeguard := model.Safeguard{
		Name: name, SafeguardType: "interlock", TargetScenarioID: scenarioID,
		IndependenceKey: "KEY-" + name, Effectiveness: 0.8, TestIntervalDays: 365,
		LastVerifiedAt: verifiedAt, LifecycleState: lifecycle, EvidenceNote: "test evidence",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := f.safeguards.Create(context.Background(), &safeguard); err != nil {
		t.Fatalf("create safeguard %s: %v", name, err)
	}
	return safeguard
}

func TestRectificationGenerateAndDuplicateBlocked(t *testing.T) {
	fixture := newRectificationFixture(t)
	ctx := context.Background()
	item := fixture.generateOne(t)
	if item.State != "pending" || item.OwnerName != "张工" || item.Cause != "blocked outlet" {
		t.Fatalf("unexpected generated item: %#v", item)
	}
	duplicate, err := fixture.service.Generate(ctx, dto.GenerateRectificationRequest{EvaluationID: fixture.evaluation.ID}, fixture.engineer)
	if err != nil {
		t.Fatalf("second generate should not fail: %v", err)
	}
	if len(duplicate.Created) != 0 || len(duplicate.Skipped) != 1 {
		t.Fatalf("duplicate generation must be blocked, got %d created %d skipped", len(duplicate.Created), len(duplicate.Skipped))
	}
	if duplicate.Skipped[0].ExistingItemID != item.ID {
		t.Fatalf("skipped entry should reference existing item %d, got %d", item.ID, duplicate.Skipped[0].ExistingItemID)
	}
	summary, err := fixture.service.Summary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Incomplete != 1 || summary.ByState["pending"] != 1 || summary.Total != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	queued := model.CoverageEvaluation{
		ScenarioID: fixture.scenario.ID, AlgorithmVersion: algorithm.Version,
		InputSnapshot: "{}", InputHash: util.HashString("queued"), UncoveredPaths: "[]",
		DeduplicatedSafeguards: "[]", RiskRankBefore: "high", RiskRankAfter: "high",
		EvaluationState: "queued", Explanation: "{}", EvaluatedBy: 10, EvaluatedByName: "engineer",
		EvaluatedAt: time.Now().UTC(), IdempotencyKey: "rectification-queued-key",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fixture.evaluations.Create(ctx, &queued); err != nil {
		t.Fatalf("create queued evaluation: %v", err)
	}
	_, err = fixture.service.Generate(ctx, dto.GenerateRectificationRequest{EvaluationID: queued.ID}, fixture.engineer)
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("generate from queued evaluation should return 409, got %v", err)
	}
}

func TestRectificationFlowAndCompletionGate(t *testing.T) {
	fixture := newRectificationFixture(t)
	ctx := context.Background()
	item := fixture.generateOne(t)
	verified := time.Now().UTC().Add(-24 * time.Hour)
	oldSafeguard := fixture.addSafeguard(t, "old-existing", fixture.scenario.ID, time.Now().UTC().Add(-time.Hour), &verified, "active")
	expiredVerification := time.Now().UTC().AddDate(0, 0, -500)
	expiredSafeguard := fixture.addSafeguard(t, "expired-one", fixture.scenario.ID, time.Now().UTC().Add(time.Minute), &expiredVerification, "expired")
	otherScenario := model.DeviationScenario{
		ProcessNodeID: fixture.scenario.ProcessNodeID, Guideword: "less", Parameter: "flow",
		Cause: "pump trip", Consequence: "loss of feed", Likelihood: 2, Severity: 3,
		ScenarioState: "draft", Version: 1, CreatedBy: 10, CreatedByName: "engineer",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fixture.scenarios.Create(ctx, &otherScenario); err != nil {
		t.Fatalf("create other scenario: %v", err)
	}
	otherSafeguard := fixture.addSafeguard(t, "other-scenario", otherScenario.ID, time.Now().UTC().Add(time.Minute), &verified, "active")
	var appErr *util.AppError
	_, err := fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "completed"}, fixture.engineer)
	if !errors.As(err, &appErr) || appErr.Status != 409 || appErr.Code != util.CodeStateTransition {
		t.Fatalf("pending -> completed should be rejected with 409, got %v", err)
	}
	started, err := fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "in_progress"}, fixture.engineer)
	if err != nil || started.State != "in_progress" {
		t.Fatalf("pending -> in_progress failed: %v", err)
	}
	submitted, err := fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "pending_review"}, fixture.engineer)
	if err != nil || submitted.State != "pending_review" {
		t.Fatalf("in_progress -> pending_review failed: %v", err)
	}
	_, err = fixture.service.Complete(ctx, item.ID, dto.CompleteRectificationRequest{SafeguardIDs: nil}, fixture.reviewer)
	if !errors.As(err, &appErr) || appErr.Status != 422 || appErr.Code != util.CodeSafeguardBinding {
		t.Fatalf("complete without binding should return 422 SAFEGUARD_BINDING_REQUIRED, got %v", err)
	}
	_, err = fixture.service.Complete(ctx, item.ID, dto.CompleteRectificationRequest{SafeguardIDs: []uint{oldSafeguard.ID}}, fixture.reviewer)
	if !errors.As(err, &appErr) || appErr.Code != util.CodeSafeguardBinding {
		t.Fatalf("complete with pre-existing safeguard should be rejected, got %v", err)
	}
	_, err = fixture.service.Complete(ctx, item.ID, dto.CompleteRectificationRequest{SafeguardIDs: []uint{expiredSafeguard.ID}}, fixture.reviewer)
	if !errors.As(err, &appErr) || appErr.Code != util.CodeSafeguardBinding {
		t.Fatalf("complete with expired safeguard should be rejected, got %v", err)
	}
	_, err = fixture.service.Complete(ctx, item.ID, dto.CompleteRectificationRequest{SafeguardIDs: []uint{otherSafeguard.ID}}, fixture.reviewer)
	if !errors.As(err, &appErr) || appErr.Code != util.CodeSafeguardBinding {
		t.Fatalf("complete with another scenario's safeguard should be rejected, got %v", err)
	}
	newSafeguard := fixture.addSafeguard(t, "newly-added", fixture.scenario.ID, time.Now().UTC().Add(time.Minute), &verified, "active")
	completed, err := fixture.service.Complete(ctx, item.ID, dto.CompleteRectificationRequest{SafeguardIDs: []uint{newSafeguard.ID}}, fixture.reviewer)
	if err != nil {
		t.Fatalf("complete with new valid safeguard should succeed: %v", err)
	}
	if completed.State != "completed" || len(completed.Bindings) != 1 || completed.Bindings[0].SafeguardID != newSafeguard.ID {
		t.Fatalf("unexpected completed item: %#v", completed)
	}
	if completed.CompletedBy == nil || *completed.CompletedBy != fixture.reviewer.UserID {
		t.Fatalf("completed_by should record reviewer, got %#v", completed.CompletedBy)
	}
	_, err = fixture.service.Update(ctx, item.ID, dto.UpdateRectificationRequest{OwnerName: strPtr("李工")}, fixture.engineer)
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("editing a completed item should return 409, got %v", err)
	}
	_, err = fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "in_progress"}, fixture.engineer)
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("transition from completed should return 409, got %v", err)
	}
}

func TestRectificationUpdateVoidAndReturn(t *testing.T) {
	fixture := newRectificationFixture(t)
	ctx := context.Background()
	item := fixture.generateOne(t)
	due := time.Now().UTC().Add(72 * time.Hour)
	updated, err := fixture.service.Update(ctx, item.ID, dto.UpdateRectificationRequest{
		OwnerName: strPtr("王工"), DueDate: &due, EvidenceNote: strPtr("已联系仪表班"),
	}, fixture.engineer)
	if err != nil {
		t.Fatalf("update rectification item: %v", err)
	}
	if updated.OwnerName != "王工" || updated.DueDate == nil || updated.EvidenceNote != "已联系仪表班" {
		t.Fatalf("update did not apply: %#v", updated)
	}
	var appErr *util.AppError
	_, err = fixture.service.Update(ctx, item.ID, dto.UpdateRectificationRequest{}, fixture.engineer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("empty update should return 422, got %v", err)
	}
	if _, err := fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "in_progress"}, fixture.engineer); err != nil {
		t.Fatalf("start rectification: %v", err)
	}
	if _, err := fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "pending_review"}, fixture.engineer); err != nil {
		t.Fatalf("submit for review: %v", err)
	}
	returned, err := fixture.service.Return(ctx, item.ID, dto.ReturnRectificationRequest{Reason: "证据不足，请补充"}, fixture.reviewer)
	if err != nil || returned.State != "in_progress" {
		t.Fatalf("return to in_progress failed: %v", err)
	}
	_, err = fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "voided"}, fixture.engineer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("void without reason should return 422, got %v", err)
	}
	voided, err := fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "voided", Reason: "场景已重新分析"}, fixture.engineer)
	if err != nil || voided.State != "voided" || voided.VoidReason == "" {
		t.Fatalf("void with reason failed: %v", err)
	}
	_, err = fixture.service.Transition(ctx, item.ID, dto.TransitionRectificationRequest{ToState: "in_progress"}, fixture.engineer)
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("transition from voided should return 409, got %v", err)
	}
	summary, err := fixture.service.Summary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Incomplete != 0 || summary.ByState["voided"] != 1 {
		t.Fatalf("voided item must not count as incomplete: %#v", summary)
	}
}

func TestRectificationItemsSurviveEvaluationVoid(t *testing.T) {
	fixture := newRectificationFixture(t)
	ctx := context.Background()
	item := fixture.generateOne(t)
	coverageService := NewCoverageEvaluationService(
		fixture.evaluations, fixture.scenarios, repository.NewProcessNodeRepository(fixture.db),
		fixture.safeguards, repository.NewAuditRepository(fixture.db), algorithm.NewEvaluator(),
	)
	if _, err := coverageService.Void(ctx, fixture.evaluation.ID, fixture.reviewer); err != nil {
		t.Fatalf("void evaluation: %v", err)
	}
	loaded, err := fixture.service.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("rectification item must survive evaluation void: %v", err)
	}
	if loaded.State != "pending" {
		t.Fatalf("evaluation void must not change rectification state, got %s", loaded.State)
	}
	summary, err := fixture.service.Summary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Total != 1 {
		t.Fatalf("evaluation void must not clear rectification items: %#v", summary)
	}
}
