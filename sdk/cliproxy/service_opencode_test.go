package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterAvailableExecutors_OpenCodeGoBaseline(t *testing.T) {
	service := &Service{
		cfg:         openCodeGoServiceTestConfig(),
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	service.registerAvailableExecutors(context.Background(), executorRegistrationOptions{includeBaseline: true})

	resolved, ok := service.coreManager.Executor(openCodeGoProviderKey)
	if !ok {
		t.Fatal("expected OpenCode Go executor after baseline registration")
	}
	if _, ok := resolved.(*runtimeexecutor.OpenCodeGoExecutor); !ok {
		t.Fatalf("executor type = %T, want *executor.OpenCodeGoExecutor", resolved)
	}
}

func TestRegisterModelsForAuth_OpenCodeGoRegistersBothProtocolModelSetsOnOneKey(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	service := &Service{cfg: cfg}
	auths := synthesizeOpenCodeGoServiceTestAuths(t, cfg)
	auth := findOpenCodeGoServiceTestAuth(t, auths)
	auth.Attributes["excluded_models"] = "gpt-hidden"

	modelRegistry := internalregistry.GetGlobalRegistry()
	for _, auth := range auths {
		modelRegistry.UnregisterClient(auth.ID)
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			modelRegistry.UnregisterClient(auth.ID)
		}
	})

	service.registerModelsForAuth(context.Background(), auth)
	models := modelRegistry.GetModelsForClient(auth.ID)
	if !hasOpenCodeGoModel(models, "gpt-visible") || !hasOpenCodeGoModel(models, "og/gpt-visible") {
		t.Fatalf("OpenAI protocol models = %v, want alias with configured prefix", openCodeGoModelIDs(models))
	}
	if !hasOpenCodeGoModel(models, "gpt-canonical") || !hasOpenCodeGoModel(models, "og/gpt-canonical") {
		t.Fatalf("OpenAI protocol models = %v, want canonical model with configured prefix", openCodeGoModelIDs(models))
	}
	if hasOpenCodeGoModel(models, "gpt-hidden") || hasOpenCodeGoModel(models, "og/gpt-hidden") {
		t.Fatalf("excluded OpenCode Go model was registered: %v", openCodeGoModelIDs(models))
	}
	if !hasOpenCodeGoModel(models, "claude-visible") || !hasOpenCodeGoModel(models, "oc/claude-visible") {
		t.Fatalf("Anthropic protocol models = %v, want alias with configured prefix on the same auth", openCodeGoModelIDs(models))
	}
}

func TestRegisterConfigAPIKeyAuths_OpenCodeGoRemovesStaleConfigAuths(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	for _, legacyID := range []string{"opencode-go:openai:legacy", "opencode-go:anthropic:legacy"} {
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID:       legacyID,
			Provider: openCodeGoProviderKey,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				coreauth.AttributeSourceBackend: coreauth.AuthSourceConfig,
				coreauth.AttributeAuthKind:      coreauth.AuthKindAPIKey,
				coreauth.AttributeRuntimeOnly:   "true",
				coreauth.AttributeAPIKey:        "legacy-secret",
			},
		}); errRegister != nil {
			t.Fatalf("register legacy auth %q: %v", legacyID, errRegister)
		}
	}

	service.registerConfigAPIKeyAuths(context.Background(), cfg)
	auths := manager.List()
	openCodeGoCount := 0
	var firstID string
	for _, auth := range auths {
		if auth != nil && isOpenCodeGoProvider(auth.Provider) {
			openCodeGoCount++
			firstID = auth.ID
		}
	}
	if openCodeGoCount != 1 {
		t.Fatalf("OpenCode Go auth count = %d, want 1 per key", openCodeGoCount)
	}
	for _, legacyID := range []string{"opencode-go:openai:legacy", "opencode-go:anthropic:legacy"} {
		if legacy, ok := manager.GetByID(legacyID); ok || legacy != nil {
			t.Fatalf("legacy protocol auth survived canonical registration: %+v", legacy)
		}
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	if firstID != "" {
		modelRegistry.RegisterClient(firstID, openCodeGoProviderKey, []*internalregistry.ModelInfo{{ID: "stale-model"}})
	}
	service.registerConfigAPIKeyAuths(context.Background(), &config.Config{})
	for _, auth := range manager.List() {
		if auth != nil && isOpenCodeGoProvider(auth.Provider) {
			t.Fatalf("stale OpenCode Go auth survived cleanup: %#v", auth)
		}
	}
	if firstID != "" {
		if models := modelRegistry.GetModelsForClient(firstID); len(models) != 0 {
			t.Fatalf("stale OpenCode Go models survived cleanup: %v", openCodeGoModelIDs(models))
		}
	}
}

