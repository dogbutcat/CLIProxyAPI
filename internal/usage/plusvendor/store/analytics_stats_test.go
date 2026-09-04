package plusstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestMonitoringAnalyticsFilterStatsUseSQLBeyondEventsLimit(t *testing.T) {
	store := newMonitoringTestStore(t)
	baseMS := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC).UnixMilli()
	events := make([]Event, 0, MaxMonitoringLimit+5)
	for i := 0; i < MaxMonitoringLimit+5; i++ {
		events = append(events, Event{
			RequestID:       fmt.Sprintf("req-%03d", i),
			TimestampMS:     baseMS + int64(i),
			Provider:        "Claude",
			Model:           "claude-sonnet-4",
			ResolvedModel:   "claude-sonnet-4",
			Endpoint:        "/v1/messages",
			AuthIndex:       "account-a",
			APIKeyHash:      "key-a",
			InputTokens:     1,
			OutputTokens:    2,
			TotalTokens:     3,
			CacheReadTokens: 1,
		})
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	analytics, err := store.MonitoringAnalytics(context.Background(), AnalyticsFilter{Provider: "claude"})
	if err != nil {
		t.Fatalf("monitoring analytics: %v", err)
	}
	if analytics.Totals.EventCount != int64(MaxMonitoringLimit+5) {
		t.Fatalf("total calls = %d, want %d", analytics.Totals.EventCount, MaxMonitoringLimit+5)
	}
	page, err := store.MonitoringEventsPage(context.Background(), AnalyticsEventPageQuery{Filter: AnalyticsFilter{Provider: "CLAUDE"}, Limit: 5})
	if err != nil {
		t.Fatalf("events page: %v", err)
	}
	if len(page.Events) != 5 || page.NextCursor == nil {
		t.Fatalf("page len=%d cursor=%#v, want len 5 with cursor", len(page.Events), page.NextCursor)
	}
}

func TestMonitoringSelectorsDoNotSelfNarrowAndFiltersAreNormalized(t *testing.T) {
	store := newMonitoringTestStore(t)
	baseMS := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC).UnixMilli()
	events := []Event{
		{
			RequestID:    "claude-ok",
			TimestampMS:  baseMS,
			Provider:     "Claude",
			Model:        "claude-sonnet-4",
			Endpoint:     "/v1/messages",
			AuthIndex:    "account-a",
			APIKeyHash:   "key-a",
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
			CachedTokens: 2,
		},
		{
			RequestID:       "openai-fail",
			TimestampMS:     baseMS + 1,
			Provider:        "openai",
			Model:           "gpt-5",
			Endpoint:        "/v1/responses",
			AuthIndex:       "account-b",
			APIKeyHash:      "key-b",
			InputTokens:     5,
			OutputTokens:    0,
			TotalTokens:     5,
			Failed:          true,
			FailStatusCode:  429,
			FailSummary:     "Quota Exceeded",
			HeaderErrorKind: "quota",
		},
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	failedFalse := false
	analytics, err := store.MonitoringAnalytics(context.Background(), AnalyticsFilter{Provider: "CLAUDE", CacheStatus: "cache-hit", Failed: &failedFalse, Search: "SONNET"})
	if err != nil {
		t.Fatalf("monitoring analytics: %v", err)
	}
	if analytics.Totals.EventCount != 1 || analytics.Totals.FailedCount != 0 {
		t.Fatalf("totals = %#v, want one successful claude cache hit", analytics.Totals)
	}
	failedTrue := true
	failedAnalytics, err := store.MonitoringAnalytics(context.Background(), AnalyticsFilter{Failed: &failedTrue, Search: "quota"})
	if err != nil {
		t.Fatalf("failed analytics: %v", err)
	}
	if failedAnalytics.Totals.EventCount != 1 || failedAnalytics.Totals.FailedCount != 1 {
		t.Fatalf("failed totals = %#v, want one failure", failedAnalytics.Totals)
	}
	selectors, err := store.MonitoringSelectors(context.Background(), AnalyticsFilter{Provider: "claude"})
	if err != nil {
		t.Fatalf("monitoring selectors: %v", err)
	}
	if !selectorHasValue(selectors.Providers, "openai") || !selectorHasValue(selectors.Providers, "Claude") {
		t.Fatalf("provider selectors = %#v, want both providers despite provider filter", selectors.Providers)
	}
	if selectorHasValue(selectors.Accounts, "account-b") {
		t.Fatalf("account selectors = %#v, provider filter should still apply to account options", selectors.Accounts)
	}
	arraySelectors, err := store.MonitoringSelectors(context.Background(), AnalyticsFilter{Providers: []string{"claude"}})
	if err != nil {
		t.Fatalf("array monitoring selectors: %v", err)
	}
	if !selectorHasValue(arraySelectors.Providers, "openai") || !selectorHasValue(arraySelectors.Providers, "Claude") {
		t.Fatalf("array provider selectors = %#v, want provider dimension without self narrowing", arraySelectors.Providers)
	}
	modelSelectors, err := store.MonitoringSelectors(context.Background(), AnalyticsFilter{Models: []string{"gpt-5"}})
	if err != nil {
		t.Fatalf("model monitoring selectors: %v", err)
	}
	if !selectorHasValue(modelSelectors.Models, "claude-sonnet-4") || !selectorHasValue(modelSelectors.Models, "gpt-5") {
		t.Fatalf("array model selectors = %#v, want model dimension without self narrowing", modelSelectors.Models)
	}
}

