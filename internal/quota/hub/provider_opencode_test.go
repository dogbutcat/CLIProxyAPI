package hub

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

func TestOpenCodeFixtureProjections(t *testing.T) {
	completedAt := openCodeTestCompletedAt()
	tests := []struct {
		name                string
		thresholdConfigured bool
		threshold           float64
		completeness        Completeness
		score               float64
		outcome             Outcome
		resetAt             time.Time
	}{
		{name: "score_only", completeness: ScoreOnly, score: 5},
		{
			name:                "threshold_exhaustion",
			thresholdConfigured: true,
			threshold:           5,
			completeness:        ExhaustionEvidence,
			score:               5,
			outcome:             Exhausted,
			resetAt:             time.Date(2026, time.January, 10, 16, 0, 0, 0, time.UTC),
		},
		{
			name:                "healthy_recovery",
			thresholdConfigured: true,
			threshold:           5,
			completeness:        AuthoritativeSnapshot,
			score:               6,
			outcome:             Healthy,
		},
		{
			name:                "reset_selection",
			thresholdConfigured: true,
			threshold:           5,
			completeness:        ExhaustionEvidence,
			score:               3,
			outcome:             Exhausted,
			resetAt:             time.Date(2026, time.January, 10, 15, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := readOpenCodeFixture(t, tt.name+".json")
			before := cloneOpenCodeTestResult(t, result)
			observation, available, err := activeOpenCodeResultAdapter().observeResult(openCodeResultMetadata{
				Source:              OpenCodeScheduled,
				CompletedAt:         completedAt,
				ThresholdConfigured: tt.thresholdConfigured,
				Threshold:           tt.threshold,
			}, result)
			if err != nil || !available {
				t.Fatalf("observeResult() = (%+v, %v, %v)", observation, available, err)
			}
			assertOpenCodeObservation(t, observation, tt.completeness, tt.score, tt.outcome, tt.resetAt)
			if !reflect.DeepEqual(result, before) {
				t.Fatal("adapter mutated the retained poll result")
			}
		})
	}
}

func TestOpenCodeManualAndScheduledSourcesHaveParity(t *testing.T) {
	result := readOpenCodeFixture(t, "threshold_exhaustion.json")
	metadata := openCodeResultMetadata{
		Source:              OpenCodeManual,
		CompletedAt:         openCodeTestCompletedAt(),
		ThresholdConfigured: true,
		Threshold:           5,
	}
	manual, err := observeOpenCodeResult(metadata, result)
	if err != nil {
		t.Fatalf("manual observeOpenCodeResult() error = %v", err)
	}
	metadata.Source = OpenCodeScheduled
	scheduled, err := observeOpenCodeResult(metadata, result)
	if err != nil {
		t.Fatalf("scheduled observeOpenCodeResult() error = %v", err)
	}
	if !reflect.DeepEqual(manual, scheduled) {
		t.Fatalf("manual observation = %+v, scheduled = %+v", manual, scheduled)
	}
}

func TestOpenCodeMonthlySkipAndUnskipProjectionParity(t *testing.T) {
	tests := []struct {
		name         string
		completeness Completeness
		score        float64
		outcome      Outcome
		resetAt      time.Time
	}{
		{
			name:         "monthly_skip",
			completeness: ExhaustionEvidence,
			score:        0,
			outcome:      Exhausted,
			resetAt:      time.Date(2026, time.January, 10, 20, 0, 0, 0, time.UTC),
		},
		{name: "monthly_unskip", completeness: AuthoritativeSnapshot, score: 12, outcome: Healthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeOpenCodeResult(openCodeResultMetadata{
				Source:              OpenCodeScheduled,
				CompletedAt:         openCodeTestCompletedAt(),
				ThresholdConfigured: true,
				Threshold:           5,
			}, readOpenCodeFixture(t, tt.name+".json"))
			if err != nil {
				t.Fatalf("observeOpenCodeResult() error = %v", err)
			}
			assertOpenCodeObservation(t, observation, tt.completeness, tt.score, tt.outcome, tt.resetAt)
		})
	}
}

