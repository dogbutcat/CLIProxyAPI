package usage

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type notifyingEventStore struct {
	*MemoryEventStore
	inserted chan struct{}
	once     sync.Once
}

func newNotifyingEventStore() *notifyingEventStore {
	return &notifyingEventStore{
		MemoryEventStore: NewMemoryEventStore(),
		inserted:         make(chan struct{}),
	}
}

func (s *notifyingEventStore) InsertEvents(ctx context.Context, events []Event) error {
	if err := s.MemoryEventStore.InsertEvents(ctx, events); err != nil {
		return err
	}
	s.once.Do(func() { close(s.inserted) })
	return nil
}

type blockingEventStore struct {
	*MemoryEventStore
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func newBlockingEventStore() *blockingEventStore {
	return &blockingEventStore{
		MemoryEventStore: NewMemoryEventStore(),
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}
}

func (s *blockingEventStore) InsertEvents(ctx context.Context, events []Event) error {
	s.startedOnce.Do(func() { close(s.started) })
	<-s.release
	return s.MemoryEventStore.InsertEvents(ctx, events)
}

type failOnceEventStore struct {
	*MemoryEventStore
	mu     sync.Mutex
	failed bool
}

func (s *failOnceEventStore) InsertEvents(ctx context.Context, events []Event) error {
	s.mu.Lock()
	if !s.failed {
		s.failed = true
		s.mu.Unlock()
		return errors.New("injected insert failure")
	}
	s.mu.Unlock()
	return s.MemoryEventStore.InsertEvents(ctx, events)
}

func TestBridgeStartCloseIdempotent(t *testing.T) {
	bridge, err := NewBridge(BridgeConfig{DBPath: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	bridge.Start(context.Background())
	bridge.Start(context.Background())
	if bridge.Collector() == nil {
		t.Fatal("Collector() = nil")
	}
	bridge.Collector().RecordEvent(Event{Model: "gpt-5", EventHash: "event-1"})
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if events := bridge.Events(); len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func TestCollectorFlushMovesBufferedEventsToStore(t *testing.T) {
	collector := NewCollector()
	collector.RecordEvent(Event{Model: "gpt-5", EventHash: "event-1"})
	if buffered := collector.BufferedEvents(); len(buffered) != 1 {
		t.Fatalf("buffered events before flush = %d, want 1", len(buffered))
	}
	if err := collector.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if buffered := collector.BufferedEvents(); len(buffered) != 0 {
		t.Fatalf("buffered events after flush = %d, want 0", len(buffered))
	}
	if events := collector.Events(); len(events) != 1 {
		t.Fatalf("stored events = %d, want 1", len(events))
	}
}

func TestCollectorPeriodicallyFlushesBufferedEvents(t *testing.T) {
	store := newNotifyingEventStore()
	collector := NewCollectorWithStore(store)
	collector.flushInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	collector.Start(ctx)
	t.Cleanup(func() {
		cancel()
		if err := collector.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})

	collector.RecordEvent(Event{Model: "gpt-5", EventHash: "periodic-event"})
	select {
	case <-store.inserted:
	case <-time.After(time.Second):
		t.Fatal("collector did not flush before deadline")
	}
	if buffered := collector.BufferedEvents(); len(buffered) != 0 {
		t.Fatalf("buffered events after periodic flush = %d, want 0", len(buffered))
	}
}

func TestCollectorStopWithoutStartFlushesBufferedEvents(t *testing.T) {
	collector := NewCollector()
	collector.RecordEvent(Event{Model: "gpt-5", EventHash: "stop-event"})
	if err := collector.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if events := collector.Events(); len(events) != 1 {
		t.Fatalf("stored events = %d, want 1", len(events))
	}
}

func TestCollectorFlushFailureRequeuesEvents(t *testing.T) {
	store := &failOnceEventStore{MemoryEventStore: NewMemoryEventStore()}
	collector := NewCollectorWithStore(store)
	collector.RecordEvent(Event{Model: "gpt-5", EventHash: "retry-event"})
	if err := collector.Flush(context.Background()); err == nil {
		t.Fatal("first Flush error = nil, want injected failure")
	}
	if buffered := collector.BufferedEvents(); len(buffered) != 1 {
		t.Fatalf("buffered events after failed flush = %d, want 1", len(buffered))
	}
	if err := collector.Flush(context.Background()); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if events, err := store.Events(context.Background()); err != nil || len(events) != 1 {
		t.Fatalf("stored events = %d, err = %v, want 1", len(events), err)
	}
}

func TestBridgeCloseCanRetryAfterCollectorStopTimeout(t *testing.T) {
	store := newBlockingEventStore()
	bridge, err := NewBridge(BridgeConfig{Store: store})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	bridge.collector.flushInterval = 10 * time.Millisecond
	bridge.Start(context.Background())
	bridge.Collector().RecordEvent(Event{Model: "gpt-5", EventHash: "slow-event"})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("collector insert did not start")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = bridge.Close(stopCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Close error = %v, want deadline exceeded", err)
	}
	bridge.mu.RLock()
	closed := bridge.closed
	bridge.mu.RUnlock()
	if closed {
		t.Fatal("bridge marked closed after failed Close")
	}

	close(store.release)
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if events := bridge.Events(); len(events) != 1 {
		t.Fatalf("events after retry Close = %d, want 1", len(events))
	}
}

func TestBridgeDBPathPersistsResponseMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	bridge, err := NewBridge(BridgeConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	timestampMS := time.Date(2026, 7, 27, 17, 0, 0, 0, time.UTC).UnixMilli()
	bridge.Collector().RecordEvent(Event{
		RequestID:           "bridge-db",
		EventHash:           "bridge-db-hash",
		TimestampMS:         timestampMS,
		Timestamp:           time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:               "gpt-5",
		ServiceTier:         "auto",
		ResponseServiceTier: "default",
		ResponseHeaders: http.Header{
			"X-Codex-Plan-Type":            []string{"pro"},
			"X-Codex-Primary-Used-Percent": []string{"91.5%"},
			"X-Oai-Request-Id":             []string{"trace-bridge"},
			"Authorization":                []string{"Bearer sk-secret"},
		},
		CreatedAtMS: timestampMS,
	})
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store, err := plusstore.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	events, err := store.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("RecentEvents len = %d, want 1", len(events))
	}
	event := events[0]
	if event.ServiceTier != "auto" || event.ResponseServiceTier != "default" {
		t.Fatalf("service tiers = request %q response %q", event.ServiceTier, event.ResponseServiceTier)
	}
	if event.HeaderQuotaPlanType != "pro" || event.HeaderTraceID != "trace-bridge" {
		t.Fatalf("derived header fields = %+v", event)
	}
	if strings.Contains(event.ResponseMetadataJSON, "sk-secret") || strings.Contains(strings.ToLower(event.ResponseMetadataJSON), "authorization") {
		t.Fatalf("unsafe response metadata: %s", event.ResponseMetadataJSON)
	}
}

func TestBridgeWorkerUsesServiceContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bridge, err := NewBridge(BridgeConfig{})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	bridge.Start(ctx)
	cancel()

	deadline := time.Now().Add(time.Second)
	for {
		if bridge.Context().Err() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bridge context was not cancelled by parent service context")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := bridge.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