func TestMonitoringAnalyticsAPIKeyFilterMatchesKeyIdentityFallback(t *testing.T) {
	store := newMonitoringTestStore(t)
	baseMS := time.Date(2026, 8, 2, 11, 30, 0, 0, time.UTC).UnixMilli()
	events := []Event{
		{
			RequestID:    "source-key-hit",
			TimestampMS:  baseMS,
			Provider:     "opencode-go",
			Model:        "qwen3.7-max",
			Endpoint:     "/v1/chat/completions",
			AuthIndex:    "account-a",
			Source:       "m:sk-source",
			SourceHash:   "source-key-hash",
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
		},
		{
			RequestID:    "api-key-hit",
			TimestampMS:  baseMS + 1,
			Provider:     "openai",
			Model:        "gpt-5",
			Endpoint:     "/v1/responses",
			AuthIndex:    "account-b",
			APIKeyHash:   "api-key-hash",
			InputTokens:  5,
			OutputTokens: 6,
			TotalTokens:  11,
		},
	}
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	sourceAnalytics, err := store.MonitoringAnalytics(context.Background(), AnalyticsFilter{
		APIKeyHashes: []string{"source-key-hash"},
	})
	if err != nil {
		t.Fatalf("source key analytics: %v", err)
	}
	if sourceAnalytics.Totals.EventCount != 1 || sourceAnalytics.ByKey[0].Key != "source-key-hash" {
		t.Fatalf("source key analytics = totals %#v by key %#v, want source-key-hash only", sourceAnalytics.Totals, sourceAnalytics.ByKey)
	}

	apiKeyAnalytics, err := store.MonitoringAnalytics(context.Background(), AnalyticsFilter{
		APIKeyHashes: []string{"api-key-hash"},
	})
	if err != nil {
		t.Fatalf("api key analytics: %v", err)
	}
	if apiKeyAnalytics.Totals.EventCount != 1 || apiKeyAnalytics.ByKey[0].Key != "api-key-hash" {
		t.Fatalf("api key analytics = totals %#v by key %#v, want api-key-hash only", apiKeyAnalytics.Totals, apiKeyAnalytics.ByKey)
	}
}

func TestMonitoringEventsKeysetPaginationNoDuplicateOrGap(t *testing.T) {
	store := newMonitoringTestStore(t)
	baseMS := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 5; i++ {
		if _, err := store.InsertEvents(context.Background(), []Event{{
			RequestID:    fmt.Sprintf("same-ts-%d", i),
			TimestampMS:  baseMS,
			Provider:     "claude",
			Model:        "claude-sonnet-4",
			Endpoint:     "/v1/messages",
			AuthIndex:    "account-a",
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		}}); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}

	seen := map[int64]bool{}
	var cursor *AnalyticsEventCursor
	for pageIndex := 0; pageIndex < 3; pageIndex++ {
		page, err := store.MonitoringEventsPage(context.Background(), AnalyticsEventPageQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pageIndex, err)
		}
		for _, event := range page.Events {
			if seen[event.ID] {
				t.Fatalf("duplicate event id %d across pages", event.ID)
			}
			seen[event.ID] = true
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("seen ids = %v, want 5 unique events", seen)
	}
	if cursor != nil {
		t.Fatalf("final cursor = %#v, want nil", cursor)
	}
}

func newMonitoringTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func selectorHasValue(options []MonitoringSelectorOption, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}
