package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"hazop-safeguard-coverage/backend/internal/algorithm"
	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/repository"
	"hazop-safeguard-coverage/backend/internal/util"

	"gorm.io/gorm"
)

// generatedChain is the shared fixture state for a scenario with one
// rectification item ready to be moved through the workflow.
type generatedChain struct {
	scenarioID uint
	itemID     uint
}

func (f *apiFixture) seedChain(t *testing.T, key string) generatedChain {
	t.Helper()
	_, scenario := f.createNodeScenario(t, key)
	evaluation := f.createCompletedEvaluation(t, key, scenario.ID, "P-"+key,
		"cause-"+key, "consequence-"+key)
	item := f.generateItem(t, f.clientFor("engineer"), evaluation.ID, "张工-"+key)
	return generatedChain{scenarioID: scenario.ID, itemID: item.ID}
}

func (f *apiFixture) moveToPendingReview(t *testing.T, client *apiClient, id uint) {
	t.Helper()
	resp := f.transition(t, client, id, "in_progress", "")
	resp.expect(t, http.StatusOK, "OK")
	resp = f.transition(t, client, id, "pending_review", "")
	resp.expect(t, http.StatusOK, "OK")
}

func (f *apiFixture) complete(t *testing.T, client *apiClient, id uint, safeguardIDs []uint) apiResponse {
	t.Helper()
	return client.do(http.MethodPost, fmt.Sprintf("%s/rectification-items/%d/complete", baseURL, id),
		map[string]any{"safeguard_ids": safeguardIDs})
}

// TestRectificationAPIGenerateFlowComplete drives the three main chains
// (generate -> transition -> complete) through the real router and middleware
// and asserts the completed item plus its binding are persisted.
func TestRectificationAPIGenerateFlowComplete(t *testing.T) {
	f := newAPIFixture(t)
	chain := f.seedChain(t, "MAIN")
	engineer := f.clientFor("engineer")
	reviewer := f.clientFor("reviewer")

	// Chain 1: generate is already done by seedChain; verify the read endpoint
	// and list/summary return the pending item with the generating user recorded.
	getResp := engineer.do(http.MethodGet, fmt.Sprintf("%s/rectification-items/%d", baseURL, chain.itemID), nil)
	getResp.expect(t, http.StatusOK, "OK")
	var item dto.RectificationItemResponse
	getResp.decodeData(t, &item)
	if item.State != "pending" || item.OwnerName != "张工-MAIN" {
		t.Fatalf("unexpected generated item: %#v", item)
	}
	if item.GeneratedBy != f.users["engineer"].ID || item.GeneratedByName != "engineer" {
		t.Fatalf("generated_by must come from the authenticated engineer: %#v", item)
	}

	listResp := engineer.do(http.MethodGet,
		fmt.Sprintf("%s/rectification-items?state=pending", baseURL), nil)
	listResp.expect(t, http.StatusOK, "OK")
	var list dto.RectificationListResponse
	listResp.decodeData(t, &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != chain.itemID {
		t.Fatalf("list should contain the single pending item, got %#v", list)
	}

	summaryResp := engineer.do(http.MethodGet, baseURL+"/rectification-items/summary", nil)
	summaryResp.expect(t, http.StatusOK, "OK")
	var summary dto.RectificationSummaryResponse
	summaryResp.decodeData(t, &summary)
	if summary.Total != 1 || summary.Incomplete != 1 || summary.ByState["pending"] != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}

	// Chain 2: flow pending -> in_progress -> pending_review.
	f.moveToPendingReview(t, engineer, chain.itemID)
	moved := f.getItem(t, chain.itemID)
	if moved.State != "pending_review" {
		t.Fatalf("item should be pending_review, got %s", moved.State)
	}

	// Chain 3: complete with a newly added, valid safeguard.
	// Created strictly after the rectification item so it counts as a new entry.
	safeguard := f.addSafeguard(t, "main-safeguard", chain.scenarioID, time.Now().Add(time.Minute))
	completeResp := f.complete(t, reviewer, chain.itemID, []uint{safeguard.ID})
	completeResp.expect(t, http.StatusOK, "OK")
	var completed dto.RectificationItemResponse
	completeResp.decodeData(t, &completed)
	if completed.State != "completed" {
		t.Fatalf("expected completed state, got %s", completed.State)
	}
	if len(completed.Bindings) != 1 || completed.Bindings[0].SafeguardID != safeguard.ID {
		t.Fatalf("expected one binding to safeguard %d, got %#v", safeguard.ID, completed.Bindings)
	}
	if completed.CompletedBy == nil || *completed.CompletedBy != f.users["reviewer"].ID {
		t.Fatalf("completed_by must record the authenticated reviewer, got %#v", completed.CompletedBy)
	}

	// Binding record is durable and points at the now-completed item.
	stored, err := f.items.FindActiveBindings(context.Background(), []uint{safeguard.ID})
	if err != nil || len(stored) != 1 || stored[0].ItemID != chain.itemID {
		t.Fatalf("expected one durable binding on item %d, got %#v (%v)", chain.itemID, stored, err)
	}

	// A successful HTTP write produces an http_write audit row.
	var writeAudits int64
	if err := f.db.Model(&model.AuditLog{}).
		Where("entity_type = ? AND action = ?", "rectification_items", "http_write").
		Count(&writeAudits).Error; err != nil {
		t.Fatalf("count http_write audits: %v", err)
	}
	if writeAudits == 0 {
		t.Fatalf("successful writes must be audited by the HTTP audit middleware")
	}
}

