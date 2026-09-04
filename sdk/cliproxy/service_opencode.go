package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	quotahub "github.com/router-for-me/CLIProxyAPI/v7/internal/quota/hub"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const openCodeGoProviderKey = "opencode-go"

type serviceOpenCodeRuntime struct {
	attemptMu          sync.Mutex
	mu                 sync.Mutex
	configKey          string
	started            bool
	generation         uint64
	ctx                context.Context
	cancel             context.CancelFunc
	poller             *quota.Poller
	manager            *coreauth.Manager
	threshold          float64
	thresholdOK        bool
	entriesByWorkspace map[string]quota.PollerEntry
	wg                 sync.WaitGroup

	fetchDashboard quota.DashboardFetcher

	// testLifecycleEvent is a test-only hook for deterministic lifecycle coordination.
	testLifecycleMu    sync.RWMutex
	testLifecycleEvent func(string, *coreauth.Manager, uint64)
}

type openCodeRuntimeState struct {
	configKey   string
	interval    time.Duration
	entries     []quota.PollerEntry
	threshold   float64
	thresholdOK bool
}

func (s *Service) syncOpenCodeRuntimeConfig(ctx context.Context, cfg *config.Config) bool {
	if s == nil {
		return true
	}
	return s.openCodeRuntime.syncConfig(ctx, cfg, s.coreManager)
}

func (s *Service) stopOpenCodeRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.openCodeRuntime.stop(ctx)
}

func (r *serviceOpenCodeRuntime) syncConfig(ctx context.Context, cfg *config.Config, managers ...*coreauth.Manager) bool {
	if r == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	var manager *coreauth.Manager
	if len(managers) > 0 {
		manager = managers[0]
	}
	state := buildOpenCodeRuntimeState(cfg)
	if state.configKey == "" || len(state.entries) == 0 || manager == nil {
		return r.replace(ctx, openCodeRuntimeState{}, nil)
	}
	return r.replace(ctx, state, manager)
}

func (r *serviceOpenCodeRuntime) replace(ctx context.Context, state openCodeRuntimeState, manager *coreauth.Manager) bool {
	r.emitTestLifecycleEvent("before_replace_gate", manager, 0)
	r.attemptMu.Lock()
	r.mu.Lock()
	if r.started && r.configKey == state.configKey && r.manager == manager {
		r.mu.Unlock()
		r.attemptMu.Unlock()
		return true
	}
	oldPoller := r.poller
	oldCancel := r.cancel
	oldManager := r.manager
	r.generation++
	generation := r.generation
	r.configKey = state.configKey
	r.manager = manager
	r.threshold = state.threshold
	r.thresholdOK = state.thresholdOK
	r.entriesByWorkspace = buildOpenCodeRuntimeEntriesByWorkspace(state.entries)
	r.poller = nil
	r.cancel = nil
	r.ctx = nil
	r.started = false
	if manager == nil || state.configKey == "" || len(state.entries) == 0 {
		r.mu.Unlock()
		r.emitTestLifecycleEvent("replace_state_published", manager, generation)
		if oldManager != nil {
			oldManager.SetQuotaRefreshCallback(nil)
			r.emitTestLifecycleEvent("callback_cleared", oldManager, generation)
		}
		r.attemptMu.Unlock()
		stopOpenCodePoller(oldCancel, oldPoller)
		r.wg.Wait()
		return ctx.Err() == nil
	}

	runtimeCtx, cancel := context.WithCancel(ctx)
	fetcher := r.fetchDashboard
	poller := quota.NewPoller(quota.PollerConfig{
		Interval:       state.interval,
		Entries:        state.entries,
		FetchDashboard: fetcher,
		PreparePollAttempt: func(source quota.PollAttemptSource, aliases []quota.PollerEntry) quota.PollAttemptCompletion {
			return r.preparePollAttempt(generation, source, aliases)
		},
	})
	r.ctx = runtimeCtx
	r.cancel = cancel
	r.poller = poller
	r.started = true
	r.mu.Unlock()
	r.emitTestLifecycleEvent("replace_state_published", manager, generation)

	manager.SetQuotaRefreshCallback(func(authID string) {
		r.pollSingleAsync(generation, authID)
	})
	r.emitTestLifecycleEvent("callback_installed", manager, generation)
	if oldManager != nil && oldManager != manager {
		oldManager.SetQuotaRefreshCallback(nil)
		r.emitTestLifecycleEvent("callback_cleared", oldManager, generation)
	}
	r.attemptMu.Unlock()
	stopOpenCodePoller(oldCancel, oldPoller)
	poller.Start(runtimeCtx)
	return ctx.Err() == nil
}

