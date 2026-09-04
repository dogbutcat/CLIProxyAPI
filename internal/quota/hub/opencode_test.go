package hub

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestBeginOpenCodeQuotaObservationRejectsInvalidStartsWithoutTicket(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-invalid")

	invalid := []struct {
		name       string
		manager    *auth.Manager
		resolved   *auth.Auth
		source     SourceKind
		configured bool
		threshold  float64
	}{
		{name: "nil manager", resolved: resolved, source: OpenCodeManual},
		{name: "nil auth", manager: manager, source: OpenCodeManual},
		{name: "wrong provider", manager: manager, resolved: cloneOpenCodeHubAuth(resolved, func(candidate *auth.Auth) { candidate.Provider = "claude" }), source: OpenCodeManual},
		{name: "disabled flag", manager: manager, resolved: cloneOpenCodeHubAuth(resolved, func(candidate *auth.Auth) { candidate.Disabled = true }), source: OpenCodeManual},
		{name: "disabled status", manager: manager, resolved: cloneOpenCodeHubAuth(resolved, func(candidate *auth.Auth) { candidate.Status = auth.StatusDisabled }), source: OpenCodeManual},
		{name: "management source", manager: manager, resolved: resolved, source: ManagementManual},
		{name: "unknown source", manager: manager, resolved: resolved, source: SourceKind(99)},
		{name: "nan threshold", manager: manager, resolved: resolved, source: OpenCodeManual, configured: true, threshold: math.NaN()},
		{name: "high threshold", manager: manager, resolved: resolved, source: OpenCodeManual, configured: true, threshold: 101},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			if completion := BeginOpenCodeQuotaObservation(testCase.manager, testCase.resolved, testCase.source, testCase.configured, testCase.threshold); completion != nil {
				t.Fatal("BeginOpenCodeQuotaObservation() returned a completion")
			}
		})
	}

	ticket, issued := manager.IssueQuotaObservationTicketForAuth(resolved)
	if !issued || ticket.StartOrder != 1 {
		t.Fatalf("next ticket = %+v, %v; want start order 1", ticket, issued)
	}
}

func TestOpenCodeQuotaObservationRejectsFetchStartedBeforeRuntime429(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-stale-runtime")
	completion := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeScheduled, true, 5)
	if completion == nil {
		t.Fatal("BeginOpenCodeQuotaObservation() returned nil")
	}

	manager.MarkResult(context.Background(), auth.Result{
		AuthID:   resolved.ID,
		Provider: openCodeProvider,
		Success:  false,
		Error:    &auth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})
	if completion(context.Background(), openCodeHubResult(80, time.Now().Add(time.Hour))) {
		t.Fatal("stale completion reported accepted")
	}

	if _, ok := manager.QuotaScore(resolved.ID); ok {
		t.Fatal("stale observation updated quota score")
	}
	updated, _ := manager.GetByID(resolved.ID)
	if updated == nil || !updated.Quota.Exceeded {
		t.Fatalf("runtime 429 state = %+v, want retained quota block", updated)
	}
}

func TestOpenCodeQuotaObservationReverseCompletionUsesLaterStart(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-reverse")
	older := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeScheduled, false, 0)
	newer := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeManual, false, 0)
	if older == nil || newer == nil {
		t.Fatal("expected both OpenCode completions")
	}

	if !newer(context.Background(), openCodeHubResult(73, time.Now().Add(time.Hour))) {
		t.Fatal("newer completion reported rejected")
	}
	if older(context.Background(), openCodeHubResult(12, time.Now().Add(time.Hour))) {
		t.Fatal("older completion reported accepted")
	}

	if score, ok := manager.QuotaScore(resolved.ID); !ok || score != 73 {
		t.Fatalf("QuotaScore() = %v, %v; want 73, true", score, ok)
	}
}

func TestOpenCodeQuotaObservationRejectsReplacedGeneration(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-generation")
	completion := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeScheduled, false, 0)
	if completion == nil {
		t.Fatal("BeginOpenCodeQuotaObservation() returned nil")
	}
	replacement := resolved.Clone()
	replacement.Metadata = map[string]any{"replacement": true}
	if _, errUpdate := manager.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}

	if completion(context.Background(), openCodeHubResult(44, time.Now().Add(time.Hour))) {
		t.Fatal("replaced-generation completion reported accepted")
	}
	if _, ok := manager.QuotaScore(resolved.ID); ok {
		t.Fatal("replaced-generation completion updated quota score")
	}
}

func TestOpenCodeQuotaObservationThresholdDisabledIsScoreOnly(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-score-only")
	completion := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeManual, false, 0)
	if !completion(context.Background(), openCodeHubResult(0, time.Now().Add(time.Hour))) {
		t.Fatal("score-only completion reported rejected")
	}

	if score, ok := manager.QuotaScore(resolved.ID); !ok || score != 0 {
		t.Fatalf("QuotaScore() = %v, %v; want 0, true", score, ok)
	}
	updated, _ := manager.GetByID(resolved.ID)
	if updated == nil || updated.Quota.Exceeded || updated.Unavailable {
		t.Fatalf("threshold-disabled auth = %+v, want score-only state", updated)
	}
}

