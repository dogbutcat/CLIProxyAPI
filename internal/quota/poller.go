package quota

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// PollResult holds the quota polling result for a single OpenCode Go entry.
type PollResult struct {
	EntryName string
	Quota     *OpenCodeGoQuota
	Error     error
	Timestamp time.Time
}

// PollerEntry defines a single OpenCode Go entry for quota polling.
type PollerEntry struct {
	Name        string
	WorkspaceID string
	AuthCookie  string
	ProxyURL    string
}

// DashboardFetcher fetches dashboard HTML for a poll input.
type DashboardFetcher func(ctx context.Context, input DashboardFetchInput) (string, error)

// PollAttemptCompletion receives each aliased result published by one poll attempt.
type PollAttemptCompletion func(entry PollerEntry, result *PollResult)

// PollAttemptSource identifies how a quota poll attempt was initiated. Its zero value is invalid.
type PollAttemptSource int

const (
	PollAttemptSourceInvalid PollAttemptSource = iota
	PollAttemptSourceManual
	PollAttemptSourceScheduled
)

// PollAttemptPreparer captures attempt-local state before dashboard network I/O begins.
type PollAttemptPreparer func(source PollAttemptSource, aliases []PollerEntry) PollAttemptCompletion

// PollerConfig holds configuration for the quota poller.
type PollerConfig struct {
	Interval           time.Duration
	Entries            []PollerEntry
	OnResult           func(entry PollerEntry, result *PollResult)
	FetchDashboard     DashboardFetcher
	PreparePollAttempt PollAttemptPreparer
}

// Poller performs periodic background quota polling for OpenCode Go entries.
type Poller struct {
	mu sync.RWMutex

	interval           time.Duration
	entries            []PollerEntry
	entriesByName      map[string]PollerEntry
	aliasesByKey       map[string][]PollerEntry
	results            map[string]*PollResult
	skipped            map[string]bool
	onResult           func(entry PollerEntry, result *PollResult)
	fetchDashboard     DashboardFetcher
	preparePollAttempt PollAttemptPreparer

	cancel context.CancelFunc
	done   chan struct{}
}

// NewPoller creates a new quota poller. It does not start polling until Start is called.
func NewPoller(cfg PollerConfig) *Poller {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	fetcher := cfg.FetchDashboard
	if fetcher == nil {
		fetcher = FetchOpenCodeGoDashboard
	}
	entries, entriesByName, aliasesByKey := normalizePollerEntries(cfg.Entries)
	return &Poller{
		interval:           interval,
		entries:            entries,
		entriesByName:      entriesByName,
		aliasesByKey:       aliasesByKey,
		results:            make(map[string]*PollResult),
		skipped:            make(map[string]bool),
		onResult:           cfg.OnResult,
		fetchDashboard:     fetcher,
		preparePollAttempt: cfg.PreparePollAttempt,
	}
}

// Start begins background polling. It is safe to call multiple times.
func (p *Poller) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil || len(p.entries) == 0 {
		return
	}

	pollCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p.cancel = cancel
	p.done = done
	go p.run(pollCtx, done)
}

// Stop halts background polling and waits for in-flight work to observe cancellation.
func (p *Poller) Stop() {
	p.mu.RLock()
	cancel := p.cancel
	done := p.done
	p.mu.RUnlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Results returns a deep snapshot of all current poll results.
func (p *Poller) Results() map[string]*PollResult {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[string]*PollResult, len(p.results))
	for k, v := range p.results {
		out[k] = clonePollResult(v)
	}
	return out
}

// PollSingle performs a live poll for one configured entry and updates its snapshot.
func (p *Poller) PollSingle(ctx context.Context, name string) *PollResult {
	if ctx == nil {
		ctx = context.Background()
	}

	p.mu.RLock()
	entry, ok := p.entriesByName[name]
	p.mu.RUnlock()
	if !ok {
		return nil
	}

	result, published := p.pollAndPublish(ctx, PollAttemptSourceManual, entry)
	if requested := published[name]; requested != nil {
		return requested
	}
	return clonePollResult(result)
}

// SkipEntry excludes a configured entry's deduplicated workspace from scheduled polling.
// Manual PollSingle calls bypass the skip state so recovery checks can still run.
func (p *Poller) SkipEntry(name string) bool {
	key := ""
	name = strings.TrimSpace(name)

	p.mu.Lock()
	defer p.mu.Unlock()
	if name != "" {
		key = p.skipKeyForNameLocked(name)
	}
	if key == "" {
		return false
	}
	if p.skipped[key] {
		return false
	}
	p.skipped[key] = true
	return true
}