func TestOpenCodeThresholdBoundaryAndDisabledPolicy(t *testing.T) {
	result := readOpenCodeFixture(t, "threshold_exhaustion.json")
	tests := []struct {
		name                string
		thresholdConfigured bool
		threshold           float64
		completeness        Completeness
		outcome             Outcome
	}{
		{name: "disabled", threshold: math.NaN(), completeness: ScoreOnly},
		{name: "equal is exhausted", thresholdConfigured: true, threshold: 5, completeness: ExhaustionEvidence, outcome: Exhausted},
		{name: "below score is healthy", thresholdConfigured: true, threshold: 4.999, completeness: AuthoritativeSnapshot, outcome: Healthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeOpenCodeResult(openCodeResultMetadata{
				Source:              OpenCodeManual,
				CompletedAt:         openCodeTestCompletedAt(),
				ThresholdConfigured: tt.thresholdConfigured,
				Threshold:           tt.threshold,
			}, result)
			if err != nil {
				t.Fatalf("observeOpenCodeResult() error = %v", err)
			}
			if observation.Completeness != tt.completeness {
				t.Fatalf("completeness = %v, want %v", observation.Completeness, tt.completeness)
			}
			if tt.outcome == 0 {
				if len(observation.Mutations) != 0 {
					t.Fatalf("mutations = %+v, want none", observation.Mutations)
				}
			} else if len(observation.Mutations) != 1 || observation.Mutations[0].Outcome != tt.outcome {
				t.Fatalf("mutations = %+v, want outcome %v", observation.Mutations, tt.outcome)
			}
		})
	}
}

func TestOpenCodeInvalidMetadataAndResultsAreRejected(t *testing.T) {
	validResult := readOpenCodeFixture(t, "healthy_recovery.json")
	validMetadata := openCodeResultMetadata{
		Source:              OpenCodeManual,
		CompletedAt:         openCodeTestCompletedAt(),
		ThresholdConfigured: true,
		Threshold:           5,
	}

	metadataTests := []struct {
		name   string
		mutate func(*openCodeResultMetadata)
	}{
		{name: "management source", mutate: func(metadata *openCodeResultMetadata) { metadata.Source = ManagementManual }},
		{name: "unknown source", mutate: func(metadata *openCodeResultMetadata) { metadata.Source = SourceKind(99) }},
		{name: "missing completion", mutate: func(metadata *openCodeResultMetadata) { metadata.CompletedAt = time.Time{} }},
		{name: "NaN threshold", mutate: func(metadata *openCodeResultMetadata) { metadata.Threshold = math.NaN() }},
		{name: "infinite threshold", mutate: func(metadata *openCodeResultMetadata) { metadata.Threshold = math.Inf(1) }},
		{name: "negative threshold", mutate: func(metadata *openCodeResultMetadata) { metadata.Threshold = -1 }},
		{name: "threshold above range", mutate: func(metadata *openCodeResultMetadata) { metadata.Threshold = 101 }},
	}
	for _, tt := range metadataTests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := validMetadata
			tt.mutate(&metadata)
			if _, err := observeOpenCodeResult(metadata, validResult); !errors.Is(err, errOpenCodeResultMetadataInvalid) {
				t.Fatalf("observeOpenCodeResult() error = %v, want metadata invalid", err)
			}
		})
	}

	pollErr := errors.New("poll failed")
	resultTests := []struct {
		name   string
		result *quota.PollResult
	}{
		{name: "nil result"},
		{name: "poll error", result: &quota.PollResult{Error: pollErr}},
		{name: "nil quota", result: &quota.PollResult{}},
		{name: "no windows", result: &quota.PollResult{Quota: &quota.OpenCodeGoQuota{}}},
		{
			name: "NaN remaining",
			result: &quota.PollResult{Quota: &quota.OpenCodeGoQuota{
				Rolling: &quota.OpenCodeGoWindow{PercentRemaining: math.NaN()},
			}},
		},
		{
			name: "infinite remaining",
			result: &quota.PollResult{Quota: &quota.OpenCodeGoQuota{
				Rolling: &quota.OpenCodeGoWindow{PercentRemaining: math.Inf(1)},
			}},
		},
	}
	for _, tt := range resultTests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := observeOpenCodeResult(validMetadata, tt.result); !errors.Is(err, errOpenCodePollResultInvalid) {
				t.Fatalf("observeOpenCodeResult() error = %v, want poll result invalid", err)
			}
		})
	}
}

func TestOpenCodeOutOfRangeRemainingPreservesLegacyClamping(t *testing.T) {
	result := &quota.PollResult{Quota: &quota.OpenCodeGoQuota{
		Rolling: &quota.OpenCodeGoWindow{PercentRemaining: -20},
		Weekly:  &quota.OpenCodeGoWindow{PercentRemaining: 120},
		Monthly: &quota.OpenCodeGoWindow{PercentRemaining: 50},
	}}
	observation, err := observeOpenCodeResult(openCodeResultMetadata{
		Source:      OpenCodeScheduled,
		CompletedAt: openCodeTestCompletedAt(),
	}, result)
	if err != nil {
		t.Fatalf("observeOpenCodeResult() error = %v", err)
	}
	assertOpenCodeObservation(t, observation, ScoreOnly, 0, 0, time.Time{})
}

