package constants

import "testing"

func TestScenarioTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to ScenarioState
		allowed  bool
	}{
		{ScenarioDraft, ScenarioAnalyzed, true},
		{ScenarioAnalyzed, ScenarioVerified, true},
		{ScenarioVerified, ScenarioAccepted, true},
		{ScenarioAnalyzed, ScenarioRework, true},
		{ScenarioVerified, ScenarioRework, true},
		{ScenarioRework, ScenarioAnalyzed, true},
		{ScenarioDraft, ScenarioAccepted, false},
		{ScenarioAccepted, ScenarioRework, false},
		{ScenarioVerified, ScenarioAnalyzed, false},
	}
	for _, test := range tests {
		if actual := CanTransitionScenario(test.from, test.to); actual != test.allowed {
			t.Errorf("%s -> %s: got %t, want %t", test.from, test.to, actual, test.allowed)
		}
	}
}

func TestCoverageTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to CoverageState
		allowed  bool
	}{
		{CoverageQueued, CoverageRunning, true},
		{CoverageRunning, CoverageCompleted, true},
		{CoverageRunning, CoverageFailed, true},
		{CoverageCompleted, CoverageConfirmed, true},
		{CoverageCompleted, CoverageVoided, true},
		{CoverageFailed, CoverageVoided, true},
		{CoverageConfirmed, CoverageVoided, true},
		{CoverageQueued, CoverageCompleted, false},
		{CoverageCompleted, CoverageRunning, false},
		{CoverageVoided, CoverageCompleted, false},
	}
	for _, test := range tests {
		if actual := CanTransitionCoverage(test.from, test.to); actual != test.allowed {
			t.Errorf("%s -> %s: got %t, want %t", test.from, test.to, actual, test.allowed)
		}
	}
}

func TestRectificationTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to RectificationState
		allowed  bool
	}{
		{RectificationPending, RectificationInProgress, true},
		{RectificationPending, RectificationVoided, true},
		{RectificationInProgress, RectificationPendingReview, true},
		{RectificationInProgress, RectificationVoided, true},
		{RectificationPendingReview, RectificationCompleted, true},
		{RectificationPendingReview, RectificationInProgress, true},
		{RectificationPendingReview, RectificationVoided, true},
		{RectificationPending, RectificationCompleted, false},
		{RectificationPending, RectificationPendingReview, false},
		{RectificationInProgress, RectificationCompleted, false},
		{RectificationInProgress, RectificationPending, false},
		{RectificationCompleted, RectificationInProgress, false},
		{RectificationCompleted, RectificationVoided, false},
		{RectificationVoided, RectificationPending, false},
	}
	for _, test := range tests {
		if actual := CanTransitionRectification(test.from, test.to); actual != test.allowed {
			t.Errorf("%s -> %s: got %t, want %t", test.from, test.to, actual, test.allowed)
		}
	}
}