// TestRectificationAPIRejectsUnauthenticated covers the missing-token,
// malformed-token and forged-signature paths. Every protected route must
// answer 401 UNAUTHORIZED through the real auth middleware and must not create
// any rectification item.
func TestRectificationAPIRejectsUnauthenticated(t *testing.T) {
	f := newAPIFixture(t)
	chain := f.seedChain(t, "AUTH")
	f.moveToPendingReview(t, f.clientFor("engineer"), chain.itemID)
	safeguard := f.addSafeguard(t, "auth-safeguard", chain.scenarioID, time.Now().Add(time.Minute))

	anon := f.anonClient()
	paths := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, baseURL + "/rectification-items", nil},
		{http.MethodGet, fmt.Sprintf("%s/rectification-items/%d", baseURL, chain.itemID), nil},
		{http.MethodPost, baseURL + "/rectification-items/generate", map[string]any{"evaluation_id": 1}},
		{http.MethodPost, fmt.Sprintf("%s/rectification-items/%d/transition", baseURL, chain.itemID),
			map[string]any{"to_state": "in_progress"}},
		{http.MethodPost, fmt.Sprintf("%s/rectification-items/%d/complete", baseURL, chain.itemID),
			map[string]any{"safeguard_ids": []uint{safeguard.ID}}},
	}
	for _, tc := range paths {
		resp := anon.do(tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusUnauthorized || resp.Envelope.Code != string(util.CodeUnauthorized) {
			t.Fatalf("%s %s without token: expected 401 UNAUTHORIZED, got %d %s",
				tc.method, tc.path, resp.StatusCode, resp.Envelope.Code)
		}
	}

	// A garbage bearer value and a token signed with the wrong secret are also
	// rejected at the authentication layer, before RBAC runs.
	garbage := &apiClient{fixture: f, token: "not-a-jwt"}
	resp := garbage.do(http.MethodPost, fmt.Sprintf("%s/rectification-items/%d/complete", baseURL, chain.itemID),
		map[string]any{"safeguard_ids": []uint{safeguard.ID}})
	resp.expect(t, http.StatusUnauthorized, util.CodeUnauthorized)

	forged := &apiClient{fixture: f, token: f.forgedToken()}
	resp = forged.do(http.MethodPost, baseURL+"/rectification-items/generate", map[string]any{"evaluation_id": 1})
	resp.expect(t, http.StatusUnauthorized, util.CodeUnauthorized)

	// No item was generated by the unauthorized generate calls.
	if f.getItem(t, chain.itemID).State != "pending_review" {
		t.Fatalf("unauthorized requests must not change item state")
	}
	if f.bindingCount(t, safeguard.ID) != 0 {
		t.Fatalf("unauthorized complete must not create a binding")
	}
	var itemCount int64
	if err := f.db.Model(&model.RectificationItem{}).Count(&itemCount).Error; err != nil {
		t.Fatalf("count items: %v", err)
	}
	if itemCount != 1 {
		t.Fatalf("unauthorized generate must not create items, found %d", itemCount)
	}
}