// SnapshotOpenCodeGoQuota returns a deep copy of quota poller snapshots for Management APIs.
func (r *serviceOpenCodeRuntime) SnapshotOpenCodeGoQuota() map[string]*quota.PollResult {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	poller := r.poller
	started := r.started
	r.mu.Unlock()
	if !started || poller == nil {
		return nil
	}
	return poller.Results()
}

// RefreshOpenCodeGoQuota manually polls one configured quota entry for Management APIs.
func (r *serviceOpenCodeRuntime) RefreshOpenCodeGoQuota(ctx context.Context, entryName string) (*quota.PollResult, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	entryName = strings.TrimSpace(entryName)
	if entryName == "" {
		return nil, true, nil
	}

	r.mu.Lock()
	poller := r.poller
	started := r.started
	runtimeCtx := r.ctx
	r.mu.Unlock()
	if !started || poller == nil || runtimeCtx == nil || runtimeCtx.Err() != nil {
		return nil, false, nil
	}
	result := poller.PollSingle(ctx, entryName)
	if result == nil {
		if alias := r.openCodeRuntimeEntryForWorkspace(entryName); alias.Name != "" {
			result = poller.PollSingle(ctx, alias.Name)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, true, err
	}
	return result, true, nil
}

// ResolveOpenCodeGoReferralWorkspace returns the current referral credential for one workspace.
func (r *serviceOpenCodeRuntime) ResolveOpenCodeGoReferralWorkspace(ctx context.Context, workspaceID string) (string, string, bool, bool, error) {
	if r == nil {
		return "", "", false, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", true, false, err
	}
	entry := r.openCodeRuntimeEntryForWorkspace(workspaceID)
	if entry.Name == "" {
		r.mu.Lock()
		started := r.started
		runtimeCtx := r.ctx
		r.mu.Unlock()
		if !started || runtimeCtx == nil || runtimeCtx.Err() != nil {
			return "", "", false, false, nil
		}
		return "", "", true, false, nil
	}
	return strings.TrimSpace(entry.AuthCookie), strings.TrimSpace(entry.ProxyURL), true, true, nil
}

func (r *serviceOpenCodeRuntime) openCodeRuntimeEntryForWorkspace(workspaceID string) quota.PollerEntry {
	workspaceID = strings.TrimSpace(workspaceID)
	if r == nil || workspaceID == "" {
		return quota.PollerEntry{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.ctx == nil || r.ctx.Err() != nil {
		return quota.PollerEntry{}
	}
	return r.entriesByWorkspace[workspaceID]
}

func (r *serviceOpenCodeRuntime) stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.emitTestLifecycleEvent("before_stop_gate", nil, 0)
	r.attemptMu.Lock()
	r.mu.Lock()
	manager := r.manager
	oldPoller := r.poller
	oldCancel := r.cancel
	r.generation++
	generation := r.generation
	r.configKey = ""
	r.started = false
	r.ctx = nil
	r.cancel = nil
	r.poller = nil
	r.manager = nil
	r.threshold = 0
	r.thresholdOK = false
	r.entriesByWorkspace = nil
	r.mu.Unlock()
	r.emitTestLifecycleEvent("stop_state_published", manager, generation)

	if manager != nil {
		manager.SetQuotaRefreshCallback(nil)
		r.emitTestLifecycleEvent("callback_cleared", manager, generation)
	}
	r.attemptMu.Unlock()
	stopOpenCodePoller(oldCancel, oldPoller)
	r.wg.Wait()
	return ctx.Err()
}

func (r *serviceOpenCodeRuntime) emitTestLifecycleEvent(event string, manager *coreauth.Manager, generation uint64) {
	if r == nil {
		return
	}
	r.testLifecycleMu.RLock()
	hook := r.testLifecycleEvent
	r.testLifecycleMu.RUnlock()
	if hook != nil {
		hook(event, manager, generation)
	}
}

func (r *serviceOpenCodeRuntime) setTestLifecycleEvent(hook func(string, *coreauth.Manager, uint64)) {
	if r == nil {
		return
	}
	r.testLifecycleMu.Lock()
	r.testLifecycleEvent = hook
	r.testLifecycleMu.Unlock()
}

func stopOpenCodePoller(cancel context.CancelFunc, poller *quota.Poller) {
	if cancel != nil {
		cancel()
	}
	if poller != nil {
		poller.Stop()
	}
}

func buildOpenCodeRuntimeState(cfg *config.Config) openCodeRuntimeState {
	key := openCodeRuntimeConfigKey(cfg)
	if key == "" {
		return openCodeRuntimeState{}
	}
	state := openCodeRuntimeState{
		configKey: key,
		interval:  5 * time.Minute,
		entries:   buildOpenCodeRuntimePollerEntries(cfg),
	}
	if intervalText := strings.TrimSpace(cfg.OpenCodeGo.Quota.PollInterval); intervalText != "" {
		if parsed, errParse := time.ParseDuration(intervalText); errParse == nil && parsed > 0 {
			state.interval = parsed
		}
	}
	if cfg.OpenCodeGo.Quota.Threshold != nil {
		state.threshold = *cfg.OpenCodeGo.Quota.Threshold
		state.thresholdOK = true
	}
	return state
}

func buildOpenCodeRuntimeEntriesByWorkspace(entries []quota.PollerEntry) map[string]quota.PollerEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]quota.PollerEntry)
	for _, entry := range entries {
		workspaceID := strings.TrimSpace(entry.WorkspaceID)
		if workspaceID == "" {
			continue
		}
		if existing, ok := out[workspaceID]; ok && strings.TrimSpace(existing.AuthCookie) != "" {
			continue
		}
		out[workspaceID] = entry
	}
	return out
}

