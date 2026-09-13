package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/repository"
	"hazop-safeguard-coverage/backend/internal/util"
)

type safeguardFixture struct {
	service    SafeguardService
	items      RectificationItemService
	safeguards repository.SafeguardRepository
	scenario   model.DeviationScenario
	engineer   util.Actor
}

func newSafeguardFixture(t *testing.T) safeguardFixture {
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
		NodeCode: "S-201", Name: "Safeguard Test Node", UnitName: "Test Unit", Medium: "gas",
		DesignPressure: 2.0, DesignTemperature: 90, OwnerTeam: "test", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := nodeRepo.Create(context.Background(), &node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	scenario := model.DeviationScenario{
		ProcessNodeID: node.ID, Guideword: "more", Parameter: "flow",
		Cause: "control valve failure", Consequence: "overfeed", Likelihood: 3, Severity: 4,
		ScenarioState: "analyzed", Version: 1, CreatedBy: 10, CreatedByName: "engineer",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := scenarioRepo.Create(context.Background(), &scenario); err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	return safeguardFixture{
		service:    NewSafeguardService(safeguardRepo, scenarioRepo, auditRepo),
		items:      NewRectificationItemService(itemRepo, evaluationRepo, safeguardRepo, auditRepo),
		safeguards: safeguardRepo,
		scenario:   scenario,
		engineer:   util.Actor{UserID: 10, Username: "engineer", Role: "process_engineer", RequestID: "req-sg"},
	}
}

func validCreateSafeguardRequest(scenarioID uint) dto.CreateSafeguardRequest {
	verified := time.Now().UTC().Add(-24 * time.Hour)
	return dto.CreateSafeguardRequest{
		Name: "High flow trip", SafeguardType: "interlock",
		TargetScenarioID: scenarioID, IndependenceKey: "SIS-S201-FLOW",
		Effectiveness: 0.85, TestIntervalDays: 365,
		LastVerifiedAt: &verified, EvidenceNote: "proof test certificate",
	}
}

func TestSafeguardCreateRejectsBlankIndependenceKey(t *testing.T) {
	fixture := newSafeguardFixture(t)
	ctx := context.Background()
	var appErr *util.AppError
	for _, key := range []string{"", "   ", "\t\n  "} {
		request := validCreateSafeguardRequest(fixture.scenario.ID)
		request.IndependenceKey = key
		_, err := fixture.service.Create(ctx, request, fixture.engineer)
		if !errors.As(err, &appErr) || appErr.Status != 422 || appErr.Code != util.CodeValidation {
			t.Fatalf("create with key %q must return 422 VALIDATION_FAILED, got %v", key, err)
		}
	}
	if total := countSafeguards(t, fixture); total != 0 {
		t.Fatalf("rejected creates must persist no safeguard, found %d", total)
	}

	// A non-blank key creates successfully and is normalized to upper case.
	request := validCreateSafeguardRequest(fixture.scenario.ID)
	request.IndependenceKey = "  sis-s201-flow  "
	created, err := fixture.service.Create(ctx, request, fixture.engineer)
	if err != nil {
		t.Fatalf("create with a valid key should succeed: %v", err)
	}
	if created.IndependenceKey != "SIS-S201-FLOW" {
		t.Fatalf("independence key should be trimmed and uppercased, got %q", created.IndependenceKey)
	}
}

func TestSafeguardUpdateRejectsBlankIndependenceKey(t *testing.T) {
	fixture := newSafeguardFixture(t)
	ctx := context.Background()
	created, err := fixture.service.Create(ctx, validCreateSafeguardRequest(fixture.scenario.ID), fixture.engineer)
	if err != nil {
		t.Fatalf("create safeguard: %v", err)
	}
	var appErr *util.AppError
	for _, blank := range []string{"", "   "} {
		_, err := fixture.service.Update(ctx, created.ID,
			dto.UpdateSafeguardRequest{IndependenceKey: &blank}, fixture.engineer)
		if !errors.As(err, &appErr) || appErr.Status != 422 || appErr.Code != util.CodeValidation {
			t.Fatalf("clearing the key with %q must return 422, got %v", blank, err)
		}
	}
	reloaded, err := fixture.service.Get(ctx, created.ID)
	if err != nil || reloaded.IndependenceKey != "SIS-S201-FLOW" {
		t.Fatalf("existing independence key must survive blank updates, got %q (%v)", reloaded.IndependenceKey, err)
	}
	// Updating other fields still works and leaves the key intact.
	updated, err := fixture.service.Update(ctx, created.ID,
		dto.UpdateSafeguardRequest{EvidenceNote: strPtr("renewed certificate")}, fixture.engineer)
	if err != nil || updated.EvidenceNote != "renewed certificate" || updated.IndependenceKey != "SIS-S201-FLOW" {
		t.Fatalf("non-key update should apply and keep the key, got %#v (%v)", updated, err)
	}
}

func countSafeguards(t *testing.T, fixture safeguardFixture) int64 {
	t.Helper()
	list, _, err := fixture.safeguards.List(context.Background(), dto.SafeguardQuery{}, time.Now().UTC())
	if err != nil {
		t.Fatalf("count safeguards: %v", err)
	}
	return int64(len(list))
}