// TestRectificationAPIRejectsWrongRole verifies the RBAC middleware returns
// 403 FORBIDDEN for role/action mismatches without touching the record.
func TestRectificationAPIRejectsWrongRole(t *testing.T) {
	f := newAPIFixture(t)
	chain := f.seedChain(t, "RBAC")
	f.moveToPendingReview(t, f.clientFor("engineer"), chain.itemID)
	safeguard := f.addSafeguard(t, "rbac-safeguard", chain.scenarioID, time.Now().Add(time.Minute))
	before := f.getItem(t, chain.itemID)

	// A safety reviewer lacks rectification:write and may not generate items.
	resp := f.clientFor("reviewer").do(http.MethodPost, baseURL+"/rectification-items/generate",
		map[string]any{"evaluation_id": 1})
	resp.expect(t, http.StatusForbidden, util.CodeForbidden)

	// A process engineer lacks rectification:review and may not complete an item.
	resp = f.clientFor("engineer").do(http.MethodPost,
		fmt.Sprintf("%s/rectification-items/%d/complete", baseURL, chain.itemID),
		map[string]any{"safeguard_ids": []uint{safeguard.ID}})
	resp.expect(t, http.StatusForbidden, util.CodeForbidden)

	// The read-only auditor can read but cannot transition.
	resp = f.clientFor("auditor").do(http.MethodGet, fmt.Sprintf("%s/rectification-items/%d", baseURL, chain.itemID), nil)
	resp.expect(t, http.StatusOK, "OK")
	resp = f.clientFor("auditor").do(http.MethodPost,
		fmt.Sprintf("%s/rectification-items/%d/transition", baseURL, chain.itemID),
		map[string]any{"to_state": "voided", "reason": "auditor attempt"})
	resp.expect(t, http.StatusForbidden, util.CodeForbidden)

	// Nothing changed and no binding was created by the forbidden writes.
	f.assertItemUntouched(t, chain.itemID, before)
	if f.bindingCount(t, safeguard.ID) != 0 {
		t.Fatalf("forbidden complete must not create a binding")
	}
}

// TestRectificationAPIRejectsIllegalTransition covers state-machine violations
// surfaced both by request binding (unknown target state -> 400) and by the
// service transition table (valid enum, impossible move -> 409).
func TestRectificationAPIRejectsIllegalTransition(t *testing.T) {
	f := newAPIFixture(t)
	chain := f.seedChain(t, "FLOW")
	engineer := f.clientFor("engineer")
	before := f.getItem(t, chain.itemID)

	// pending -> completed is not in the oneof set, so binding rejects it first.
	resp := f.transition(t, engineer, chain.itemID, "completed", "")
	resp.expect(t, http.StatusBadRequest, util.CodeValidation)

	// pending -> pending_review is a syntactically valid enum but skips
	// in_progress, so the state machine rejects it.
	resp = f.transition(t, engineer, chain.itemID, "pending_review", "")
	resp.expect(t, http.StatusConflict, util.CodeStateTransition)

	// voiding requires a reason; the state machine path enforces the 422.
	resp = f.transition(t, engineer, chain.itemID, "voided", "x")
	resp.expect(t, http.StatusUnprocessableEntity, util.CodeValidation)

	// A legal move forward then completes; once completed no further
	// transition is possible.
	f.moveToPendingReview(t, engineer, chain.itemID)
	safeguard := f.addSafeguard(t, "flow-safeguard", chain.scenarioID, time.Now().Add(time.Minute))
	completeResp := f.complete(t, f.clientFor("reviewer"), chain.itemID, []uint{safeguard.ID})
	completeResp.expect(t, http.StatusOK, "OK")

	resp = f.transition(t, engineer, chain.itemID, "in_progress", "")
	resp.expect(t, http.StatusConflict, util.CodeStateTransition)

	// The failed early transitions left the original pending record untouched;
	// after the legal completion the item and its single binding stay intact.
	finished := f.getItem(t, chain.itemID)
	if finished.State != "completed" || len(finished.Bindings) != 1 {
		t.Fatalf("item should be completed with one binding, got state=%s bindings=%d",
			finished.State, len(finished.Bindings))
	}
	if before.State != "pending" {
		t.Fatalf("sanity: record should have started pending, got %s", before.State)
	}
}

