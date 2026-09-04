package usage

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const defaultCollectorFlushInterval = time.Second

type Event struct {
	RequestID             string
	TimestampMS           int64
	Timestamp             string
	Provider              string
	ExecutorType          string
	Model                 string
	RequestedModel        string
	ResolvedModel         string
	Endpoint              string
	Method                string
	Path                  string
	AuthType              string
	AuthIndex             string
	AuthLabelSnapshot     string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	Source                string
	SourceHash            string
	APIKeyHash            string
	ReasoningEffort       string
	ServiceTier           string
	ResponseServiceTier   string
	InputTokens           int64
	OutputTokens          int64
	ReasoningTokens       int64
	CachedTokens          int64
	CacheTokens           int64
	CacheReadTokens       int64
	CacheCreationTokens   int64
	TotalTokens           int64
	LatencyMS             *int64
	TTFTMS                *int64
	Failed                bool
	FailStatusCode        int
	FailSummary           string
	FailBody              string
	ResponseHeaders       http.Header
	HeaderTraceID         string
	ResponseMetadataJSON  string
	CreatedAtMS           int64
	EventHash             string
}

type CollectorStats struct {
	TotalInserted  int64
	TotalDropped   int64
	LastInsertedAt int64
	LastError      string
	QueueSize      int
}

type EventStore interface {
	InsertEvents(context.Context, []Event) error
	Events(context.Context) ([]Event, error)
}

type MemoryEventStore struct {
	mu     sync.Mutex
	events []Event
}

func NewMemoryEventStore() *MemoryEventStore {
	return &MemoryEventStore{}
}

func (s *MemoryEventStore) InsertEvents(_ context.Context, events []Event) error {
	if s == nil || len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		s.events = append(s.events, cloneEvent(event))
	}
	return nil
}

func (s *MemoryEventStore) Events(_ context.Context) ([]Event, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	for i := range s.events {
		out[i] = cloneEvent(s.events[i])
	}
	return out, nil
}

type Collector struct {
	store EventStore

	mu      sync.Mutex
	flushMu sync.Mutex
	events  []Event
	closed  bool

	cancel context.CancelFunc
	done   chan struct{}

	flushInterval time.Duration

	totalInserted  atomic.Int64
	totalDropped   atomic.Int64
	lastInsertedAt atomic.Int64

	lastErrorMu sync.RWMutex
	lastError   string
}

func NewCollector() *Collector {
	return NewCollectorWithStore(NewMemoryEventStore())
}

func NewCollectorWithStore(store EventStore) *Collector {
	if store == nil {
		store = NewMemoryEventStore()
	}
	return &Collector{
		store:         store,
		flushInterval: defaultCollectorFlushInterval,
	}
}

func (c *Collector) Start(ctx context.Context) {
	if c == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.done != nil {
		c.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	flushInterval := c.flushInterval
	if flushInterval <= 0 {
		flushInterval = defaultCollectorFlushInterval
	}
	c.cancel = cancel
	c.done = done
	c.closed = false
	c.mu.Unlock()

	go c.run(workerCtx, done, flushInterval)
}

func (c *Collector) run(ctx context.Context, done chan struct{}, flushInterval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Flush(ctx)
		}
	}
}

func (c *Collector) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.closed = true
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			c.setLastError(ctx.Err().Error())
			return ctx.Err()
		}
	}
	if err := c.Flush(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	if c.done == done {
		c.cancel = nil
		c.done = nil
	}
	c.mu.Unlock()
	return nil
}

func (c *Collector) RecordEvent(event Event) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		c.totalDropped.Add(1)
		return
	}
	c.events = append(c.events, cloneEvent(event))
}

func (c *Collector) Flush(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if len(c.events) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := make([]Event, len(c.events))
	for i := range c.events {
		batch[i] = cloneEvent(c.events[i])
	}
	c.events = c.events[:0]
	c.mu.Unlock()

	if err := c.store.InsertEvents(ctx, batch); err != nil {
		c.mu.Lock()
		c.events = append(batch, c.events...)
		c.mu.Unlock()
		c.setLastError(err.Error())
		return err
	}
	c.totalInserted.Add(int64(len(batch)))
	c.lastInsertedAt.Store(time.Now().UnixMilli())
	c.setLastError("")
	return nil
}

func (c *Collector) Events() []Event {
	if c == nil {
		return nil
	}
	if err := c.Flush(context.Background()); err != nil {
		return nil
	}
	events, err := c.store.Events(context.Background())
	if err != nil {
		c.setLastError(err.Error())
		return nil
	}
	return events
}

func (c *Collector) BufferedEvents() []Event {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	for i := range c.events {
		out[i] = cloneEvent(c.events[i])
	}
	return out
}

func (c *Collector) Stats() CollectorStats {
	if c == nil {
		return CollectorStats{}
	}
	c.mu.Lock()
	queueSize := len(c.events)
	c.mu.Unlock()
	c.lastErrorMu.RLock()
	lastErr := c.lastError
	c.lastErrorMu.RUnlock()
	return CollectorStats{
		TotalInserted:  c.totalInserted.Load(),
		TotalDropped:   c.totalDropped.Load(),
		LastInsertedAt: c.lastInsertedAt.Load(),
		LastError:      lastErr,
		QueueSize:      queueSize,
	}
}

func (c *Collector) setLastError(msg string) {
	c.lastErrorMu.Lock()
	c.lastError = msg
	c.lastErrorMu.Unlock()
}

func cloneEvent(event Event) Event {
	event.ResponseHeaders = event.ResponseHeaders.Clone()
	return event
}