func buildOpenCodeRuntimePollerEntries(cfg *config.Config) []quota.PollerEntry {
	if cfg == nil || cfg.OpenCodeGo.IsZero() {
		return nil
	}
	auths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		return nil
	}
	entries := make([]quota.PollerEntry, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || !isOpenCodeGoProvider(auth.Provider) || auth.ID == "" || auth.Attributes == nil {
			continue
		}
		workspaceID := strings.TrimSpace(auth.Attributes["workspace_id"])
		authCookie := strings.TrimSpace(auth.Attributes["auth_cookie"])
		if workspaceID == "" || authCookie == "" {
			continue
		}
		entries = append(entries, quota.PollerEntry{
			Name:        auth.ID,
			WorkspaceID: workspaceID,
			AuthCookie:  authCookie,
			ProxyURL:    strings.TrimSpace(auth.ProxyURL),
		})
	}
	return entries
}

func (r *serviceOpenCodeRuntime) pollSingleAsync(generation uint64, authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	r.mu.Lock()
	if !r.started || r.generation != generation || r.poller == nil || r.ctx == nil || r.ctx.Err() != nil {
		r.mu.Unlock()
		return
	}
	poller := r.poller
	ctx := r.ctx
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		poller.PollSingle(ctx, authID)
	}()
}