// UnskipEntry resumes scheduled polling for a configured entry's deduplicated workspace.
func (p *Poller) UnskipEntry(name string) bool {
	key := ""
	name = strings.TrimSpace(name)

	p.mu.Lock()
	defer p.mu.Unlock()
	if name != "" {
		key = p.skipKeyForNameLocked(name)
	}
	if key == "" || !p.skipped[key] {
		return false
	}
	delete(p.skipped, key)
	return true
}

// IsSkipped reports whether a configured entry's deduplicated workspace is skipped.
func (p *Poller) IsSkipped(name string) bool {
	key := ""
	name = strings.TrimSpace(name)

	p.mu.RLock()
	defer p.mu.RUnlock()
	if name != "" {
		key = p.skipKeyForNameLocked(name)
	}
	return key != "" && p.skipped[key]
}

func (p *Poller) run(ctx context.Context, done chan struct{}) {
	defer func() {
		close(done)
		p.mu.Lock()
		if p.done == done {
			p.cancel = nil
			p.done = nil
		}
		p.mu.Unlock()
	}()

	p.pollAll(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollAll(ctx)
		}
	}
}

func (p *Poller) pollAll(ctx context.Context) {
	p.mu.RLock()
	entries := make([]PollerEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		if !p.isEntrySkippedLocked(entry) {
			entries = append(entries, entry)
		}
	}
	p.mu.RUnlock()

	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		p.pollAndPublish(ctx, PollAttemptSourceScheduled, entry)
	}
}

func (p *Poller) pollAndPublish(ctx context.Context, source PollAttemptSource, entry PollerEntry) (*PollResult, map[string]*PollResult) {
	aliases := p.aliasesForEntry(entry)
	var completion PollAttemptCompletion
	if p.preparePollAttempt != nil {
		completion = p.preparePollAttempt(source, append([]PollerEntry(nil), aliases...))
	}

	result := p.pollEntry(ctx, entry)
	return result, p.publishResult(aliases, result, completion)
}

func (p *Poller) pollEntry(ctx context.Context, entry PollerEntry) *PollResult {
	result := &PollResult{EntryName: entry.Name, Timestamp: time.Now()}
	if strings.TrimSpace(entry.WorkspaceID) == "" {
		result.Error = fmt.Errorf("missing workspace id for quota poll entry %q", entry.Name)
		return result
	}
	if strings.TrimSpace(entry.AuthCookie) == "" {
		result.Error = fmt.Errorf("missing auth cookie for quota poll entry %q", entry.Name)
		return result
	}

	html, err := p.fetchDashboard(ctx, DashboardFetchInput{
		WorkspaceID: entry.WorkspaceID,
		AuthCookie:  entry.AuthCookie,
		ProxyURL:    entry.ProxyURL,
	})
	if err != nil {
		result.Error = fmt.Errorf("fetch OpenCode Go quota for entry %q: %w", entry.Name, err)
		result.Timestamp = time.Now()
		return result
	}

	quota, err := ParseOpenCodeGoHTML(html)
	if err != nil {
		result.Error = fmt.Errorf("parse OpenCode Go quota for entry %q: %w", entry.Name, err)
		result.Timestamp = time.Now()
		return result
	}

	result.Quota = quota
	result.Timestamp = time.Now()
	logPolledQuota(entry, quota)
	return result
}

func (p *Poller) publishResult(aliases []PollerEntry, result *PollResult, completion PollAttemptCompletion) map[string]*PollResult {
	published := make(map[string]*PollResult, len(aliases))

	p.mu.Lock()
	for _, alias := range aliases {
		aliasResult := clonePollResult(result)
		aliasResult.EntryName = alias.Name
		p.results[alias.Name] = aliasResult
		published[alias.Name] = clonePollResult(aliasResult)
	}
	p.mu.Unlock()

	if completion != nil {
		for _, alias := range aliases {
			completion(alias, clonePollResult(published[alias.Name]))
		}
	}

	if p.onResult != nil {
		for _, alias := range aliases {
			p.onResult(alias, clonePollResult(published[alias.Name]))
		}
	}
	return published
}

