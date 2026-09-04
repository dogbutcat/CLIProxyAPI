package usage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type BridgeConfig struct {
	DBPath        string
	Store         EventStore
	ImportSession config.UsageImportSessionConfig
}

type Bridge struct {
	mu      sync.RWMutex
	closeMu sync.Mutex

	dbPath    string
	collector *Collector
	store     interface{ Close() error }
	events    []Event

	started bool
	closed  bool
	cancel  context.CancelFunc
	ctx     context.Context

	importSessionManager *usageImportSessionManager
	importCleanupDone    <-chan struct{}
}

func NewBridge(cfg BridgeConfig) (*Bridge, error) {
	eventStore := cfg.Store
	var closeStore interface{ Close() error }
	if eventStore == nil && cfg.DBPath != "" {
		store, err := plusstore.OpenStore(cfg.DBPath)
		if err != nil {
			return nil, err
		}
		eventStore = sqliteEventStore{store: store}
		closeStore = store
	}
	collector := NewCollectorWithStore(eventStore)
	importSessionManager, err := newBridgeImportSessionManager(cfg)
	if err != nil {
		if closeStore != nil {
			_ = closeStore.Close()
		}
		return nil, err
	}
	return &Bridge{
		dbPath:               cfg.DBPath,
		collector:            collector,
		store:                closeStore,
		ctx:                  context.Background(),
		importSessionManager: importSessionManager,
	}, nil
}

func (b *Bridge) Start(ctx context.Context) {
	if b == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.started {
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.ctx = workerCtx
	b.started = true
	b.collector.Start(workerCtx)
	if b.importSessionManager != nil {
		b.importCleanupDone = b.importSessionManager.StartCleanupLoop(workerCtx, time.Minute)
	}
}

func (b *Bridge) Close(ctxs ...context.Context) error {
	if b == nil {
		return nil
	}
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	cancel := b.cancel
	collector := b.collector
	importCleanupDone := b.importCleanupDone
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if collector != nil {
		if err := collector.Stop(ctx); err != nil {
			return fmt.Errorf("close usage bridge: stop collector: %w", err)
		}
		b.mu.Lock()
		b.events = collector.Events()
		b.mu.Unlock()
	}
	if importCleanupDone != nil {
		select {
		case <-importCleanupDone:
		case <-ctx.Done():
			return fmt.Errorf("close usage bridge: stop import session cleanup: %w", ctx.Err())
		}
	}
	if b.store != nil {
		if err := b.store.Close(); err != nil {
			return fmt.Errorf("close usage bridge: close store: %w", err)
		}
	}
	b.mu.Lock()
	b.closed = true
	b.cancel = nil
	b.mu.Unlock()
	return nil
}

func (b *Bridge) Context() context.Context {
	if b == nil {
		return context.Background()
	}
	b.mu.RLock()
	ctx := b.ctx
	b.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (b *Bridge) Collector() *Collector {
	if b == nil {
		return nil
	}
	return b.collector
}

func (b *Bridge) Events() []Event {
	if b == nil || b.collector == nil {
		return nil
	}
	b.mu.RLock()
	if b.closed {
		events := make([]Event, len(b.events))
		for i := range b.events {
			events[i] = cloneEvent(b.events[i])
		}
		b.mu.RUnlock()
		return events
	}
	b.mu.RUnlock()
	return b.collector.Events()
}

func (b *Bridge) DBPath() string {
	if b == nil {
		return ""
	}
	return b.dbPath
}

func newBridgeImportSessionManager(cfg BridgeConfig) (*usageImportSessionManager, error) {
	importCfg := cfg.ImportSession.WithDefaults()
	root := strings.TrimSpace(importCfg.Dir)
	if root == "" && strings.TrimSpace(cfg.DBPath) != "" {
		root = filepath.Join(filepath.Dir(strings.TrimSpace(cfg.DBPath)), "usage-import-sessions")
	}
	if root == "" {
		return nil, nil
	}
	importCfg.Dir = root
	if err := importCfg.Validate(); err != nil {
		return nil, err
	}
	return newUsageImportSessionManager(root, importCfg)
}

type sqliteEventStore struct {
	store *plusstore.Store
}

func (s sqliteEventStore) InsertEvents(ctx context.Context, events []Event) error {
	if s.store == nil || len(events) == 0 {
		return nil
	}
	converted := make([]plusstore.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, bridgeEventToStoreEvent(event))
	}
	_, err := s.store.InsertEvents(ctx, converted)
	return err
}

func (s sqliteEventStore) Events(ctx context.Context) ([]Event, error) {
	if s.store == nil {
		return nil, nil
	}
	events, err := s.store.RecentEvents(ctx, 50000)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(events))
	for _, event := range events {
		out = append(out, storeEventToBridgeEvent(event))
	}
	return out, nil
}