// TestRectificationAPICompleteRequiresBinding covers the missing-binding gate:
// completion from pending_review must bind at least one newly added, valid
// safeguard belonging to the same scenario.
func TestRectificationAPICompleteRequiresBinding(t *testing.T) {
	f := newAPIFixture(t)
	chain := f.seedChain(t, "BIND")
	engineer := f.clientFor("engineer")
	reviewer := f.clientFor("reviewer")
	f.moveToPendingReview(t, engineer, chain.itemID)
	before := f.getItem(t, chain.itemID)

	// No safeguard ids at all.
	resp := f.complete(t, reviewer, chain.itemID, []uint{})
	resp.expect(t, http.StatusUnprocessableEntity, util.CodeSafeguardBinding)

	// An explicit empty array serialised the same way must also be rejected.
	resp = reviewer.do(http.MethodPost,
		fmt.Sprintf("%s/rectification-items/%d/complete", baseURL, chain.itemID),
		map[string]any{"safeguard_ids": []uint{}})
	resp.expect(t, http.StatusUnprocessableEntity, util.CodeSafeguardBinding)

	// A safeguard that belongs to a different scenario cannot close this item.
	_, otherScenario := f.createNodeScenario(t, "BINDOTHER")
	foreign := f.addSafeguard(t, "foreign-safeguard", otherScenario.ID, time.Now().Add(time.Minute))
	resp = f.complete(t, reviewer, chain.itemID, []uint{foreign.ID})
	resp.expect(t, http.StatusUnprocessableEntity, util.CodeSafeguardBinding)

	// A safeguard created before the rectification item is not "newly added".
	old := f.addSafeguard(t, "too-old-safeguard", chain.scenarioID, before.CreatedAt.Add(-time.Minute))
	resp = f.complete(t, reviewer, chain.itemID, []uint{old.ID})
	resp.expect(t, http.StatusUnprocessableEntity, util.CodeSafeguardBinding)

	// Completing from the wrong source state (item currently pending_review is
	// moved back to in_progress first) is a state conflict, not a binding error.
	returnResp := reviewer.do(http.MethodPost,
		fmt.Sprintf("%s/rectification-items/%d/return", baseURL, chain.itemID),
		map[string]any{"reason": "please add more evidence"})
	returnResp.expect(t, http.StatusOK, "OK")
	afterReturn := f.getItem(t, chain.itemID)
	valid := f.addSafeguard(t, "bind-valid-safeguard", chain.scenarioID, time.Now().Add(time.Minute))
	resp = f.complete(t, reviewer, chain.itemID, []uint{valid.ID})
	resp.expect(t, http.StatusConflict, util.CodeStateTransition)

	// All rejected attempts must leave the item un-completed and without any
	// binding rows; the state after the legal return must be preserved.
	after := f.getItem(t, chain.itemID)
	if after.State == "completed" {
		t.Fatalf("item must not be completed after rejected attempts")
	}
	for _, sg := range []uint{foreign.ID, old.ID, valid.ID} {
		if f.bindingCount(t, sg) != 0 {
			t.Fatalf("rejected completion must not bind safeguard %d", sg)
		}
	}
	f.assertItemUntouched(t, chain.itemID, afterReturn)
}

// TestRectificationAPIDuplicateBindingRejected first closes one item with a
// safeguard, then proves the same safeguard cannot close a second item, and
// cannot be re-bound to the already completed item. The completed item and its
// original binding remain exactly as they were.
func TestRectificationAPIDuplicateBindingRejected(t *testing.T) {
	f := newAPIFixture(t)
	first := f.seedChain(t, "DUP1")
	// A second chain in the same scenario so both items may legally use the
	// same safeguard from a domain-rule perspective.
	evaluation2 := f.createCompletedEvaluation(t, "DUP1B", first.scenarioID, "P-DUP1B",
		"cause-dup-2", "consequence-dup-2")
	secondItem := f.generateItem(t, f.clientFor("engineer"), evaluation2.ID, "张工-DUP2")

	engineer := f.clientFor("engineer")
	reviewer := f.clientFor("reviewer")
	f.moveToPendingReview(t, engineer, first.itemID)
	f.moveToPendingReview(t, engineer, secondItem.ID)

	shared := f.addSafeguard(t, "shared-safeguard", first.scenarioID, time.Now().Add(time.Minute))
	firstResp := f.complete(t, reviewer, first.itemID, []uint{shared.ID})
	firstResp.expect(t, http.StatusOK, "OK")
	firstAfter := f.getItem(t, first.itemID)
	if firstAfter.State != "completed" || len(firstAfter.Bindings) != 1 {
		t.Fatalf("first item should be completed with the shared safeguard")
	}

	// Reusing the already-bound safeguard on the second item must fail 409.
	secondBefore := f.getItem(t, secondItem.ID)
	resp := f.complete(t, reviewer, secondItem.ID, []uint{shared.ID})
	resp.expect(t, http.StatusConflict, util.CodeSafeguardBound)

	// The error names the item the safeguard already closed.
	if want := fmt.Sprintf("整改项 #%d", first.itemID); !strings.Contains(resp.Envelope.Message, want) {
		t.Fatalf("error message should reference %s, got %q", want, resp.Envelope.Message)
	}
	f.assertItemUntouched(t, secondItem.ID, secondBefore)

	// Re-completing the already completed item (with a fresh safeguard) is a
	// state conflict, and the completed record must keep its original binding.
	another := f.addSafeguard(t, "another-safeguard", first.scenarioID, time.Now().Add(2*time.Minute))
	resp = f.complete(t, reviewer, first.itemID, []uint{another.ID})
	resp.expect(t, http.StatusConflict, util.CodeStateTransition)

	final := f.getItem(t, first.itemID)
	if final.State != "completed" || len(final.Bindings) != 1 || final.Bindings[0].SafeguardID != shared.ID {
		t.Fatalf("completed item must retain only the original binding, got state=%s bindings=%#v",
			final.State, final.Bindings)
	}
	if f.bindingCount(t, another.ID) != 0 {
		t.Fatalf("the fresh safeguard must never be bound after the rejected re-complete")
	}
}