func (p *Poller) aliasesForEntry(entry PollerEntry) []PollerEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	aliases := p.aliasesByKey[normalizedPollerEntryKey(entry)]
	if len(aliases) == 0 {
		return []PollerEntry{entry}
	}
	return append([]PollerEntry(nil), aliases...)
}

func (p *Poller) skipKeyForNameLocked(name string) string {
	if entry, ok := p.entriesByName[name]; ok {
		return pollerSkipKey(entry)
	}
	for _, entry := range p.entriesByName {
		if strings.TrimSpace(entry.WorkspaceID) == name {
			return pollerSkipKey(entry)
		}
	}
	return ""
}

func (p *Poller) isEntrySkippedLocked(entry PollerEntry) bool {
	key := pollerSkipKey(entry)
	return key != "" && p.skipped[key]
}

func normalizePollerEntries(entries []PollerEntry) ([]PollerEntry, map[string]PollerEntry, map[string][]PollerEntry) {
	canonicalByKey := make(map[string]PollerEntry)
	entriesByName := make(map[string]PollerEntry)
	aliasesByKey := make(map[string][]PollerEntry)
	var order []string

	for _, entry := range entries {
		entry = normalizePollerEntry(entry)
		if entry.Name == "" {
			continue
		}
		if _, exists := entriesByName[entry.Name]; exists {
			continue
		}

		key := normalizedPollerEntryKey(entry)
		entriesByName[entry.Name] = entry
		aliasesByKey[key] = append(aliasesByKey[key], entry)

		canonical, exists := canonicalByKey[key]
		if !exists {
			canonicalByKey[key] = entry
			order = append(order, key)
			continue
		}
		if strings.TrimSpace(canonical.AuthCookie) == "" && strings.TrimSpace(entry.AuthCookie) != "" {
			canonicalByKey[key] = entry
		}
	}

	canonicalEntries := make([]PollerEntry, 0, len(order))
	for _, key := range order {
		canonicalEntries = append(canonicalEntries, canonicalByKey[key])
	}
	return canonicalEntries, entriesByName, aliasesByKey
}

func normalizePollerEntry(entry PollerEntry) PollerEntry {
	entry.Name = strings.TrimSpace(entry.Name)
	entry.WorkspaceID = strings.TrimSpace(entry.WorkspaceID)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	if entry.Name == "" {
		entry.Name = entry.WorkspaceID
	}
	return entry
}

func normalizedPollerEntryKey(entry PollerEntry) string {
	if key := pollerEntryKey(entry); key != "" {
		return key
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		return ""
	}
	return "name:" + name
}

func pollerEntryKey(entry PollerEntry) string {
	workspaceID := strings.TrimSpace(entry.WorkspaceID)
	if workspaceID == "" {
		return ""
	}
	return workspaceID + "\x00" + strings.TrimSpace(entry.ProxyURL)
}

func pollerSkipKey(entry PollerEntry) string {
	if key := pollerEntryKey(entry); key != "" {
		return "workspace:" + key
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		return ""
	}
	return "name:" + name
}

func clonePollResult(result *PollResult) *PollResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Quota = cloneQuota(result.Quota)
	return &clone
}

func cloneQuota(quota *OpenCodeGoQuota) *OpenCodeGoQuota {
	if quota == nil {
		return nil
	}
	return &OpenCodeGoQuota{
		Rolling: cloneWindow(quota.Rolling),
		Weekly:  cloneWindow(quota.Weekly),
		Monthly: cloneWindow(quota.Monthly),
	}
}

func cloneWindow(window *OpenCodeGoWindow) *OpenCodeGoWindow {
	if window == nil {
		return nil
	}
	clone := *window
	return &clone
}

func logPolledQuota(entry PollerEntry, quota *OpenCodeGoQuota) {
	fields := log.Fields{
		"source": "opencode-go:quota",
		"entry":  entry.Name,
	}
	if quota.Rolling != nil {
		fields["rolling_remaining"] = fmt.Sprintf("%.1f%%", quota.Rolling.PercentRemaining)
	}
	if quota.Weekly != nil {
		fields["weekly_remaining"] = fmt.Sprintf("%.1f%%", quota.Weekly.PercentRemaining)
	}
	if quota.Monthly != nil {
		fields["monthly_remaining"] = fmt.Sprintf("%.1f%%", quota.Monthly.PercentRemaining)
	}
	log.WithFields(fields).Info("opencode-go quota polled")
}
