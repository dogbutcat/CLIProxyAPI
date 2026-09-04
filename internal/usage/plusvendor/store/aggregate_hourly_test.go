package plusstore

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestHourlyAggregateCatchUpMergedMatchesRawOracle(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC).UnixMilli()
	events := []Event{
		aggregateTestEvent("a", base+10*time.Minute.Milliseconds(), "openai", "acct-a", "gpt-5", 10, 20, false, 0, 0, 0),
		aggregateTestEvent("b", base+50*time.Minute.Milliseconds(), "openai", "acct-a", "gpt-5", 3, 4, true, 429, 1, 0),
		aggregateTestEvent("c", base+70*time.Minute.Milliseconds(), "gemini", "acct-b", "gemini-2.5-pro", 7, 8, false, 0, 0, 5),
		aggregateTestEvent("d", base+130*time.Minute.Milliseconds(), "openai", "acct-a", "gpt-5", 2, 2, false, 0, 0, 0),
	}
	if _, err := store.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	if _, err := store.CatchUpHourlyRollups(ctx, RollupOptions{ThroughMS: base + 3*HourMS, BatchHours: 1}); err != nil {
		t.Fatalf("CatchUpHourlyRollups() error = %v", err)
	}
	query := AggregateQuery{FromMS: base + 30*time.Minute.Milliseconds(), ToMS: base + 150*time.Minute.Milliseconds()}
	assertAggregatesMatchRawOracle(t, store, query)
}

func TestHourlyRollupRestartCatchUpNoGap(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := store.InsertEvents(ctx, []Event{
		aggregateTestEvent("restart-a", base+1, "openai", "acct-a", "gpt-5", 1, 2, false, 0, 0, 0),
		aggregateTestEvent("restart-b", base+HourMS+1, "openai", "acct-a", "gpt-5", 3, 4, false, 0, 0, 0),
	}); err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	first, err := store.CatchUpHourlyRollups(ctx, RollupOptions{ThroughMS: base + 3*HourMS, BatchHours: 1, MaxBatches: 1, Owner: "first"})
	if err != nil {
		t.Fatalf("first CatchUpHourlyRollups() error = %v", err)
	}
	if first.CheckpointMS != base+HourMS {
		t.Fatalf("first checkpoint = %d, want %d", first.CheckpointMS, base+HourMS)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen OpenStore() error = %v", err)
	}
	defer store.Close()
	second, err := store.CatchUpHourlyRollups(ctx, RollupOptions{ThroughMS: base + 3*HourMS, BatchHours: 1, Owner: "second"})
	if err != nil {
		t.Fatalf("second CatchUpHourlyRollups() error = %v", err)
	}
	if second.CheckpointMS != base+3*HourMS {
		t.Fatalf("second checkpoint = %d, want %d", second.CheckpointMS, base+3*HourMS)
	}
	assertAggregatesMatchRawOracle(t, store, AggregateQuery{FromMS: base, ToMS: base + 3*HourMS})
}

func TestHourlyRollupRebuildReflectsRawMutation(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC).UnixMilli()
	event := aggregateTestEvent("rebuild", base+1, "openai", "acct-a", "gpt-5", 1, 2, false, 0, 0, 0)
	if _, err := store.InsertEvents(ctx, []Event{event}); err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	if _, err := store.CatchUpHourlyRollups(ctx, RollupOptions{ThroughMS: base + HourMS}); err != nil {
		t.Fatalf("CatchUpHourlyRollups() error = %v", err)
	}
	if _, err := store.db.Exec(`update usage_events set input_tokens = 11, output_tokens = 13, total_tokens = 24 where event_hash = ?`, event.EventHash); err != nil {
		t.Fatalf("mutate raw event: %v", err)
	}
	if _, err := store.db.Exec(`update usage_event_identity_ledger set aggregate_schema_version = 0 where event_hash = ?`, event.EventHash); err != nil {
		t.Fatalf("mark ledger stale: %v", err)
	}
	if _, err := store.RebuildHourlyRollups(ctx, base, base+HourMS); err != nil {
		t.Fatalf("RebuildHourlyRollups() error = %v", err)
	}
	assertAggregatesMatchRawOracle(t, store, AggregateQuery{FromMS: base, ToMS: base + HourMS})
}