// prepareTwoReviewItems builds two pending-review items in the same scenario
// sharing one newly added safeguard, the precondition for a binding race.
func (f *apiFixture) prepareTwoReviewItems(t *testing.T, key string) (first, second uint, safeguard model.Safeguard) {
	t.Helper()
	chain := f.seedChain(t, key)
	evaluation2 := f.createCompletedEvaluation(t, key+"B", chain.scenarioID, "P-"+key+"B",
		"cause-"+key+"-2", "consequence-"+key+"-2")
	secondItem := f.generateItem(t, f.clientFor("engineer"), evaluation2.ID, "张工-"+key+"2")
	engineer := f.clientFor("engineer")
	f.moveToPendingReview(t, engineer, chain.itemID)
	f.moveToPendingReview(t, engineer, secondItem.ID)
	safeguard = f.addSafeguard(t, "raced-"+key, chain.scenarioID, time.Now().Add(time.Minute))
	return chain.itemID, secondItem.ID, safeguard
}

// assertSingleWinner validates the race invariant for a pair of completion
// results: exactly one 200 whose item is completed and owns the sole binding,
// and one 409 SAFEGUARD_ALREADY_BOUND whose item stays pending_review.
func (f *apiFixture) assertSingleWinner(t *testing.T, itemIDs [2]uint, results [2]apiResponse, safeguardID uint) {
	t.Helper()
	winner, losers := -1, 0
	for i, result := range results {
		switch {
		case result.StatusCode == http.StatusOK && result.Envelope.Code == "OK":
			if winner != -1 {
				t.Fatalf("both requests succeeded; only one may win")
			}
			winner = i
		case result.StatusCode == http.StatusConflict && result.Envelope.Code == string(util.CodeSafeguardBound):
			losers++
		default:
			t.Fatalf("unexpected race outcome for item %d: %d %s (%s)",
				itemIDs[i], result.StatusCode, result.Envelope.Code, result.Envelope.Message)
		}
	}
	if winner == -1 || losers != 1 {
		t.Fatalf("exactly one success and one SAFEGUARD_ALREADY_BOUND required, winner=%d losers=%d", winner, losers)
	}
	for i, id := range itemIDs {
		item := f.getItem(t, id)
		if i == winner {
			if item.State != "completed" {
				t.Fatalf("winning item %d must be completed, got %s", id, item.State)
			}
			if len(item.Bindings) != 1 || item.Bindings[0].SafeguardID != safeguardID {
				t.Fatalf("winning item %d must own the unique binding, got %#v", id, item.Bindings)
			}
			continue
		}
		// The losing request must not have rewritten its rectification item:
		// no state change, no completion marker, no binding row.
		if item.State != "pending_review" {
			t.Fatalf("losing item %d state must stay pending_review, got %s", id, item.State)
		}
		if item.CompletedBy != nil || item.CompletedAt != nil || len(item.Bindings) != 0 {
			t.Fatalf("losing item %d must not be completed or gain bindings: %#v", id, item)
		}
	}
	if count := f.bindingCount(t, safeguardID); count != 1 {
		t.Fatalf("exactly one binding row may exist, got %d", count)
	}
}

// TestRectificationAPIConcurrentBindingRace fires two completion requests
// simultaneously against a multi-connection WAL database, so both requests
// enter their write transactions on separate connections and genuinely race
// for the safeguard unique index. Exactly one may succeed; the loser is
// rejected by the database-level constraint and its item is left untouched.
func TestRectificationAPIConcurrentBindingRace(t *testing.T) {
	f := newAPIFixture(t, withPoolSize(8))
	firstID, secondID, safeguard := f.prepareTwoReviewItems(t, "RACE")
	results := f.raceCompletions(t, []uint{firstID, secondID}, safeguard.ID)
	f.assertSingleWinner(t, [2]uint{firstID, secondID},
		[2]apiResponse{results[0], results[1]}, safeguard.ID)
}