func bridgeEventToStoreEvent(event Event) plusstore.Event {
	metadata := bridgeResponseHeaderMetadata(event)
	if event.EventHash == "" {
		event.EventHash = plusstore.BuildEventHash(plusstore.Event{
			RequestID:    event.RequestID,
			Timestamp:    event.Timestamp,
			Endpoint:     event.Endpoint,
			Model:        event.Model,
			AuthIndex:    event.AuthIndex,
			SourceHash:   event.SourceHash,
			InputTokens:  event.InputTokens,
			OutputTokens: event.OutputTokens,
			Failed:       event.Failed,
		})
	}
	return plusstore.Event{
		RequestID:             event.RequestID,
		EventHash:             event.EventHash,
		TimestampMS:           event.TimestampMS,
		Timestamp:             event.Timestamp,
		Provider:              event.Provider,
		ExecutorType:          event.ExecutorType,
		Model:                 event.Model,
		RequestedModel:        event.RequestedModel,
		ResolvedModel:         event.ResolvedModel,
		Endpoint:              event.Endpoint,
		Method:                event.Method,
		Path:                  event.Path,
		AuthType:              event.AuthType,
		AuthIndex:             event.AuthIndex,
		Source:                event.Source,
		SourceHash:            event.SourceHash,
		APIKeyHash:            event.APIKeyHash,
		AuthLabelSnapshot:     event.AuthLabelSnapshot,
		AuthProviderSnapshot:  event.AuthProviderSnapshot,
		AuthProjectIDSnapshot: event.AuthProjectIDSnapshot,
		ReasoningEffort:       event.ReasoningEffort,
		ServiceTier:           event.ServiceTier,
		ResponseServiceTier:   event.ResponseServiceTier,
		InputTokens:           event.InputTokens,
		OutputTokens:          event.OutputTokens,
		ReasoningTokens:       event.ReasoningTokens,
		CachedTokens:          event.CachedTokens,
		CacheTokens:           event.CacheTokens,
		CacheReadTokens:       event.CacheReadTokens,
		CacheCreationTokens:   event.CacheCreationTokens,
		TotalTokens:           event.TotalTokens,
		LatencyMS:             event.LatencyMS,
		TTFTMS:                event.TTFTMS,
		Failed:                event.Failed,
		FailStatusCode:        event.FailStatusCode,
		FailSummary:           event.FailSummary,
		FailBody:              event.FailBody,
		ResponseMetadata:      metadata,
		ResponseMetadataJSON:  event.ResponseMetadataJSON,
		HeaderTraceID:         event.HeaderTraceID,
		CreatedAtMS:           event.CreatedAtMS,
	}
}

func storeEventToBridgeEvent(event plusstore.Event) Event {
	return Event{
		RequestID:             event.RequestID,
		EventHash:             event.EventHash,
		TimestampMS:           event.TimestampMS,
		Timestamp:             event.Timestamp,
		Provider:              event.Provider,
		ExecutorType:          event.ExecutorType,
		Model:                 event.Model,
		RequestedModel:        event.RequestedModel,
		ResolvedModel:         event.ResolvedModel,
		Endpoint:              event.Endpoint,
		Method:                event.Method,
		Path:                  event.Path,
		AuthType:              event.AuthType,
		AuthIndex:             event.AuthIndex,
		AuthLabelSnapshot:     event.AuthLabelSnapshot,
		AuthProviderSnapshot:  event.AuthProviderSnapshot,
		AuthProjectIDSnapshot: event.AuthProjectIDSnapshot,
		Source:                event.Source,
		SourceHash:            event.SourceHash,
		APIKeyHash:            event.APIKeyHash,
		ReasoningEffort:       event.ReasoningEffort,
		ServiceTier:           event.ServiceTier,
		ResponseServiceTier:   event.ResponseServiceTier,
		InputTokens:           event.InputTokens,
		OutputTokens:          event.OutputTokens,
		ReasoningTokens:       event.ReasoningTokens,
		CachedTokens:          event.CachedTokens,
		CacheTokens:           event.CacheTokens,
		CacheReadTokens:       event.CacheReadTokens,
		CacheCreationTokens:   event.CacheCreationTokens,
		TotalTokens:           event.TotalTokens,
		LatencyMS:             event.LatencyMS,
		TTFTMS:                event.TTFTMS,
		Failed:                event.Failed,
		FailStatusCode:        event.FailStatusCode,
		FailSummary:           event.FailSummary,
		ResponseMetadataJSON:  event.ResponseMetadataJSON,
		HeaderTraceID:         event.HeaderTraceID,
		CreatedAtMS:           event.CreatedAtMS,
	}
}

func bridgeResponseHeaderMetadata(event Event) *plusstore.ResponseHeaderMetadata {
	if len(event.ResponseHeaders) != 0 {
		headers := make(map[string]any, len(event.ResponseHeaders))
		for key, values := range event.ResponseHeaders {
			copied := make([]string, len(values))
			copy(copied, values)
			headers[key] = copied
		}
		base := time.UnixMilli(event.TimestampMS)
		if event.TimestampMS <= 0 {
			base = time.Time{}
		}
		if metadata := plusstore.ParseResponseHeaderMetadata(headers, base); metadata != nil {
			return metadata
		}
	}
	return plusstore.ResponseHeaderMetadataFromJSON(event.ResponseMetadataJSON)
}
