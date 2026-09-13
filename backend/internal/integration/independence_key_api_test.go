package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/util"
)

// registerSafeguardBody builds a valid create payload for the safeguards API.
func registerSafeguardBody(scenarioID uint, key, name string) map[string]any {
	return map[string]any{
		"name":               name,
		"safeguard_type":     "interlock",
		"target_scenario_id": scenarioID,
		"independence_key":   key,
		"effectiveness":      0.85,
		"test_interval_days": 365,
		"last_verified_at":   time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339),
		"evidence_note":      "proof test certificate",
	}
}

// addKeylessSafeguard stores an otherwise-valid active safeguard with a blank
// independence key directly, emulating a legacy ledger record created before
// the validation existed (the API now refuses to create such a record).
func (f *apiFixture) addKeylessSafeguard(t *testing.T, name string, scenarioID uint, createdAt time.Time) model.Safeguard {
	t.Helper()
	verified := time.Now().UTC().Add(-24 * time.Hour)
	safeguard := model.Safeguard{
		Name: name, SafeguardType: "interlock", TargetScenarioID: scenarioID,
		IndependenceKey: "   ", Effectiveness: 0.8, TestIntervalDays: 365,
		LastVerifiedAt: &verified, LifecycleState: "active", EvidenceNote: "legacy missing key",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := f.safeguards.Create(context.Background(), &safeguard); err != nil {
		t.Fatalf("create keyless safeguard: %v", err)
	}
	return safeguard
}

// TestRectificationAPISafeguardIndependenceKeyValidation covers the register and
// edit gates through the real router: a whitespace-only independence key is
// rejected with a clear 422 on both POST and PUT, and an existing key survives.
func TestRectificationAPISafeguardIndependenceKeyValidation(t *testing.T) {
	f := newAPIFixture(t)
	_, scenario := f.createNodeScenario(t, "INDEP")
	engineer := f.clientFor("engineer")

	// Registering with blank / whitespace-only keys is rejected; nothing is stored.
	for _, key := range []string{"", "   ", "\t\n"} {
		resp := engineer.do(http.MethodPost, baseURL+"/safeguards",
			registerSafeguardBody(scenario.ID, key, "Trip "+key))
		resp.expect(t, http.StatusUnprocessableEntity, util.CodeValidation)
	}
	listResp := engineer.do(http.MethodGet,
		fmt.Sprintf("%s/safeguards?scenario_id=%d", baseURL, scenario.ID), nil)
	listResp.expect(t, http.StatusOK, "OK")
	var list dto.SafeguardListResponse
	listResp.decodeData(t, &list)
	if list.Total != 0 {
		t.Fatalf("rejected registrations must persist no safeguard, found %d", list.Total)
	}

	// A valid key registers and is normalized to upper case.
	createdResp := engineer.do(http.MethodPost, baseURL+"/safeguards",
		registerSafeguardBody(scenario.ID, "  sis-indep-01  ", "High flow trip"))
	createdResp.expect(t, http.StatusCreated, "OK")
	var created dto.SafeguardResponse
	createdResp.decodeData(t, &created)
	if created.IndependenceKey != "SIS-INDEP-01" {
		t.Fatalf("independence key should be normalized, got %q", created.IndependenceKey)
	}

	// Editing the key to blanks is rejected and the stored key survives.
	for _, blank := range []string{"", "   "} {
		resp := engineer.do(http.MethodPut, fmt.Sprintf("%s/safeguards/%d", baseURL, created.ID),
			map[string]any{"independence_key": blank})
		resp.expect(t, http.StatusUnprocessableEntity, util.CodeValidation)
	}
	getResp := engineer.do(http.MethodGet, fmt.Sprintf("%s/safeguards/%d", baseURL, created.ID), nil)
	getResp.expect(t, http.StatusOK, "OK")
	var reloaded dto.SafeguardResponse
	getResp.decodeData(t, &reloaded)
	if reloaded.IndependenceKey != "SIS-INDEP-01" {
		t.Fatalf("existing independence key must survive blank edits, got %q", reloaded.IndependenceKey)
	}
}

// TestRectificationAPICompleteRejectsKeylessSafeguard verifies the completion
// backstop: an otherwise-valid newly added safeguard that lacks an independence
// key (which the coverage deduction refuses to count) cannot close an item, and
// the rejection neither completes the item nor writes a binding. An already
// established binding on a different item is left intact.
func TestRectificationAPICompleteRejectsKeylessSafeguard(t *testing.T) {
	f := newAPIFixture(t)
	engineer := f.clientFor("engineer")
	reviewer := f.clientFor("reviewer")

	// One shared scenario with two rectification items, so both safeguards
	// legitimately target the same deviation and differ only in their key.
	_, scenario := f.createNodeScenario(t, "KEY")
	evaluationA := f.createCompletedEvaluation(t, "KEYA", scenario.ID, "P-KEYA", "cause-a", "consequence-a")
	itemA := f.generateItem(t, engineer, evaluationA.ID, "张工-A")
	evaluationB := f.createCompletedEvaluation(t, "KEYB", scenario.ID, "P-KEYB", "cause-b", "consequence-b")
	itemB := f.generateItem(t, engineer, evaluationB.ID, "张工-B")

	// Item A is completed legitimately with a keyed safeguard, establishing a
	// binding that the new validation must never touch.
	f.moveToPendingReview(t, engineer, itemA.ID)
	f.moveToPendingReview(t, engineer, itemB.ID)
	keyed := f.addSafeguard(t, "keyed-safeguard", scenario.ID, time.Now().Add(time.Minute))
	f.complete(t, reviewer, itemA.ID, []uint{keyed.ID}).expect(t, http.StatusOK, "OK")

	// A legacy keyless safeguard exists for the same deviation, created after item B.
	keyless := f.addKeylessSafeguard(t, "keyless-safeguard", scenario.ID, time.Now().Add(2*time.Minute))
	beforeB := f.getItem(t, itemB.ID)

	resp := f.complete(t, reviewer, itemB.ID, []uint{keyless.ID})
	resp.expect(t, http.StatusUnprocessableEntity, util.CodeSafeguardBinding)
	if !strings.Contains(resp.Envelope.Message, "独立性键") {
		t.Fatalf("error should prompt to fill the independence key in the ledger, got %q", resp.Envelope.Message)
	}

	// Item B is not rewritten and no binding is created for the keyless safeguard.
	f.assertItemUntouched(t, itemB.ID, beforeB)
	if f.bindingCount(t, keyless.ID) != 0 {
		t.Fatalf("a keyless safeguard must never gain a binding")
	}

	// The pre-existing binding on item A is untouched by the new validation.
	itemAReloaded := f.getItem(t, itemA.ID)
	if itemAReloaded.State != "completed" || len(itemAReloaded.Bindings) != 1 ||
		itemAReloaded.Bindings[0].SafeguardID != keyed.ID {
		t.Fatalf("existing binding must remain intact: state=%s bindings=%#v",
			itemAReloaded.State, itemAReloaded.Bindings)
	}
	if f.bindingCount(t, keyed.ID) != 1 {
		t.Fatalf("keyed safeguard must keep exactly its original binding")
	}

	// After the ledger record is corrected with a real key, item B can complete.
	if err := f.db.Model(&model.Safeguard{}).Where("id = ?", keyless.ID).
		Update("independence_key", "SIS-KEYB-TRIP").Error; err != nil {
		t.Fatalf("fill independence key: %v", err)
	}
	okResp := f.complete(t, reviewer, itemB.ID, []uint{keyless.ID})
	okResp.expect(t, http.StatusOK, "OK")
	itemBDone := f.getItem(t, itemB.ID)
	if itemBDone.State != "completed" || len(itemBDone.Bindings) != 1 {
		t.Fatalf("item B should complete once the key is fixed, got %#v", itemBDone)
	}
}