// TestRectificationAPIConcurrentBindingRaceRepeated reruns the multi-connection
// race across independent databases so the unique-index guarantee is exercised
// repeatedly rather than passing on a single scheduling outcome.
func TestRectificationAPIConcurrentBindingRaceRepeated(t *testing.T) {
	const rounds = 5
	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			f := newAPIFixture(t, withPoolSize(8))
			key := fmt.Sprintf("RR%d", round)
			firstID, secondID, safeguard := f.prepareTwoReviewItems(t, key)
			results := f.raceCompletions(t, []uint{firstID, secondID}, safeguard.ID)
			f.assertSingleWinner(t, [2]uint{firstID, secondID},
				[2]apiResponse{results[0], results[1]}, safeguard.ID)
		})
	}
}

// TestRectificationAPIStaggeredBindingHitsUniqueIndex deliberately offsets the
// commit timing: the losing request completes its pre-check (FindActiveBindings
// sees no conflict) and parks inside its open transaction; the winner then
// commits the binding; only afterwards is the loser released to INSERT. The
// pre-check can no longer protect it, so rejection must come from the safeguard
// unique index. This proves the database constraint is the real backstop.
func TestRectificationAPIStaggeredBindingHitsUniqueIndex(t *testing.T) {
	loserTxEntered := make(chan struct{})
	releaseLoser := make(chan struct{})
	uniqueViolations := make(chan struct{}, 4)
	f := newAPIFixture(t,
		withPoolSize(8),
		withExtraMiddleware(testRoleMiddleware()),
		withItemRepoOverride(func(db *gorm.DB) repository.RectificationItemRepository {
			return newStaggeredRepo(db, loserTxEntered, releaseLoser, uniqueViolations)
		}),
	)
	firstID, secondID, safeguard := f.prepareTwoReviewItems(t, "STAGGER")
	reviewerToken := f.login("reviewer")

	// Loser parks inside its transaction after a clean pre-check.
	loserResult := make(chan apiResponse, 1)
	go func() {
		client := (&apiClient{fixture: f, token: reviewerToken}).withHeader(testRoleHeader, "loser")
		loserResult <- f.complete(t, client, secondID, []uint{safeguard.ID})
	}()
	select {
	case <-loserTxEntered:
	case <-time.After(5 * time.Second):
		t.Fatalf("losing request never reached the write phase")
	}

	// Winner runs to completion and commits the only binding while the loser's
	// pre-check is already behind it.
	winnerClient := &apiClient{fixture: f, token: reviewerToken}
	winnerResp := f.complete(t, winnerClient, firstID, []uint{safeguard.ID})
	winnerResp.expect(t, http.StatusOK, "OK")
	if winner := f.getItem(t, firstID); winner.State != "completed" || len(winner.Bindings) != 1 {
		t.Fatalf("winner must complete with one binding, got state=%s bindings=%d",
			winner.State, len(winner.Bindings))
	}

	// Release the loser to run its now-duplicate INSERT; the unique index (not
	// the pre-check) must reject it.
	close(releaseLoser)
	loserResp := <-loserResult
	loserResp.expect(t, http.StatusConflict, util.CodeSafeguardBound)

	// The loser passed its pre-check yet still failed, so the rejection must be
	// the database-level unique index on rectification_bindings.safeguard_id.
	select {
	case <-uniqueViolations:
	case <-time.After(2 * time.Second):
		t.Fatalf("losing INSERT was expected to hit the safeguard unique index, but no unique violation was recorded")
	}

	// The loser's rectification item is not rewritten by the failed commit.
	loser := f.getItem(t, secondID)
	if loser.State != "pending_review" {
		t.Fatalf("losing item must stay pending_review after unique-index rejection, got %s", loser.State)
	}
	if loser.CompletedBy != nil || loser.CompletedAt != nil || len(loser.Bindings) != 0 {
		t.Fatalf("losing item must gain no completion marker or bindings: %#v", loser)
	}
	if count := f.bindingCount(t, safeguard.ID); count != 1 {
		t.Fatalf("the unique index must leave exactly one binding, got %d", count)
	}
	if stored, _ := f.items.FindActiveBindings(context.Background(), []uint{safeguard.ID}); len(stored) != 1 || stored[0].ItemID != firstID {
		t.Fatalf("the surviving binding must belong to the winning item %d", firstID)
	}
}