func (r *serviceOpenCodeRuntime) preparePollAttempt(
	generation uint64,
	source quota.PollAttemptSource,
	aliases []quota.PollerEntry,
) quota.PollAttemptCompletion {
	hubSource, ok := openCodeHubSource(source)
	if !ok || len(aliases) == 0 {
		return nil
	}
	r.mu.Lock()
	if !r.started || r.generation != generation || r.manager == nil || r.ctx == nil || r.ctx.Err() != nil {
		r.mu.Unlock()
		return nil
	}
	manager := r.manager
	ctx := r.ctx
	poller := r.poller
	threshold := r.threshold
	thresholdOK := r.thresholdOK
	r.mu.Unlock()

	completions := make(map[string]quotahub.OpenCodeQuotaCompletion, len(aliases))
	for _, alias := range aliases {
		authID := strings.TrimSpace(alias.Name)
		if authID == "" {
			continue
		}
		resolved, found := manager.GetByID(authID)
		if !found || resolved == nil {
			continue
		}
		if completion := quotahub.BeginOpenCodeQuotaObservation(manager, resolved, hubSource, thresholdOK, threshold); completion != nil {
			completions[authID] = completion
		}
	}
	if len(completions) == 0 {
		return nil
	}
	return func(entry quota.PollerEntry, result *quota.PollResult) {
		authID := strings.TrimSpace(entry.Name)
		completion := completions[authID]
		if completion == nil {
			return
		}
		r.attemptMu.Lock()
		defer r.attemptMu.Unlock()
		r.mu.Lock()
		active := r.started && r.generation == generation && r.manager == manager && r.ctx == ctx &&
			r.poller == poller && ctx.Err() == nil
		r.mu.Unlock()
		if !active || !completion(ctx, result) || !thresholdOK {
			return
		}
		score, _, valid := openCodeRuntimeMinimumQuota(result.Quota)
		if valid {
			r.updatePollerSkipState(poller, entry, result.Quota, score <= threshold)
		}
	}
}

func openCodeHubSource(source quota.PollAttemptSource) (quotahub.SourceKind, bool) {
	switch source {
	case quota.PollAttemptSourceManual:
		return quotahub.OpenCodeManual, true
	case quota.PollAttemptSourceScheduled:
		return quotahub.OpenCodeScheduled, true
	default:
		return 0, false
	}
}

func (r *serviceOpenCodeRuntime) updatePollerSkipState(poller *quota.Poller, entry quota.PollerEntry, q *quota.OpenCodeGoQuota, thresholdExceeded bool) {
	if poller == nil || strings.TrimSpace(entry.Name) == "" {
		return
	}
	monthlyExhausted := q != nil && q.Monthly != nil && q.Monthly.PercentRemaining <= 0
	if monthlyExhausted && thresholdExceeded {
		poller.SkipEntry(entry.Name)
		return
	}
	poller.UnskipEntry(entry.Name)
}

func openCodeRuntimeMinimumQuota(q *quota.OpenCodeGoQuota) (float64, time.Time, bool) {
	if q == nil {
		return 0, time.Time{}, false
	}
	var score float64
	var resetAt time.Time
	found := false
	for _, window := range []*quota.OpenCodeGoWindow{q.Rolling, q.Weekly, q.Monthly} {
		if window == nil {
			continue
		}
		remaining := window.PercentRemaining
		if remaining < 0 {
			remaining = 0
		}
		if remaining > 100 {
			remaining = 100
		}
		if !found || remaining < score {
			score = remaining
			resetAt = window.ResetTime
			found = true
		}
	}
	return score, resetAt, found
}