func TestServiceOpenCodeQuotaScheduledAndManualHandoffSharesWorkspaceFetch(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	cfg.OpenCodeGo.Quota.PollInterval = "1h"
	manager := coreauth.NewManager(nil, nil, nil)
	auths := registerOpenCodeGoServiceTestAuths(t, manager, cfg)
	auth := findOpenCodeGoServiceTestAuth(t, auths)

	var fetchCount atomic.Int64
	var rollingUsage atomic.Int64
	rollingUsage.Store(60)
	runtime := &serviceOpenCodeRuntime{fetchDashboard: func(context.Context, quota.DashboardFetchInput) (string, error) {
		fetchCount.Add(1)
		return openCodeGoQuotaDashboard(float64(rollingUsage.Load()), 30, 20), nil
	}}
	if !runtime.syncConfig(context.Background(), cfg, manager) {
		t.Fatal("syncConfig() returned false")
	}
	t.Cleanup(func() { _ = runtime.stop(context.Background()) })

	waitForOpenCodeQuotaScore(t, manager, auth.ID, 40)
	if got := fetchCount.Load(); got != 1 {
		t.Fatalf("scheduled shared-workspace fetches = %d, want 1", got)
	}

	rollingUsage.Store(80)
	result, available, errRefresh := runtime.RefreshOpenCodeGoQuota(context.Background(), auth.ID)
	if errRefresh != nil || !available || result == nil || result.Quota == nil {
		t.Fatalf("manual refresh = result %+v available %v error %v", result, available, errRefresh)
	}
	waitForOpenCodeQuotaScore(t, manager, auth.ID, 20)
	if got := fetchCount.Load(); got != 2 {
		t.Fatalf("scheduled plus manual shared-workspace fetches = %d, want 2", got)
	}
}

func TestServiceApplyConfigRuntimeRegistersCanonicalKeyBeforeQuotaStart(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	cfg.OpenCodeGo.Quota.PollInterval = "1h"
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	observedAuthCount := -1
	service.openCodeRuntime.setTestLifecycleEvent(func(event string, _ *coreauth.Manager, generation uint64) {
		if event != "replace_state_published" || generation != 1 {
			return
		}
		observedAuthCount = 0
		for _, auth := range manager.List() {
			if auth != nil && isOpenCodeGoProvider(auth.Provider) {
				observedAuthCount++
			}
		}
	})
	service.openCodeRuntime.fetchDashboard = func(context.Context, quota.DashboardFetchInput) (string, error) {
		return openCodeGoQuotaDashboard(20, 20, 20), nil
	}

	commit := service.commitConfigUpdate(cfg)
	if !service.applyConfigRuntime(context.Background(), commit, true) {
		t.Fatal("applyConfigRuntime() returned false")
	}
	t.Cleanup(func() {
		service.openCodeRuntime.setTestLifecycleEvent(nil)
		_ = service.stopOpenCodeRuntime(context.Background())
	})
	if observedAuthCount != 1 {
		t.Fatalf("OpenCode Go auth count when quota runtime started = %d, want 1 canonical key", observedAuthCount)
	}
	if entries := buildOpenCodeRuntimePollerEntries(cfg); len(entries) != 1 {
		t.Fatalf("quota poller entries = %d, want 1 per key", len(entries))
	}
}

func TestBuildOpenCodeRuntimePollerEntries_TenKeysRemainTenEntries(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	keys := make([]config.OpenCodeGoKeyEntry, 0, 10)
	for i := 0; i < 10; i++ {
		keys = append(keys, config.OpenCodeGoKeyEntry{
			KeyName:     fmt.Sprintf("key-%02d", i),
			APIKey:      fmt.Sprintf("secret-%02d", i),
			WorkspaceID: fmt.Sprintf("workspace-%02d", i),
			AuthCookie:  fmt.Sprintf("cookie-%02d", i),
		})
	}
	cfg.OpenCodeGo.KeyGroups[0].Keys = keys

	entries := buildOpenCodeRuntimePollerEntries(cfg)
	if len(entries) != len(keys) {
		t.Fatalf("quota poller entries = %d, want %d keys rather than one entry per protocol", len(entries), len(keys))
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.Name]; exists {
			t.Fatalf("duplicate quota auth identity %q", entry.Name)
		}
		seen[entry.Name] = struct{}{}
	}
}

func TestServiceOpenCodeQuotaRejectsFetchStartedBeforeRuntime429(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	manager := coreauth.NewManager(nil, nil, nil)
	auths := registerOpenCodeGoServiceTestAuths(t, manager, cfg)
	resolved := findOpenCodeGoServiceTestAuth(t, auths)
	runtime := newPreparedServiceOpenCodeRuntime(manager, false, 0)
	entry := quota.PollerEntry{Name: resolved.ID, WorkspaceID: "workspace-1", AuthCookie: "cookie"}
	completion := runtime.preparePollAttempt(1, quota.PollAttemptSourceScheduled, []quota.PollerEntry{entry})
	if completion == nil {
		t.Fatal("preparePollAttempt() returned nil")
	}

	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   resolved.ID,
		Provider: openCodeGoProviderKey,
		Model:    "oc/claude-visible",
		Success:  false,
		Error:    &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})
	completion(entry, openCodeGoPollResult(resolved.ID, 80))

	if _, ok := manager.QuotaScore(resolved.ID); ok {
		t.Fatal("stale service completion updated quota score")
	}
	updated, _ := manager.GetByID(resolved.ID)
	if updated == nil || !updated.Quota.Exceeded {
		t.Fatalf("runtime quota state = %+v, want retained 429 block", updated)
	}
}

