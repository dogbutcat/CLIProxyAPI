package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

func TestHourlyAggregateWorkerRunOnceRollup(t *testing.T) {
	ctx := context.Background()
	store, err := plusstore.OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC).UnixMilli()
	event := plusstore.Event{
		RequestID:     "worker",
		EventHash:     "worker-hash",
		TimestampMS:   base + 1,
		Timestamp:     time.UnixMilli(base + 1).UTC().Format(time.RFC3339Nano),
		Provider:      "openai",
		Model:         "gpt-5",
		ResolvedModel: "gpt-5",
		AuthIndex:     "acct-worker",
		InputTokens:   4,
		OutputTokens:  5,
		TotalTokens:   9,
		CreatedAtMS:   base + 1,
	}
	if _, err := store.InsertEvents(ctx, []plusstore.Event{event}); err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	result, err := NewHourlyAggregateWorker(store, plusstore.RollupOptions{ThroughMS: base + plusstore.HourMS}).RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.RowsWritten != 1 || result.RawEvents != 1 {
		t.Fatalf("RunOnce() result = %+v, want one raw event and one row", result)
	}
	dashboard, err := NewDashboardHourlyRollup(store).Query(ctx, plusstore.AggregateQuery{FromMS: base, ToMS: base + plusstore.HourMS})
	if err != nil {
		t.Fatalf("Dashboard Query() error = %v", err)
	}
	if dashboard.Totals.TotalTokens != 9 {
		t.Fatalf("dashboard total tokens = %d, want 9", dashboard.Totals.TotalTokens)
	}
	history, err := NewAccountHistoryRollup(store).Query(ctx, plusstore.AggregateQuery{FromMS: base, ToMS: base + plusstore.HourMS, AccountID: "acct-worker"})
	if err != nil {
		t.Fatalf("Account Query() error = %v", err)
	}
	if len(history) != 1 || history[0].Metrics.TotalTokens != 9 {
		t.Fatalf("history = %+v, want one row with 9 tokens", history)
	}
}
