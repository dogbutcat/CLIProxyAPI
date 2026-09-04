package plusstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCoreInsertQueryOperations(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	latency := int64(42)
	used := 87.5
	event := Event{
		RequestID:              "req-1",
		EventHash:              "hash-1",
		TimestampMS:            time.Unix(1700000000, 0).UnixMilli(),
		Timestamp:              time.Unix(1700000000, 0).UTC().Format(time.RFC3339Nano),
		Provider:               "openai",
		Model:                  "gpt-5",
		ResolvedModel:          "gpt-5-2026-07-01",
		ResponseServiceTier:    "default",
		Endpoint:               "POST /v1/chat/completions",
		InputTokens:            10,
		OutputTokens:           20,
		TotalTokens:            30,
		LatencyMS:              &latency,
		HeaderQuotaUsedPercent: &used,
		HeaderQuotaPlanType:    "plus",
		HeaderTraceID:          "trace-1",
		RawJSON:                `{"api_key":"sk-secret-value","model":"gpt-5"}`,
		CreatedAtMS:            time.Now().UnixMilli(),
	}
	result, err := store.InsertEvents(ctx, []Event{event, event})
	if err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	if result.Inserted != 1 || result.Skipped != 1 {
		t.Fatalf("InsertEvents() = %+v, want inserted=1 skipped=1", result)
	}
	events, err := store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("RecentEvents() len = %d, want 1", len(events))
	}
	if events[0].RawJSON != "" {
		t.Fatalf("RecentEvents() exposed raw JSON = %q", events[0].RawJSON)
	}
	if events[0].HeaderTraceID != "trace-1" {
		t.Fatalf("HeaderTraceID = %q, want trace-1", events[0].HeaderTraceID)
	}
	if events[0].ResponseServiceTier != "default" {
		t.Fatalf("ResponseServiceTier = %q, want default", events[0].ResponseServiceTier)
	}
	windowEvents, err := store.EventsBetween(ctx, event.TimestampMS-1, event.TimestampMS+1, 10)
	if err != nil {
		t.Fatalf("EventsBetween() error = %v", err)
	}
	if len(windowEvents) != 1 || windowEvents[0].EventHash != "hash-1" {
		t.Fatalf("EventsBetween() = %+v, want hash-1", windowEvents)
	}
}

func TestInsertBatchClaimsAndAttachesIdentityLedger(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	timestampMS := time.Date(2026, 7, 27, 10, 34, 56, 789000000, time.UTC).UnixMilli()
	createdAtMS := timestampMS + 123
	result, err := store.InsertEvents(ctx, []Event{{
		RequestID:   "ledger-normal",
		EventHash:   "ledger-normal-hash",
		TimestampMS: timestampMS,
		Timestamp:   time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:       "gpt-5",
		TotalTokens: 1,
		CreatedAtMS: createdAtMS,
	}})
	if err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	if result.Inserted != 1 || result.Skipped != 0 {
		t.Fatalf("InsertEvents() = %+v, want inserted=1 skipped=0", result)
	}
	row := identityLedgerRowForHash(t, store.db, "ledger-normal-hash")
	if !row.RawEventID.Valid || row.RawEventID.Int64 <= 0 {
		t.Fatalf("raw_event_id = %+v, want attached raw id", row.RawEventID)
	}
	if row.TimestampMS != timestampMS {
		t.Fatalf("ledger timestamp_ms = %d, want %d", row.TimestampMS, timestampMS)
	}
	wantBucketMS := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC).UnixMilli()
	if row.BucketMS != wantBucketMS {
		t.Fatalf("ledger bucket_ms = %d, want %d", row.BucketMS, wantBucketMS)
	}
	if row.FirstSeenAtMS != createdAtMS {
		t.Fatalf("first_seen_at_ms = %d, want %d", row.FirstSeenAtMS, createdAtMS)
	}
}