func TestServiceOpenCodeQuotaReverseCompletionUsesLaterAttempt(t *testing.T) {
	t.Run("newer recovery rejects older monthly exhaustion", func(t *testing.T) {
		manager, resolved := newServiceOpenCodeQuotaManager(t, "opencode-service-reverse-recovery")
		entry := quota.PollerEntry{Name: resolved.ID, WorkspaceID: "workspace-1", AuthCookie: "cookie"}
		poller := quota.NewPoller(quota.PollerConfig{Entries: []quota.PollerEntry{entry}})
		runtime := newPreparedServiceOpenCodeRuntime(manager, true, 5)
		runtime.poller = poller
		poller.SkipEntry(entry.Name)
		older := runtime.preparePollAttempt(1, quota.PollAttemptSourceScheduled, []quota.PollerEntry{entry})
		newer := runtime.preparePollAttempt(1, quota.PollAttemptSourceManual, []quota.PollerEntry{entry})

		newer(entry, openCodeGoPollResultWithWindows(resolved.ID, 60, 70, 12))
		older(entry, openCodeGoPollResultWithWindows(resolved.ID, 60, 70, 0))
		if poller.IsSkipped(entry.Name) {
			t.Fatal("stale monthly exhaustion re-skipped recovered workspace")
		}
		if score, ok := manager.QuotaScore(resolved.ID); !ok || score != 12 {
			t.Fatalf("QuotaScore() = %v, %v; want 12, true", score, ok)
		}
	})

	t.Run("newer monthly exhaustion rejects older recovery", func(t *testing.T) {
		manager, resolved := newServiceOpenCodeQuotaManager(t, "opencode-service-reverse-exhaustion")
		entry := quota.PollerEntry{Name: resolved.ID, WorkspaceID: "workspace-1", AuthCookie: "cookie"}
		poller := quota.NewPoller(quota.PollerConfig{Entries: []quota.PollerEntry{entry}})
		runtime := newPreparedServiceOpenCodeRuntime(manager, true, 5)
		runtime.poller = poller
		older := runtime.preparePollAttempt(1, quota.PollAttemptSourceScheduled, []quota.PollerEntry{entry})
		newer := runtime.preparePollAttempt(1, quota.PollAttemptSourceManual, []quota.PollerEntry{entry})

		newer(entry, openCodeGoPollResultWithWindows(resolved.ID, 60, 70, 0))
		older(entry, openCodeGoPollResultWithWindows(resolved.ID, 60, 70, 12))
		if !poller.IsSkipped(entry.Name) {
			t.Fatal("stale recovery unskipped exhausted workspace")
		}
		if score, ok := manager.QuotaScore(resolved.ID); !ok || score != 0 {
			t.Fatalf("QuotaScore() = %v, %v; want 0, true", score, ok)
		}
	})
}

