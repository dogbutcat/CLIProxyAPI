package plusstore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUsageExportStreamRedactsAndImports(t *testing.T) {
	store := newUsageStreamTestStore(t)
	nowMS := time.Date(2026, 8, 2, 18, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := store.InsertEvents(context.Background(), []Event{{
		RequestID:    "stream-1",
		TimestampMS:  nowMS,
		Provider:     "openai",
		Model:        "gpt-5",
		Endpoint:     "/v1/responses",
		Source:       "sk-secret-stream-key-123456",
		InputTokens:  4,
		OutputTokens: 5,
		TotalTokens:  9,
		RawJSON:      `{"api_key":"sk-secret-stream-key-123456"}`,
	}}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	var buf bytes.Buffer
	count, err := store.ExportEventsJSONL(context.Background(), &buf)
	if err != nil {
		t.Fatalf("export events: %v", err)
	}
	if count != 1 {
		t.Fatalf("export count = %d, want 1", count)
	}
	if strings.Contains(buf.String(), "sk-secret-stream-key") {
		t.Fatalf("export leaked secret: %s", buf.String())
	}

	target := newUsageStreamTestStore(t)
	result, err := target.ImportEventsJSONL(context.Background(), strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("import events: %v", err)
	}
	if result.Added != 1 || result.Total != 1 || result.Failed != 0 {
		t.Fatalf("import result = %#v, want one added event", result)
	}
}

func TestUsageExportStreamRespectsCancellation(t *testing.T) {
	store := newUsageStreamTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ExportEventsJSONL(ctx, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("export err = %v, want context.Canceled", err)
	}
}

func TestImportSessionCompleteStoreRetryableClassification(t *testing.T) {
	if !IsUsageImportRetryable(context.Canceled) {
		t.Fatal("context.Canceled should be retryable")
	}
	if IsUsageImportRetryable(errors.New("invalid import stream")) {
		t.Fatal("plain import stream errors should not be retryable")
	}
}

func newUsageStreamTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