func openCodeRuntimeConfigKey(cfg *config.Config) string {
	if cfg == nil || cfg.OpenCodeGo.IsZero() {
		return ""
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(cfg.OpenCodeGo.Quota.PollInterval))
	b.WriteString("|")
	if cfg.OpenCodeGo.Quota.Threshold != nil {
		b.WriteString(strconv.FormatFloat(*cfg.OpenCodeGo.Quota.Threshold, 'g', -1, 64))
	}
	for groupIndex := range cfg.OpenCodeGo.KeyGroups {
		group := &cfg.OpenCodeGo.KeyGroups[groupIndex]
		b.WriteString("|g:")
		b.WriteString(strings.TrimSpace(group.NamePrefix))
		b.WriteString(":")
		b.WriteString(strconv.FormatBool(group.Disabled))
		b.WriteString(":")
		b.WriteString(strconv.FormatBool(group.DisableCooling))
		appendOpenCodeRuntimeProtocolKey(&b, "openai", group.OpenAI)
		appendOpenCodeRuntimeProtocolKey(&b, "anthropic", group.Anthropic)
		for keyIndex := range group.Keys {
			key := &group.Keys[keyIndex]
			b.WriteString("|k:")
			b.WriteString(strings.TrimSpace(key.KeyName))
			b.WriteString(":")
			b.WriteString(hashOpenCodeRuntimeSecret(key.APIKey))
			b.WriteString(":")
			b.WriteString(strings.TrimSpace(key.ProxyURL))
			b.WriteString(":")
			b.WriteString(strings.TrimSpace(key.WorkspaceID))
			b.WriteString(":")
			b.WriteString(hashOpenCodeRuntimeSecret(key.AuthCookie))
		}
	}
	return b.String()
}

func appendOpenCodeRuntimeProtocolKey(b *strings.Builder, name string, protocol *config.OpenCodeGoProtocolConfig) {
	if b == nil {
		return
	}
	b.WriteString("|p:")
	b.WriteString(name)
	if protocol == nil {
		return
	}
	b.WriteString(":")
	b.WriteString(strings.TrimSpace(protocol.NameSuffix))
	b.WriteString(":")
	b.WriteString(strings.TrimSpace(protocol.BaseURL))
	b.WriteString(":")
	b.WriteString(strings.TrimSpace(protocol.Prefix))
	b.WriteString(":")
	b.WriteString(strconv.Itoa(protocol.Priority))
	for modelIndex := range protocol.Models {
		model := protocol.Models[modelIndex]
		b.WriteString(":m=")
		b.WriteString(strings.TrimSpace(model.Name))
		b.WriteString(">")
		b.WriteString(strings.TrimSpace(model.Alias))
	}
}

func hashOpenCodeRuntimeSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func isOpenCodeGoProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), openCodeGoProviderKey)
}

func normalizeServiceOpenCodeGoProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return "openai"
	case "anthropic", "claude":
		return "anthropic"
	default:
		return ""
	}
}

func (s *Service) resolveConfigOpenCodeGoProtocol(auth *coreauth.Auth) *config.OpenCodeGoProtocolConfig {
	if s == nil || s.cfg == nil || auth == nil || !isOpenCodeGoProvider(auth.Provider) || auth.Attributes == nil {
		return nil
	}
	protocol := normalizeServiceOpenCodeGoProtocol(auth.Attributes["protocol"])
	namePrefix := strings.TrimSpace(auth.Attributes["name_prefix"])
	keyName := strings.TrimSpace(auth.Attributes["key_name"])
	nameSuffix := strings.TrimSpace(auth.Attributes["name_suffix"])
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	apiKey := strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey])
	for groupIndex := range s.cfg.OpenCodeGo.KeyGroups {
		group := &s.cfg.OpenCodeGo.KeyGroups[groupIndex]
		if group.Disabled || (namePrefix != "" && !strings.EqualFold(strings.TrimSpace(group.NamePrefix), namePrefix)) {
			continue
		}
		if !openCodeGoAuthMatchesGroupKey(group, keyName, apiKey) {
			continue
		}
		candidate := openCodeGoProtocolFromGroup(group, protocol)
		if candidate == nil {
			continue
		}
		if nameSuffix != "" && !strings.EqualFold(strings.TrimSpace(candidate.NameSuffix), nameSuffix) {
			continue
		}
		if baseURL != "" && !strings.EqualFold(strings.TrimSpace(candidate.BaseURL), baseURL) {
			continue
		}
		return candidate
	}
	return nil
}