func TestServiceOpenCodeReloadInvalidatesPreparedQuotaCompletion(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	cfg.OpenCodeGo.Quota.PollInterval = "1h"
	manager := coreauth.NewManager(nil, nil, nil)
	auths := registerOpenCodeGoServiceTestAuths(t, manager, cfg)
	resolved := findOpenCodeGoServiceTestAuth(t, auths)
	runtime := newPreparedServiceOpenCodeRuntime(manager, false, 0)
	runtime.configKey = "old-config"
	entry := quota.PollerEntry{Name: resolved.ID, WorkspaceID: "workspace-1", AuthCookie: "cookie"}
	oldCompletion := runtime.preparePollAttempt(1, quota.PollAttemptSourceScheduled, []quota.PollerEntry{entry})
	if oldCompletion == nil {
		t.Fatal("preparePollAttempt() returned nil")
	}

	fetchStarted := make(chan struct{}, 1)
	runtime.fetchDashboard = func(ctx context.Context, _ quota.DashboardFetchInput) (string, error) {
		select {
		case fetchStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	if !runtime.syncConfig(context.Background(), cfg, manager) {
		t.Fatal("reload syncConfig() returned false")
	}
	t.Cleanup(func() { _ = runtime.stop(context.Background()) })
	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("reloaded poller did not start")
	}

	oldCompletion(entry, openCodeGoPollResult(resolved.ID, 55))
	if _, ok := manager.QuotaScore(resolved.ID); ok {
		t.Fatal("old generation completion updated quota score after reload")
	}
}

func TestServiceOpenCodeReloadSerializesManualQuotaCompletion(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	threshold := 5.0
	cfg.OpenCodeGo.Quota.Threshold = &threshold
	cfg.OpenCodeGo.Quota.PollInterval = "1h"
	manager := coreauth.NewManager(nil, nil, nil)
	auths := registerOpenCodeGoServiceTestAuths(t, manager, cfg)
	resolved := findOpenCodeGoServiceTestAuth(t, auths)

	store := &serviceBlockingCooldownStore{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	manager.SetCooldownStateStore(store)
	var scheduledFetches atomic.Int64
	runtime := &serviceOpenCodeRuntime{fetchDashboard: func(ctx context.Context, _ quota.DashboardFetchInput) (string, error) {
		if manual, _ := ctx.Value(serviceManualRefreshContextKey{}).(bool); manual {
			return openCodeGoQuotaDashboard(10, 20, 100), nil
		}
		if scheduledFetches.Add(1) == 1 {
			return openCodeGoQuotaDashboard(10, 20, 20), nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	store.runtime = runtime
	if !runtime.syncConfig(context.Background(), cfg, manager) {
		t.Fatal("syncConfig() returned false")
	}
	t.Cleanup(func() {
		store.releaseCompletion()
		_ = runtime.stop(context.Background())
	})
	waitForOpenCodeQuotaScore(t, manager, resolved.ID, 80)

	manualDone := make(chan error, 1)
	manualExited := make(chan struct{})
	manualCtx := context.WithValue(context.Background(), serviceManualRefreshContextKey{}, true)
	go func() {
		defer close(manualExited)
		result, available, errRefresh := runtime.RefreshOpenCodeGoQuota(manualCtx, resolved.ID)
		if errRefresh != nil {
			manualDone <- errRefresh
			return
		}
		if !available || result == nil || result.Quota == nil {
			manualDone <- fmt.Errorf("manual refresh unavailable or empty: available=%v result=%+v", available, result)
			return
		}
		manualDone <- nil
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("manual completion did not reach cooldown persistence")
	}

	updated := openCodeGoServiceTestConfig()
	updated.OpenCodeGo.Quota.Threshold = &threshold
	updated.OpenCodeGo.Quota.PollInterval = "2h"
	reloadReachedGate := make(chan struct{})
	reloadMayAttemptLock := make(chan struct{})
	var reloadReachedOnce sync.Once
	var reloadMayAttemptOnce sync.Once
	runtime.setTestLifecycleEvent(func(event string, _ *coreauth.Manager, _ uint64) {
		if event != "before_replace_gate" {
			return
		}
		reloadReachedOnce.Do(func() { close(reloadReachedGate) })
		<-reloadMayAttemptLock
	})
	reloadDone := make(chan bool, 1)
	reloadExited := make(chan struct{})
	go func() {
		defer close(reloadExited)
		reloadDone <- runtime.syncConfig(context.Background(), updated, manager)
	}()
	t.Cleanup(func() {
		reloadMayAttemptOnce.Do(func() { close(reloadMayAttemptLock) })
		store.releaseCompletion()
		select {
		case <-manualExited:
		case <-time.After(2 * time.Second):
			t.Error("manual refresh goroutine did not exit during cleanup")
		}
		select {
		case <-reloadExited:
		case <-time.After(2 * time.Second):
			t.Error("reload goroutine did not exit during cleanup")
		}
		runtime.setTestLifecycleEvent(nil)
	})
	select {
	case <-reloadReachedGate:
	case <-time.After(time.Second):
		t.Fatal("reload did not reach the attempt gate")
	}
	if runtime.attemptMu.TryLock() {
		runtime.attemptMu.Unlock()
		t.Fatal("attempt gate was not held by the applying manual completion")
	}
	runtime.mu.Lock()
	generationWhileBlocked := runtime.generation
	runtime.mu.Unlock()
	if generationWhileBlocked != 1 {
		t.Fatalf("generation while completion blocked = %d, want 1", generationWhileBlocked)
	}

	reloadMayAttemptOnce.Do(func() { close(reloadMayAttemptLock) })
	store.releaseCompletion()
	select {
	case errManual := <-manualDone:
		if errManual != nil {
			t.Fatalf("manual refresh error = %v", errManual)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual refresh did not finish")
	}
	select {
	case reloaded := <-reloadDone:
		if !reloaded {
			t.Fatal("reload returned false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.generations) == 0 {
		t.Fatal("manual completion did not persist cooldown state")
	}
	for _, generation := range store.generations {
		if generation != 1 {
			t.Fatalf("old manual completion persisted in generation %d, want 1", generation)
		}
	}
}

func TestServiceOpenCodeQuotaCallbackLifecycleBranches(t *testing.T) {
	newRuntime := func(t *testing.T, manager *coreauth.Manager, cfg *config.Config) *serviceOpenCodeRuntime {
		t.Helper()
		registerOpenCodeGoServiceTestAuths(t, manager, cfg)
		runtime := &serviceOpenCodeRuntime{fetchDashboard: func(context.Context, quota.DashboardFetchInput) (string, error) {
			return openCodeGoQuotaDashboard(10, 20, 20), nil
		}}
		if !runtime.syncConfig(context.Background(), cfg, manager) {
			t.Fatal("syncConfig() returned false")
		}
		return runtime
	}

	t.Run("disable clears current manager callback", func(t *testing.T) {
		cfg := openCodeGoServiceTestConfig()
		cfg.OpenCodeGo.Quota.PollInterval = "1h"
		manager := coreauth.NewManager(nil, nil, nil)
		runtime := newRuntime(t, manager, cfg)
		var events []serviceCallbackLifecycleEvent
		runtime.setTestLifecycleEvent(func(event string, eventManager *coreauth.Manager, generation uint64) {
			if event == "callback_cleared" || event == "callback_installed" {
				events = append(events, serviceCallbackLifecycleEvent{event, eventManager, generation})
			}
		})

		if !runtime.syncConfig(context.Background(), &config.Config{}, manager) {
			t.Fatal("disabling syncConfig() returned false")
		}
		runtime.setTestLifecycleEvent(nil)
		if len(events) != 1 || events[0].name != "callback_cleared" || events[0].manager != manager || events[0].generation != 2 {
			t.Fatalf("disable callback events = %+v, want one generation-2 clear", events)
		}
	})

	t.Run("manager replacement installs new then clears old", func(t *testing.T) {
		cfg := openCodeGoServiceTestConfig()
		cfg.OpenCodeGo.Quota.PollInterval = "1h"
		oldManager := coreauth.NewManager(nil, nil, nil)
		newManager := coreauth.NewManager(nil, nil, nil)
		runtime := newRuntime(t, oldManager, cfg)
		registerOpenCodeGoServiceTestAuths(t, newManager, cfg)
		var events []serviceCallbackLifecycleEvent
		runtime.setTestLifecycleEvent(func(event string, eventManager *coreauth.Manager, generation uint64) {
			if event == "callback_cleared" || event == "callback_installed" {
				events = append(events, serviceCallbackLifecycleEvent{event, eventManager, generation})
			}
		})

		if !runtime.syncConfig(context.Background(), cfg, newManager) {
			t.Fatal("manager replacement syncConfig() returned false")
		}
		runtime.setTestLifecycleEvent(nil)
		t.Cleanup(func() { _ = runtime.stop(context.Background()) })
		if len(events) != 2 ||
			events[0].name != "callback_installed" || events[0].manager != newManager || events[0].generation != 2 ||
			events[1].name != "callback_cleared" || events[1].manager != oldManager || events[1].generation != 2 {
			t.Fatalf("manager replacement callback events = %+v, want new install then old clear", events)
		}
	})
}

func TestServiceOpenCodeReloadAndStopSerializeQuotaCallbackCleanup(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	cfg.OpenCodeGo.Quota.PollInterval = "1h"
	manager := coreauth.NewManager(nil, nil, nil)
	registerOpenCodeGoServiceTestAuths(t, manager, cfg)
	runtime := &serviceOpenCodeRuntime{fetchDashboard: func(context.Context, quota.DashboardFetchInput) (string, error) {
		return openCodeGoQuotaDashboard(10, 20, 20), nil
	}}
	if !runtime.syncConfig(context.Background(), cfg, manager) {
		t.Fatal("syncConfig() returned false")
	}

	reloadPublished := make(chan struct{})
	reloadMayInstall := make(chan struct{})
	stopReachedGate := make(chan struct{})
	var reloadPublishedOnce sync.Once
	var reloadMayInstallOnce sync.Once
	var stopReachedOnce sync.Once
	var eventsMu sync.Mutex
	var events []serviceCallbackLifecycleEvent
	runtime.setTestLifecycleEvent(func(event string, eventManager *coreauth.Manager, generation uint64) {
		switch event {
		case "replace_state_published":
			if generation == 2 {
				reloadPublishedOnce.Do(func() { close(reloadPublished) })
				<-reloadMayInstall
			}
		case "before_stop_gate":
			stopReachedOnce.Do(func() { close(stopReachedGate) })
		case "callback_installed", "callback_cleared":
			eventsMu.Lock()
			events = append(events, serviceCallbackLifecycleEvent{event, eventManager, generation})
			eventsMu.Unlock()
		}
	})
	updated := openCodeGoServiceTestConfig()
	updated.OpenCodeGo.Quota.PollInterval = "2h"
	reloadDone := make(chan bool, 1)
	reloadExited := make(chan struct{})
	go func() {
		defer close(reloadExited)
		reloadDone <- runtime.syncConfig(context.Background(), updated, manager)
	}()
	var stopExited chan struct{}
	t.Cleanup(func() {
		reloadMayInstallOnce.Do(func() { close(reloadMayInstall) })
		select {
		case <-reloadExited:
		case <-time.After(2 * time.Second):
			t.Error("reload goroutine did not exit during cleanup")
		}
		if stopExited != nil {
			select {
			case <-stopExited:
			case <-time.After(2 * time.Second):
				t.Error("stop goroutine did not exit during cleanup")
			}
		}
		runtime.setTestLifecycleEvent(nil)
		_ = runtime.stop(context.Background())
	})
	select {
	case <-reloadPublished:
	case <-time.After(time.Second):
		t.Fatal("reload did not publish its state under the attempt gate")
	}
	stopDone := make(chan error, 1)
	stopExited = make(chan struct{})
	go func() {
		defer close(stopExited)
		stopDone <- runtime.stop(context.Background())
	}()
	select {
	case <-stopReachedGate:
	case <-time.After(time.Second):
		t.Fatal("stop did not reach the attempt gate")
	}

	reloadMayInstallOnce.Do(func() { close(reloadMayInstall) })
	select {
	case reloaded := <-reloadDone:
		if !reloaded {
			t.Fatal("reload returned false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish")
	}
	select {
	case errStop := <-stopDone:
		if errStop != nil {
			t.Fatalf("stop() error = %v", errStop)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish")
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) != 2 ||
		events[0].name != "callback_installed" || events[0].manager != manager || events[0].generation != 2 ||
		events[1].name != "callback_cleared" || events[1].manager != manager || events[1].generation != 3 {
		t.Fatalf("reload/stop callback events = %+v, want generation-2 install then generation-3 clear", events)
	}
}

func TestServiceOpenCodeQuotaThresholdDisabledIsScoreOnly(t *testing.T) {
	manager, resolved := newServiceOpenCodeQuotaManager(t, "opencode-service-score")
	runtime := newPreparedServiceOpenCodeRuntime(manager, false, 0)
	entry := quota.PollerEntry{Name: resolved.ID, WorkspaceID: "workspace-1", AuthCookie: "cookie"}
	completion := runtime.preparePollAttempt(1, quota.PollAttemptSourceManual, []quota.PollerEntry{entry})
	completion(entry, openCodeGoPollResult(resolved.ID, 0))

	if score, ok := manager.QuotaScore(resolved.ID); !ok || score != 0 {
		t.Fatalf("QuotaScore() = %v, %v; want 0, true", score, ok)
	}
	updated, _ := manager.GetByID(resolved.ID)
	if updated == nil || updated.Quota.Exceeded || updated.Unavailable {
		t.Fatalf("threshold-disabled auth = %+v, want score only", updated)
	}
}

func TestServiceOpenCodeQuotaThresholdCrossingAndRecovery(t *testing.T) {
	manager, resolved := newServiceOpenCodeQuotaManager(t, "opencode-service-threshold")
	runtime := newPreparedServiceOpenCodeRuntime(manager, true, 5)
	entry := quota.PollerEntry{Name: resolved.ID, WorkspaceID: "workspace-1", AuthCookie: "cookie"}
	exhausted := runtime.preparePollAttempt(1, quota.PollAttemptSourceScheduled, []quota.PollerEntry{entry})
	exhausted(entry, openCodeGoPollResult(resolved.ID, 5))
	updated, _ := manager.GetByID(resolved.ID)
	if updated == nil || !updated.Quota.Exceeded || updated.Quota.Reason != "quota_hub" {
		t.Fatalf("threshold crossing auth = %+v, want QuotaHub exhaustion", updated)
	}

	recovered := runtime.preparePollAttempt(1, quota.PollAttemptSourceManual, []quota.PollerEntry{entry})
	recovered(entry, openCodeGoPollResult(resolved.ID, 6))
	updated, _ = manager.GetByID(resolved.ID)
	if updated == nil || updated.Quota.Exceeded || updated.Unavailable {
		t.Fatalf("threshold recovery auth = %+v, want available", updated)
	}
}

func TestServiceOpenCodeQuotaMonthlySkipAndUnskip(t *testing.T) {
	manager, resolved := newServiceOpenCodeQuotaManager(t, "auth-b")
	poller := quota.NewPoller(quota.PollerConfig{Entries: []quota.PollerEntry{
		{Name: "auth-a", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"},
		{Name: "auth-b", WorkspaceID: "workspace-1", AuthCookie: "cookie-b"},
	}})
	runtime := &serviceOpenCodeRuntime{
		generation:  1,
		started:     true,
		ctx:         context.Background(),
		poller:      poller,
		manager:     manager,
		threshold:   5,
		thresholdOK: true,
	}
	entry := quota.PollerEntry{Name: resolved.ID, WorkspaceID: "workspace-1", AuthCookie: "cookie-b"}

	exhausted := runtime.preparePollAttempt(1, quota.PollAttemptSourceScheduled, []quota.PollerEntry{entry})
	exhausted(entry, openCodeGoPollResultWithWindows("auth-b", 60, 70, 0))
	if !poller.IsSkipped("auth-a") || !poller.IsSkipped("auth-b") {
		t.Fatal("monthly exhaustion did not skip the deduplicated workspace")
	}
	recovered := runtime.preparePollAttempt(1, quota.PollAttemptSourceManual, []quota.PollerEntry{entry})
	recovered(entry, openCodeGoPollResultWithWindows("auth-b", 60, 70, 12))
	if poller.IsSkipped("auth-a") || poller.IsSkipped("auth-b") {
		t.Fatal("monthly recovery did not unskip the deduplicated workspace")
	}
}

func TestServiceOpenCodeStopDrainsPollerAndClearsQuotaCallback(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	cfg.OpenCodeGo.Quota.PollInterval = "1h"
	manager := coreauth.NewManager(nil, nil, nil)
	registerOpenCodeGoServiceTestAuths(t, manager, cfg)
	started := make(chan struct{}, 1)
	drained := make(chan struct{}, 1)
	runtime := &serviceOpenCodeRuntime{fetchDashboard: func(ctx context.Context, _ quota.DashboardFetchInput) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		drained <- struct{}{}
		return "", ctx.Err()
	}}
	if !runtime.syncConfig(context.Background(), cfg, manager) {
		t.Fatal("syncConfig() returned false")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("immediate poll did not start")
	}
	var callbackEvents []serviceCallbackLifecycleEvent
	runtime.setTestLifecycleEvent(func(event string, eventManager *coreauth.Manager, generation uint64) {
		if event == "callback_cleared" {
			callbackEvents = append(callbackEvents, serviceCallbackLifecycleEvent{event, eventManager, generation})
		}
	})
	errStop := runtime.stop(context.Background())
	runtime.setTestLifecycleEvent(nil)
	if errStop != nil {
		t.Fatalf("stop() error = %v", errStop)
	}
	if len(callbackEvents) != 1 ||
		callbackEvents[0].name != "callback_cleared" || callbackEvents[0].manager != manager || callbackEvents[0].generation != 2 {
		t.Fatalf("stop callback events = %+v, want one generation-2 clear", callbackEvents)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("stop() returned without draining the poller fetch")
	}
}

func TestCooldownRestoreCompletesBeforeOpenCodeRuntimeStart(t *testing.T) {
	cfg := openCodeGoServiceTestConfig()
	cfg.SaveCooldownStatus = true
	cfg.OpenCodeGo.Quota.PollInterval = "1h"
	manager := coreauth.NewManager(nil, nil, nil)
	auths := registerOpenCodeGoServiceTestAuths(t, manager, cfg)
	resolved := findOpenCodeGoServiceTestAuth(t, auths)
	resetAt := time.Now().Add(time.Hour).UTC()
	manager.SetCooldownStateStore(&serviceStartupCooldownStore{records: []coreauth.CooldownStateRecord{{
		Provider:       openCodeGoProviderKey,
		AuthID:         resolved.ID,
		Status:         string(coreauth.StatusError),
		NextRetryAfter: resetAt,
		Reason:         "quota_hub",
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota_hub",
			NextRecoverAt: resetAt,
		},
		UpdatedAt: time.Now().UTC(),
	}}})
	restoredAtFetch := make(chan bool, 1)
	service := &Service{cfg: cfg, coreManager: manager}
	service.openCodeRuntime.fetchDashboard = func(context.Context, quota.DashboardFetchInput) (string, error) {
		updated, _ := manager.GetByID(resolved.ID)
		restoredAtFetch <- updated != nil && updated.Quota.Exceeded && updated.Quota.Reason == "quota_hub"
		return openCodeGoQuotaDashboard(20, 20, 20), nil
	}
	if !service.restoreCooldownAndStartOpenCodeRuntime(context.Background(), cfg) {
		t.Fatal("restoreCooldownAndStartOpenCodeRuntime() returned false")
	}
	t.Cleanup(func() { _ = service.stopOpenCodeRuntime(context.Background()) })
	select {
	case restored := <-restoredAtFetch:
		if !restored {
			t.Fatal("first OpenCode poll started before cooldown restoration")
		}
	case <-time.After(time.Second):
		t.Fatal("first OpenCode poll did not start")
	}
}

type serviceStartupCooldownStore struct {
	mu      sync.Mutex
	records []coreauth.CooldownStateRecord
}

type serviceCallbackLifecycleEvent struct {
	name       string
	manager    *coreauth.Manager
	generation uint64
}

type serviceManualRefreshContextKey struct{}

type serviceBlockingCooldownStore struct {
	mu          sync.Mutex
	blockOnce   sync.Once
	releaseOnce sync.Once
	runtime     *serviceOpenCodeRuntime
	started     chan struct{}
	release     chan struct{}
	generations []uint64
}

func (store *serviceBlockingCooldownStore) releaseCompletion() {
	store.releaseOnce.Do(func() { close(store.release) })
}

func (*serviceBlockingCooldownStore) Load(context.Context) ([]coreauth.CooldownStateRecord, error) {
	return nil, nil
}

func (store *serviceBlockingCooldownStore) Save(context.Context, []coreauth.CooldownStateRecord) error {
	store.runtime.mu.Lock()
	generation := store.runtime.generation
	store.runtime.mu.Unlock()
	store.mu.Lock()
	store.generations = append(store.generations, generation)
	store.mu.Unlock()
	store.blockOnce.Do(func() {
		store.started <- struct{}{}
		<-store.release
	})
	return nil
}

func (store *serviceStartupCooldownStore) Load(context.Context) ([]coreauth.CooldownStateRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]coreauth.CooldownStateRecord(nil), store.records...), nil
}

func (*serviceStartupCooldownStore) Save(context.Context, []coreauth.CooldownStateRecord) error {
	return nil
}

func openCodeGoServiceTestConfig() *config.Config {
	return &config.Config{OpenCodeGo: config.OpenCodeGoConfig{KeyGroups: []config.OpenCodeGoKeyGroup{{
		NamePrefix: "acct",
		OpenAI: &config.OpenCodeGoProtocolConfig{
			NameSuffix: "openai",
			BaseURL:    "https://api.opencode.example/v1",
			Prefix:     "og",
			Models: []config.OpenCodeGoModelEntry{
				{Name: "gpt-upstream", Alias: "gpt-visible"},
				{Name: "gpt-canonical"},
				{Name: "gpt-hidden"},
			},
		},
		Anthropic: &config.OpenCodeGoProtocolConfig{
			NameSuffix: "anthropic",
			BaseURL:    "https://api.opencode.example/anthropic",
			Prefix:     "oc",
			Models: []config.OpenCodeGoModelEntry{
				{Name: "claude-upstream", Alias: "claude-visible"},
			},
		},
		Keys: []config.OpenCodeGoKeyEntry{{
			KeyName:     "primary",
			APIKey:      "opencode-key",
			WorkspaceID: "workspace-1",
			AuthCookie:  "auth-cookie",
		}},
	}}}}
}

func synthesizeOpenCodeGoServiceTestAuths(t *testing.T, cfg *config.Config) []*coreauth.Auth {
	t.Helper()
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         testUnixTime(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	return auths
}

func testUnixTime() time.Time {
	return time.Unix(1704067200, 0)
}

func findOpenCodeGoServiceTestAuth(t *testing.T, auths []*coreauth.Auth) *coreauth.Auth {
	t.Helper()
	for _, auth := range auths {
		if auth == nil || !isOpenCodeGoProvider(auth.Provider) {
			continue
		}
		return auth.Clone()
	}
	t.Fatal("missing OpenCode Go auth")
	return nil
}

func hasOpenCodeGoModel(models []*internalregistry.ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && strings.TrimSpace(model.ID) == id {
			return true
		}
	}
	return false
}

func openCodeGoModelIDs(models []*internalregistry.ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			out = append(out, model.ID)
		}
	}
	return out
}

func registerOpenCodeGoServiceTestAuths(t *testing.T, manager *coreauth.Manager, cfg *config.Config) []*coreauth.Auth {
	t.Helper()
	auths := synthesizeOpenCodeGoServiceTestAuths(t, cfg)
	for _, candidate := range auths {
		if candidate == nil || !isOpenCodeGoProvider(candidate.Provider) {
			continue
		}
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}
	return auths
}

func newServiceOpenCodeQuotaManager(t *testing.T, authID string) (*coreauth.Manager, *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	registered, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: openCodeGoProviderKey,
		Status:   coreauth.StatusActive,
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return manager, registered
}

func newPreparedServiceOpenCodeRuntime(manager *coreauth.Manager, thresholdOK bool, threshold float64) *serviceOpenCodeRuntime {
	return &serviceOpenCodeRuntime{
		generation:  1,
		started:     true,
		ctx:         context.Background(),
		manager:     manager,
		thresholdOK: thresholdOK,
		threshold:   threshold,
	}
}

func openCodeGoPollResult(entryName string, score float64) *quota.PollResult {
	return &quota.PollResult{
		EntryName: entryName,
		Timestamp: time.Now(),
		Quota: &quota.OpenCodeGoQuota{
			Rolling: &quota.OpenCodeGoWindow{PercentRemaining: score, ResetTime: time.Now().Add(time.Hour).UTC()},
		},
	}
}

func openCodeGoPollResultWithWindows(entryName string, rolling, weekly, monthly float64) *quota.PollResult {
	resetAt := time.Now().Add(time.Hour).UTC()
	return &quota.PollResult{
		EntryName: entryName,
		Timestamp: time.Now(),
		Quota: &quota.OpenCodeGoQuota{
			Rolling: &quota.OpenCodeGoWindow{PercentRemaining: rolling, ResetTime: resetAt},
			Weekly:  &quota.OpenCodeGoWindow{PercentRemaining: weekly, ResetTime: resetAt},
			Monthly: &quota.OpenCodeGoWindow{PercentRemaining: monthly, ResetTime: resetAt},
		},
	}
}

func waitForOpenCodeQuotaScore(t *testing.T, manager *coreauth.Manager, authID string, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if score, ok := manager.QuotaScore(authID); ok && score == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	score, ok := manager.QuotaScore(authID)
	t.Fatalf("QuotaScore(%s) = %v, %v; want %v, true", authID, score, ok, want)
}

func openCodeGoQuotaDashboard(rollingUsage, weeklyUsage, monthlyUsage float64) string {
	return fmt.Sprintf(`<section>
		<div data-slot="usage-item">
			<span data-slot="usage-label">Rolling Usage</span>
			<span data-slot="usage-value">%.1f%%</span>
			<span data-slot="reset-time">Resets in 30 minutes</span>
		</div>
		<div data-slot="usage-item">
			<span data-slot="usage-label">Weekly Usage</span>
			<span data-slot="usage-value">%.1f%%</span>
			<span data-slot="reset-time">Resets in 1 day</span>
		</div>
		<div data-slot="usage-item">
			<span data-slot="usage-label">Monthly Usage</span>
			<span data-slot="usage-value">%.1f%%</span>
			<span data-slot="reset-time">Resets in 2 days</span>
		</div>
	</section>`, rollingUsage, weeklyUsage, monthlyUsage)
}