func (f *apiFixture) raceCompletions(t *testing.T, itemIDs []uint, safeguardID uint) []apiResponse {
	t.Helper()
	// Warm the shared token cache on the originating goroutine so concurrent
	// requests only read it (avoids racing the tokens map under -race).
	reviewerToken := f.login("reviewer")
	var wg sync.WaitGroup
	results := make([]apiResponse, len(itemIDs))
	start := make(chan struct{})
	for i, id := range itemIDs {
		wg.Add(1)
		go func(index int, itemID uint) {
			defer wg.Done()
			client := &apiClient{fixture: f, token: reviewerToken}
			<-start
			results[index] = f.complete(t, client, itemID, []uint{safeguardID})
		}(i, id)
	}
	close(start)
	wg.Wait()
	return results
}

// TestRectificationAPIGenerateIdempotentOnRepost ensures a repeated generate
// against the same completed evaluation reports the existing item as skipped
// rather than creating a duplicate through the HTTP layer.
func TestRectificationAPIGenerateIdempotentOnRepost(t *testing.T) {
	f := newAPIFixture(t)
	chain := f.seedChain(t, "IDEM")
	evaluation, err := f.evaluations.GetByID(context.Background(),
		f.getItem(t, chain.itemID).EvaluationID)
	if err != nil {
		t.Fatalf("load evaluation: %v", err)
	}
	resp := f.clientFor("engineer").do(http.MethodPost,
		baseURL+"/rectification-items/generate",
		map[string]any{"evaluation_id": evaluation.ID, "owner_name": "张工-IDEM"})
	resp.expect(t, http.StatusCreated, "OK")
	var generated dto.GenerateRectificationResponse
	resp.decodeData(t, &generated)
	if len(generated.Created) != 0 || len(generated.Skipped) != 1 {
		t.Fatalf("repost must create 0 and skip 1, got %d created %d skipped",
			len(generated.Created), len(generated.Skipped))
	}
	if generated.Skipped[0].ExistingItemID != chain.itemID {
		t.Fatalf("skipped entry must reference item %d, got %d",
			chain.itemID, generated.Skipped[0].ExistingItemID)
	}
	var count int64
	if err := f.db.Model(&model.RectificationItem{}).Count(&count).Error; err != nil {
		t.Fatalf("count items: %v", err)
	}
	if count != 1 {
		t.Fatalf("repost must not create a second item, found %d", count)
	}
}

// TestRectificationAPIGenerateFromNonCompletedEvaluation confirms the HTTP
// layer surfaces the 409 CONFLICT business error when the source evaluation is
// not in a generatable state.
func TestRectificationAPIGenerateFromNonCompletedEvaluation(t *testing.T) {
	f := newAPIFixture(t)
	_, scenario := f.createNodeScenario(t, "QUEUED")
	now := time.Now().UTC()
	queued := model.CoverageEvaluation{
		ScenarioID: scenario.ID, AlgorithmVersion: algorithm.Version,
		InputSnapshot: "{}", InputHash: util.HashString("queued-eval"),
		UncoveredPaths: "[]", DeduplicatedSafeguards: "[]",
		RiskRankBefore: "high", RiskRankAfter: "high",
		EvaluationState: "queued", Explanation: "{}",
		EvaluatedBy: f.users["engineer"].ID, EvaluatedByName: "engineer", EvaluatedAt: now,
		IdempotencyKey: "idem-queued", CreatedAt: now, UpdatedAt: now,
	}
	if err := f.evaluations.Create(context.Background(), &queued); err != nil {
		t.Fatalf("create queued evaluation: %v", err)
	}
	resp := f.clientFor("engineer").do(http.MethodPost,
		baseURL+"/rectification-items/generate",
		map[string]any{"evaluation_id": queued.ID, "owner_name": "张工-QUEUED"})
	resp.expect(t, http.StatusConflict, util.CodeConflict)
	var count int64
	if err := f.db.Model(&model.RectificationItem{}).Count(&count).Error; err != nil {
		t.Fatalf("count items: %v", err)
	}
	if count != 0 {
		t.Fatalf("no item may be generated from a queued evaluation, found %d", count)
	}
}