func TestHourlyRollupContentionAndConcurrentWritersDoNotDuplicate(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC).UnixMilli()
	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.InsertEvents(ctx, []Event{aggregateTestEvent(fmt.Sprintf("writer-%d", i), base+int64(i), "openai", "acct-a", "gpt-5", 1, 1, false, 0, 0, 0)})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent InsertEvents() error = %v", err)
		}
	}
	if _, err := store.CatchUpHourlyRollups(ctx, RollupOptions{ThroughMS: base + HourMS, Owner: "winner"}); err != nil {
		t.Fatalf("CatchUpHourlyRollups() error = %v", err)
	}
	assertAggregatesMatchRawOracle(t, store, AggregateQuery{FromMS: base, ToMS: base + HourMS})
	if _, err := store.db.Exec(`update usage_rollup_checkpoints set owner = 'held', lease_until_ms = ? where name = ?`, nowMS()+HourMS, hourlyRollupName); err != nil {
		t.Fatalf("hold lease: %v", err)
	}
	result, err := store.CatchUpHourlyRollups(ctx, RollupOptions{ThroughMS: base + HourMS, Owner: "blocked"})
	if err != nil {
		t.Fatalf("contended CatchUpHourlyRollups() error = %v", err)
	}
	if !result.Contended {
		t.Fatalf("Contended = false, want true")
	}
}

func assertAggregatesMatchRawOracle(t *testing.T, store *Store, query AggregateQuery) {
	t.Helper()
	got, err := store.MergedHourlyAggregates(context.Background(), query)
	if err != nil {
		t.Fatalf("MergedHourlyAggregates() error = %v", err)
	}
	want, err := store.RawHourlyAggregates(context.Background(), query)
	if err != nil {
		t.Fatalf("RawHourlyAggregates() error = %v", err)
	}
	if !reflect.DeepEqual(aggregateComparable(got), aggregateComparable(want)) {
		t.Fatalf("merged aggregates mismatch\n got: %#v\nwant: %#v", aggregateComparable(got), aggregateComparable(want))
	}
}

type comparableAggregate struct {
	HourMS       int64
	Provider     string
	Model        string
	AccountID    string
	Failed       bool
	FailCode     int
	CacheStatus  string
	EventCount   int64
	FailedCount  int64
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	CacheRead    int64
	CacheCreate  int64
}

func aggregateComparable(rows []HourlyAggregate) []comparableAggregate {
	out := make([]comparableAggregate, 0, len(rows))
	for _, row := range rows {
		out = append(out, comparableAggregate{
			HourMS:       row.HourMS,
			Provider:     row.Dimension.Provider,
			Model:        row.Dimension.Model,
			AccountID:    row.Dimension.AccountID,
			Failed:       row.Dimension.Failed,
			FailCode:     row.Dimension.FailStatusCode,
			CacheStatus:  row.Dimension.CacheStatus,
			EventCount:   row.Metrics.EventCount,
			FailedCount:  row.Metrics.FailedCount,
			InputTokens:  row.Metrics.InputTokens,
			OutputTokens: row.Metrics.OutputTokens,
			TotalTokens:  row.Metrics.TotalTokens,
			CacheRead:    row.Metrics.CacheReadTokens,
			CacheCreate:  row.Metrics.CacheCreationTokens,
		})
	}
	return out
}

func aggregateTestEvent(id string, timestampMS int64, provider, account, model string, input, output int64, failed bool, failCode int, cacheRead, cacheCreate int64) Event {
	return Event{
		RequestID:            id,
		EventHash:            "aggregate-" + id,
		TimestampMS:          timestampMS,
		Timestamp:            time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Provider:             provider,
		Model:                model,
		ResolvedModel:        model,
		AuthIndex:            account,
		AccountSnapshot:      account,
		InputTokens:          input,
		OutputTokens:         output,
		CacheReadTokens:      cacheRead,
		CacheCreationTokens:  cacheCreate,
		CachedTokens:         cacheRead,
		TotalTokens:          input + output,
		Failed:               failed,
		FailStatusCode:       failCode,
		AuthLabelSnapshot:    account + "-label",
		AuthProviderSnapshot: provider,
		CreatedAtMS:          timestampMS,
	}
}