func TestOpenCodeQuotaObservationThresholdCrossingAndRecovery(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-threshold")
	resetAt := time.Now().Add(time.Hour).UTC()
	exhausted := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeScheduled, true, 5)
	if !exhausted(context.Background(), openCodeHubResult(5, resetAt)) {
		t.Fatal("exhaustion completion reported rejected")
	}

	updated, _ := manager.GetByID(resolved.ID)
	if updated == nil || !updated.Quota.Exceeded || updated.Quota.Reason != "quota_hub" || !updated.Quota.NextRecoverAt.Equal(resetAt) {
		t.Fatalf("exhausted auth = %+v, want QuotaHub block through %v", updated, resetAt)
	}

	resolved, _ = manager.GetByID(resolved.ID)
	recovered := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeManual, true, 5)
	if !recovered(context.Background(), openCodeHubResult(6, time.Now().Add(time.Hour))) {
		t.Fatal("recovery completion reported rejected")
	}
	updated, _ = manager.GetByID(resolved.ID)
	if updated == nil || updated.Quota.Exceeded || updated.Unavailable {
		t.Fatalf("recovered auth = %+v, want available", updated)
	}
	if score, ok := manager.QuotaScore(resolved.ID); !ok || score != 6 {
		t.Fatalf("QuotaScore() = %v, %v; want 6, true", score, ok)
	}
}

func TestOpenCodeQuotaCompletionIsSingleUse(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-once")
	completion := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeManual, false, 0)
	if !completion(context.Background(), openCodeHubResult(19, time.Now().Add(time.Hour))) {
		t.Fatal("first completion reported rejected")
	}
	if completion(context.Background(), openCodeHubResult(91, time.Now().Add(time.Hour))) {
		t.Fatal("second completion reported accepted")
	}

	if score, ok := manager.QuotaScore(resolved.ID); !ok || score != 19 {
		t.Fatalf("QuotaScore() = %v, %v; want first result 19", score, ok)
	}
}

func TestOpenCodeQuotaCompletionContainsAdapterFailures(t *testing.T) {
	tests := []struct {
		name    string
		adapter openCodeResultAdapter
	}{
		{name: "unavailable"},
		{name: "error", adapter: openCodeResultAdapter{observe: func(openCodeResultMetadata, *quota.PollResult) (Observation, error) {
			return Observation{}, errors.New("sensitive adapter detail")
		}}},
		{name: "panic", adapter: openCodeResultAdapter{observe: func(openCodeResultMetadata, *quota.PollResult) (Observation, error) {
			panic("sensitive panic detail")
		}}},
		{name: "invalid observation", adapter: openCodeResultAdapter{observe: func(openCodeResultMetadata, *quota.PollResult) (Observation, error) {
			return Observation{Completeness: ScoreOnly}, nil
		}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			manager, resolved := newOpenCodeHubTestManager(t, "opencode-failure-"+testCase.name)
			completion := beginOpenCodeQuotaObservationWithAdapter(manager, resolved, OpenCodeManual, false, 0, testCase.adapter)
			if completion == nil {
				t.Fatal("beginOpenCodeQuotaObservationWithAdapter() returned nil")
			}
			if completion(context.Background(), openCodeHubResult(50, time.Now().Add(time.Hour))) {
				t.Fatal("failed adapter completion reported accepted")
			}
			if _, ok := manager.QuotaScore(resolved.ID); ok {
				t.Fatal("failed adapter updated quota score")
			}
		})
	}
}

func TestOpenCodeQuotaCompletionDetachesCancellationAndRetainsValues(t *testing.T) {
	manager, resolved := newOpenCodeHubTestManager(t, "opencode-context")
	store := &openCodeContextStore{}
	manager.SetCooldownStateStore(store)
	completion := BeginOpenCodeQuotaObservation(manager, resolved, OpenCodeManual, true, 5)

	ctx := context.WithValue(context.Background(), openCodeContextKey{}, "retained")
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if !completion(ctx, openCodeHubResult(1, time.Now().Add(time.Hour))) {
		t.Fatal("canceled-context completion reported rejected")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saves != 1 || store.err != nil || store.value != "retained" {
		t.Fatalf("Save() = saves %d err %v value %v; want 1, nil, retained", store.saves, store.err, store.value)
	}
}

type openCodeContextStore struct {
	mu    sync.Mutex
	saves int
	err   error
	value any
}

type openCodeContextKey struct{}

func (*openCodeContextStore) Load(context.Context) ([]auth.CooldownStateRecord, error) {
	return nil, nil
}

func (store *openCodeContextStore) Save(ctx context.Context, _ []auth.CooldownStateRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saves++
	store.err = ctx.Err()
	store.value = ctx.Value(openCodeContextKey{})
	return nil
}

func newOpenCodeHubTestManager(t *testing.T, authID string) (*auth.Manager, *auth.Auth) {
	t.Helper()
	manager := auth.NewManager(nil, nil, nil)
	registered, errRegister := manager.Register(context.Background(), &auth.Auth{
		ID:       authID,
		Provider: openCodeProvider,
		Status:   auth.StatusActive,
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return manager, registered
}

func cloneOpenCodeHubAuth(source *auth.Auth, mutate func(*auth.Auth)) *auth.Auth {
	clone := source.Clone()
	mutate(clone)
	return clone
}

func openCodeHubResult(score float64, resetAt time.Time) *quota.PollResult {
	return &quota.PollResult{
		EntryName: "opencode-test",
		Timestamp: time.Now(),
		Quota: &quota.OpenCodeGoQuota{
			Rolling: &quota.OpenCodeGoWindow{PercentRemaining: score, ResetTime: resetAt},
		},
	}
}