func TestOpenCodePartialSnapshotsPreserveThresholdSemantics(t *testing.T) {
	completedAt := openCodeTestCompletedAt()
	tests := []struct {
		name         string
		result       *quota.PollResult
		score        float64
		completeness Completeness
		outcome      Outcome
		resetAt      time.Time
	}{
		{
			name: "partial healthy snapshot",
			result: &quota.PollResult{Quota: &quota.OpenCodeGoQuota{
				Rolling: &quota.OpenCodeGoWindow{PercentRemaining: 20},
				Weekly:  &quota.OpenCodeGoWindow{PercentRemaining: 30},
			}},
			score:        20,
			completeness: AuthoritativeSnapshot,
			outcome:      Healthy,
		},
		{
			name: "partial exhausted snapshot",
			result: &quota.PollResult{Quota: &quota.OpenCodeGoQuota{
				Weekly: &quota.OpenCodeGoWindow{PercentRemaining: 5, ResetTime: completedAt.Add(time.Hour)},
			}},
			score:        5,
			completeness: ExhaustionEvidence,
			outcome:      Exhausted,
			resetAt:      completedAt.Add(time.Hour),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeOpenCodeResult(openCodeResultMetadata{
				Source:              OpenCodeScheduled,
				CompletedAt:         completedAt,
				ThresholdConfigured: true,
				Threshold:           5,
			}, tt.result)
			if err != nil {
				t.Fatalf("observeOpenCodeResult() error = %v", err)
			}
			assertOpenCodeObservation(t, observation, tt.completeness, tt.score, tt.outcome, tt.resetAt)
		})
	}
}

func TestOpenCodeExhaustionInvalidResetStaysScoreOnly(t *testing.T) {
	completedAt := openCodeTestCompletedAt()
	tests := []struct {
		name    string
		resetAt time.Time
	}{
		{name: "missing reset"},
		{name: "reset at completion", resetAt: completedAt},
		{name: "past reset", resetAt: completedAt.Add(-time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation, err := observeOpenCodeResult(openCodeResultMetadata{
				Source:              OpenCodeScheduled,
				CompletedAt:         completedAt,
				ThresholdConfigured: true,
				Threshold:           5,
			}, openCodeTestQuotaResult(5, tt.resetAt))
			if err != nil {
				t.Fatalf("observeOpenCodeResult() error = %v", err)
			}
			assertOpenCodeObservation(t, observation, ScoreOnly, 5, 0, time.Time{})
		})
	}
}

func assertOpenCodeObservation(
	t *testing.T,
	observation Observation,
	wantCompleteness Completeness,
	wantScore float64,
	wantOutcome Outcome,
	wantReset time.Time,
) {
	t.Helper()
	if observation.Score == nil || *observation.Score != wantScore {
		t.Fatalf("score = %v, want %v", observation.Score, wantScore)
	}
	if observation.Completeness != wantCompleteness {
		t.Fatalf("completeness = %v, want %v", observation.Completeness, wantCompleteness)
	}
	if wantOutcome == 0 {
		if len(observation.Mutations) != 0 {
			t.Fatalf("mutations = %+v, want none", observation.Mutations)
		}
		return
	}
	if len(observation.Mutations) != 1 {
		t.Fatalf("mutations = %+v, want one", observation.Mutations)
	}
	mutation := observation.Mutations[0]
	if mutation.Scope != ScopeAuth || mutation.Model != "" || mutation.Outcome != wantOutcome || !mutation.ResetAt.Equal(wantReset) {
		t.Fatalf("mutation = %+v, want auth outcome %v reset %v", mutation, wantOutcome, wantReset)
	}
}

func readOpenCodeFixture(t *testing.T, name string) *quota.PollResult {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "opencode", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	var result quota.PollResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode fixture %q: %v", name, err)
	}
	return &result
}

func cloneOpenCodeTestResult(t *testing.T, result *quota.PollResult) *quota.PollResult {
	t.Helper()
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal poll result: %v", err)
	}
	var clone quota.PollResult
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatalf("unmarshal poll result clone: %v", err)
	}
	return &clone
}

func openCodeTestQuotaResult(score float64, resetAt time.Time) *quota.PollResult {
	return &quota.PollResult{Quota: &quota.OpenCodeGoQuota{
		Rolling: &quota.OpenCodeGoWindow{PercentRemaining: score, ResetTime: resetAt},
		Weekly:  &quota.OpenCodeGoWindow{PercentRemaining: 80, ResetTime: resetAt.Add(time.Hour)},
		Monthly: &quota.OpenCodeGoWindow{PercentRemaining: 90, ResetTime: resetAt.Add(2 * time.Hour)},
	}}
}

func openCodeTestCompletedAt() time.Time {
	return time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
}