func TestInsertBatchDuplicateHashSkipsOnLedgerClaim(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC).UnixMilli()
	event := Event{RequestID: "dup", EventHash: "ledger-dup-hash", TimestampMS: base, Timestamp: time.UnixMilli(base).UTC().Format(time.RFC3339Nano), Model: "gpt-5", TotalTokens: 1, CreatedAtMS: base + 1}
	result, err := store.InsertEvents(ctx, []Event{event, event})
	if err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	if result.Inserted != 1 || result.Skipped != 1 {
		t.Fatalf("InsertEvents() = %+v, want inserted=1 skipped=1", result)
	}
	if rawUsageEventCount(t, store.db, event.EventHash) != 1 {
		t.Fatalf("raw event count = %d, want 1", rawUsageEventCount(t, store.db, event.EventHash))
	}
	result, err = store.InsertEvents(ctx, []Event{event})
	if err != nil {
		t.Fatalf("InsertEvents() duplicate replay error = %v", err)
	}
	if result.Inserted != 0 || result.Skipped != 1 {
		t.Fatalf("duplicate replay InsertEvents() = %+v, want inserted=0 skipped=1", result)
	}
}

func TestInsertBatchLedgerClaimPreventsRawResurrection(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC).UnixMilli()
	event := Event{RequestID: "deleted", EventHash: "ledger-deleted-raw-hash", TimestampMS: base, Timestamp: time.UnixMilli(base).UTC().Format(time.RFC3339Nano), Model: "gpt-5", TotalTokens: 1, CreatedAtMS: base + 1}
	if result, err := store.InsertEvents(ctx, []Event{event}); err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	} else if result.Inserted != 1 || result.Skipped != 0 {
		t.Fatalf("InsertEvents() = %+v, want inserted=1 skipped=0", result)
	}
	if _, err := store.db.Exec(`delete from usage_events where event_hash = ?`, event.EventHash); err != nil {
		t.Fatalf("delete raw usage event: %v", err)
	}
	result, err := store.InsertEvents(ctx, []Event{event})
	if err != nil {
		t.Fatalf("InsertEvents() replay error = %v", err)
	}
	if result.Inserted != 0 || result.Skipped != 1 {
		t.Fatalf("replay InsertEvents() = %+v, want inserted=0 skipped=1", result)
	}
	if rawUsageEventCount(t, store.db, event.EventHash) != 0 {
		t.Fatalf("raw event resurrected, count = %d", rawUsageEventCount(t, store.db, event.EventHash))
	}
}

func TestInsertBatchAttachesLegacyRawEventWhenLedgerMissing(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	timestampMS := time.Date(2026, 7, 27, 13, 45, 0, 0, time.UTC).UnixMilli()
	legacyCreatedAtMS := timestampMS - 500
	res, err := store.db.Exec(`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms) values (?, ?, ?, ?, ?)`, "legacy-attach-hash", timestampMS, time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano), "gpt-5", legacyCreatedAtMS)
	if err != nil {
		t.Fatalf("insert legacy raw usage event: %v", err)
	}
	legacyRawID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId() error = %v", err)
	}
	result, err := store.InsertEvents(ctx, []Event{{
		RequestID:   "legacy-attach",
		EventHash:   "legacy-attach-hash",
		TimestampMS: timestampMS + 1000,
		Timestamp:   time.UnixMilli(timestampMS + 1000).UTC().Format(time.RFC3339Nano),
		Model:       "gpt-5",
		TotalTokens: 1,
		CreatedAtMS: timestampMS + 9999,
	}})
	if err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	if result.Inserted != 0 || result.Skipped != 1 {
		t.Fatalf("InsertEvents() = %+v, want inserted=0 skipped=1", result)
	}
	row := identityLedgerRowForHash(t, store.db, "legacy-attach-hash")
	if !row.RawEventID.Valid || row.RawEventID.Int64 != legacyRawID {
		t.Fatalf("raw_event_id = %+v, want %d", row.RawEventID, legacyRawID)
	}
	if row.TimestampMS != timestampMS {
		t.Fatalf("ledger timestamp_ms = %d, want legacy raw timestamp %d", row.TimestampMS, timestampMS)
	}
	if row.FirstSeenAtMS != legacyCreatedAtMS {
		t.Fatalf("first_seen_at_ms = %d, want legacy raw created_at_ms %d", row.FirstSeenAtMS, legacyCreatedAtMS)
	}
}