// TestRectificationAPIOwnerValidation exercises the responsible-person rules
// end to end: blank owners are rejected on generate and edit, an already
// assigned owner cannot be overwritten with blanks, an unassigned pending item
// cannot enter in_progress, and edits to voided/completed records stay blocked.
func TestRectificationAPIOwnerValidation(t *testing.T) {
	f := newAPIFixture(t)
	_, scenario := f.createNodeScenario(t, "OWNER")
	evaluation := f.createCompletedEvaluation(t, "OWNER", scenario.ID, "P-OWNER",
		"cause-owner", "consequence-owner")
	engineer := f.clientFor("engineer")

	// Blank or whitespace-only owners are rejected at generation; nothing is created.
	for _, owner := range []string{"", "   ", "\t\n"} {
		resp := engineer.do(http.MethodPost, baseURL+"/rectification-items/generate",
			map[string]any{"evaluation_id": evaluation.ID, "owner_name": owner})
		resp.expect(t, http.StatusUnprocessableEntity, util.CodeValidation)
	}
	var count int64
	if err := f.db.Model(&model.RectificationItem{}).Count(&count).Error; err != nil {
		t.Fatalf("count items: %v", err)
	}
	if count != 0 {
		t.Fatalf("blank-owner generations must create no item, found %d", count)
	}

	// A valid owner generates the item.
	item := f.generateItem(t, engineer, evaluation.ID, "张工-OWNER")

	// An assigned owner cannot be replaced with blanks through PUT.
	for _, blank := range []string{"", "   "} {
		resp := engineer.do(http.MethodPut,
			fmt.Sprintf("%s/rectification-items/%d", baseURL, item.ID),
			map[string]any{"owner_name": blank})
		resp.expect(t, http.StatusUnprocessableEntity, util.CodeValidation)
	}
	getResp := engineer.do(http.MethodGet,
		fmt.Sprintf("%s/rectification-items/%d", baseURL, item.ID), nil)
	getResp.expect(t, http.StatusOK, "OK")
	var current dto.RectificationItemResponse
	getResp.decodeData(t, &current)
	if current.OwnerName != "张工-OWNER" {
		t.Fatalf("assigned owner must survive blank updates, got %q", current.OwnerName)
	}

	// A legacy ownerless pending item cannot move to in_progress until an owner is set.
	legacy := f.generateItem(t, engineer,
		f.createCompletedEvaluation(t, "OWNERLESS", scenario.ID, "P-OWNERLESS",
			"cause-ownerless", "consequence-ownerless").ID,
		"临时责任人")
	if err := f.db.Model(&model.RectificationItem{}).Where("id = ?", legacy.ID).
		Update("owner_name", "   ").Error; err != nil {
		t.Fatalf("simulate legacy ownerless item: %v", err)
	}
	beforeStart := f.getItem(t, legacy.ID)
	resp := f.transition(t, engineer, legacy.ID, "in_progress", "")
	resp.expect(t, http.StatusUnprocessableEntity, util.CodeValidation)
	f.assertItemUntouched(t, legacy.ID, beforeStart)

	assignResp := engineer.do(http.MethodPut,
		fmt.Sprintf("%s/rectification-items/%d", baseURL, legacy.ID),
		map[string]any{"owner_name": "赵工-OWNER"})
	assignResp.expect(t, http.StatusOK, "OK")
	resp = f.transition(t, engineer, legacy.ID, "in_progress", "")
	resp.expect(t, http.StatusOK, "OK")
	if f.getItem(t, legacy.ID).State != "in_progress" {
		t.Fatalf("item should start after an owner is assigned")
	}

	// Edits to a completed record remain blocked even with a valid new owner.
	completedChain := f.seedChain(t, "OWNERCLOSED")
	f.moveToPendingReview(t, engineer, completedChain.itemID)
	safeguard := f.addSafeguard(t, "owner-closed-safeguard", completedChain.scenarioID, time.Now().Add(time.Minute))
	f.complete(t, f.clientFor("reviewer"), completedChain.itemID, []uint{safeguard.ID}).
		expect(t, http.StatusOK, "OK")
	editClosed := engineer.do(http.MethodPut,
		fmt.Sprintf("%s/rectification-items/%d", baseURL, completedChain.itemID),
		map[string]any{"owner_name": "新责任人"})
	editClosed.expect(t, http.StatusConflict, util.CodeStateTransition)
	closed := f.getItem(t, completedChain.itemID)
	if closed.State != "completed" || len(closed.Bindings) != 1 {
		t.Fatalf("completed record must stay intact after rejected edit, state=%s bindings=%d",
			closed.State, len(closed.Bindings))
	}

	// Edits to a voided record remain blocked as well.
	voidedChain := f.seedChain(t, "OWNERVOID")
	resp = f.transition(t, engineer, voidedChain.itemID, "voided", "场景已取消整改")
	resp.expect(t, http.StatusOK, "OK")
	editVoided := engineer.do(http.MethodPut,
		fmt.Sprintf("%s/rectification-items/%d", baseURL, voidedChain.itemID),
		map[string]any{"owner_name": "新责任人"})
	editVoided.expect(t, http.StatusConflict, util.CodeStateTransition)
	if f.getItem(t, voidedChain.itemID).State != "voided" {
		t.Fatalf("voided record must remain voided after rejected edit")
	}
}