func (s *Service) resolveConfigOpenCodeGoGroup(auth *coreauth.Auth) *config.OpenCodeGoKeyGroup {
	if s == nil || s.cfg == nil || auth == nil || !isOpenCodeGoProvider(auth.Provider) || auth.Attributes == nil {
		return nil
	}
	namePrefix := strings.TrimSpace(auth.Attributes["name_prefix"])
	keyName := strings.TrimSpace(auth.Attributes["key_name"])
	apiKey := strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey])
	for groupIndex := range s.cfg.OpenCodeGo.KeyGroups {
		group := &s.cfg.OpenCodeGo.KeyGroups[groupIndex]
		if group.Disabled || (namePrefix != "" && !strings.EqualFold(strings.TrimSpace(group.NamePrefix), namePrefix)) {
			continue
		}
		if openCodeGoAuthMatchesGroupKey(group, keyName, apiKey) {
			return group
		}
	}
	return nil
}

func (s *Service) buildOpenCodeGoConfigModelsForAuth(auth *coreauth.Auth, excluded []string) []*ModelInfo {
	group := s.resolveConfigOpenCodeGoGroup(auth)
	if group == nil {
		return nil
	}
	forcePrefix := s.cfg != nil && s.cfg.ForceModelPrefix
	protocols := []*config.OpenCodeGoProtocolConfig{group.OpenAI, group.Anthropic}
	out := make([]*ModelInfo, 0)
	seen := make(map[string]struct{})
	for _, protocol := range protocols {
		models := applyExcludedModels(buildOpenCodeGoConfigModels(protocol), excluded)
		models = applyModelPrefixes(models, strings.TrimSpace(protocolPrefix(protocol)), forcePrefix)
		for _, model := range models {
			if model == nil {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(model.ID))
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
		}
	}
	return out
}

func protocolPrefix(protocol *config.OpenCodeGoProtocolConfig) string {
	if protocol == nil {
		return ""
	}
	return protocol.Prefix
}

func openCodeGoAuthMatchesGroupKey(group *config.OpenCodeGoKeyGroup, keyName, apiKey string) bool {
	if group == nil {
		return false
	}
	for keyIndex := range group.Keys {
		key := &group.Keys[keyIndex]
		if keyName != "" && !strings.EqualFold(strings.TrimSpace(key.KeyName), keyName) {
			continue
		}
		if apiKey != "" && !strings.EqualFold(strings.TrimSpace(key.APIKey), apiKey) {
			continue
		}
		return true
	}
	return false
}

func openCodeGoProtocolFromGroup(group *config.OpenCodeGoKeyGroup, protocol string) *config.OpenCodeGoProtocolConfig {
	if group == nil {
		return nil
	}
	switch normalizeServiceOpenCodeGoProtocol(protocol) {
	case "openai":
		return group.OpenAI
	case "anthropic":
		return group.Anthropic
	default:
		return nil
	}
}

func buildOpenCodeGoConfigModels(protocol *config.OpenCodeGoProtocolConfig) []*ModelInfo {
	if protocol == nil || len(protocol.Models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	out := make([]*ModelInfo, 0, len(protocol.Models))
	seen := make(map[string]struct{}, len(protocol.Models))
	for _, model := range protocol.Models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if alias == "" {
			alias = name
		}
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		info := &ModelInfo{
			ID:          alias,
			Object:      "model",
			Created:     now,
			OwnedBy:     openCodeGoProviderKey,
			Type:        openCodeGoProviderKey,
			DisplayName: alias,
			UserDefined: true,
		}
		if resolved := modelconfig.ResolveModelInfo(name, openCodeGoProviderKey, nil); resolved != nil && resolved.Thinking != nil {
			info.Thinking = resolved.Thinking
		}
		out = append(out, info)
	}
	return out
}