func TestDatabaseConcurrentInsertKeepsWALBusyBehavior(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	const writers = 12
	base := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC).UnixMilli()
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			timestampMS := base + int64(i)
			_, err := store.InsertEvents(ctx, []Event{{
				RequestID:   fmt.Sprintf("concurrent-%d", i),
				EventHash:   fmt.Sprintf("concurrent-hash-%d", i),
				TimestampMS: timestampMS,
				Timestamp:   time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
				Model:       "gpt-5",
				TotalTokens: 1,
				CreatedAtMS: timestampMS,
			}})
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
	count, err := store.CountEvents(ctx)
	if err != nil {
		t.Fatalf("CountEvents() error = %v", err)
	}
	if count != writers {
		t.Fatalf("event count = %d, want %d", count, writers)
	}
}

func TestInsertDeadLetterSanitizesSecrets(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	if err := store.AddDeadLetter(ctx, `{"api_key":"sk-dead-letter-secret","email":"alice@example.com"}`, errors.New("authorization: Bearer sk-error-secret")); err != nil {
		t.Fatalf("AddDeadLetter() error = %v", err)
	}
	var payload, errText string
	if err := store.db.QueryRow(`select payload, error from dead_letter_events limit 1`).Scan(&payload, &errText); err != nil {
		t.Fatalf("query dead letter: %v", err)
	}
	combined := payload + " " + errText
	for _, secret := range []string{"sk-dead-letter-secret", "sk-error-secret", "alice@example.com"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("dead letter leaked %q in %q", secret, combined)
		}
	}
}

func TestInsertBatchDerivesResponseHeaderColumnsFromRawHeaderJSON(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	timestampMS := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC).UnixMilli()
	result, err := store.InsertEvents(ctx, []Event{{
		RequestID:            "headers-json",
		EventHash:            "headers-json-hash",
		TimestampMS:          timestampMS,
		Timestamp:            time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:                "gpt-5",
		ResponseMetadataJSON: `{"X-Codex-Plan-Type":["pro"],"X-Codex-Primary-Used-Percent":["82.5%"],"X-Oai-Request-Id":["trace-json"],"Authorization":["Bearer sk-secret"]}`,
		CreatedAtMS:          timestampMS,
	}})
	if err != nil {
		t.Fatalf("InsertEvents() error = %v", err)
	}
	if result.Inserted != 1 || result.Skipped != 0 {
		t.Fatalf("InsertEvents() = %+v, want inserted=1 skipped=0", result)
	}
	events, err := store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("RecentEvents() len = %d, want 1", len(events))
	}
	if events[0].HeaderQuotaPlanType != "pro" || events[0].HeaderTraceID != "trace-json" {
		t.Fatalf("derived headers = %+v", events[0])
	}
	if events[0].ResponseMetadata == nil || events[0].ResponseMetadata.Quota == nil {
		t.Fatalf("ResponseMetadata = %+v, want parsed quota metadata", events[0].ResponseMetadata)
	}
	if strings.Contains(events[0].ResponseMetadataJSON, "sk-secret") || strings.Contains(strings.ToLower(events[0].ResponseMetadataJSON), "authorization") {
		t.Fatalf("ResponseMetadataJSON leaked unsafe header: %s", events[0].ResponseMetadataJSON)
	}
}

type identityLedgerTestRow struct {
	RawEventID             sql.NullInt64
	TimestampMS            int64
	BucketMS               int64
	AggregateSchemaVersion int
	FirstSeenAtMS          int64
	UpdatedAtMS            int64
}

func identityLedgerRowForHash(t *testing.T, db *sql.DB, eventHash string) identityLedgerTestRow {
	t.Helper()
	var row identityLedgerTestRow
	err := db.QueryRow(`select raw_event_id, timestamp_ms, bucket_ms, aggregate_schema_version, first_seen_at_ms, updated_at_ms from usage_event_identity_ledger where event_hash = ?`, eventHash).Scan(&row.RawEventID, &row.TimestampMS, &row.BucketMS, &row.AggregateSchemaVersion, &row.FirstSeenAtMS, &row.UpdatedAtMS)
	if err != nil {
		t.Fatalf("query identity ledger %s: %v", eventHash, err)
	}
	return row
}

func rawUsageEventCount(t *testing.T, db *sql.DB, eventHash string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`select count(*) from usage_events where event_hash = ?`, eventHash).Scan(&count); err != nil {
		t.Fatalf("count raw usage events %s: %v", eventHash, err)
	}
	return count
}
