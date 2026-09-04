package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestManagerLifecycleAdvancesQuotaGeneration(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "lifecycle-generation"

	beforeRegister := issueLifecycleQuotaTicket(t, manager, authID)
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	afterRegister := issueLifecycleQuotaTicket(t, manager, authID)
	assertLaterLifecycleQuotaTicket(t, afterRegister, beforeRegister)

	if _, errUpdate := manager.Update(ctx, &Auth{ID: authID, Provider: "gemini"}); errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	afterUpdate := issueLifecycleQuotaTicket(t, manager, authID)
	assertLaterLifecycleQuotaTicket(t, afterUpdate, afterRegister)

	manager.Remove(ctx, authID)
	afterRemove := issueLifecycleQuotaTicket(t, manager, authID)
	assertLaterLifecycleQuotaTicket(t, afterRemove, afterUpdate)
}

func TestManagerLifecycleQuotaGenerationSurvivesReRegister(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "lifecycle-reregister"

	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	beforeRemove := issueLifecycleQuotaTicket(t, manager, authID)
	manager.Remove(ctx, authID)
	tombstone := issueLifecycleQuotaTicket(t, manager, authID)
	assertLaterLifecycleQuotaTicket(t, tombstone, beforeRemove)

	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "gemini"}); errRegister != nil {
		t.Fatalf("re-register error = %v", errRegister)
	}
	afterReRegister := issueLifecycleQuotaTicket(t, manager, authID)
	assertLaterLifecycleQuotaTicket(t, afterReRegister, tombstone)
}

func TestManagerLifecycleQuotaRemoveUsesExactIDBeforeTrimmedFallback(t *testing.T) {
	ctx := context.Background()

	t.Run("trimmed fallback", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		resolved, errRegister := manager.Register(ctx, &Auth{
			ID: "remove-canonical", Provider: "opencode-go", Status: StatusActive,
		})
		if errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}

		manager.Remove(ctx, "  remove-canonical  ")
		if _, ok := manager.GetByID("remove-canonical"); ok {
			t.Fatal("Remove() padded fallback retained canonical auth")
		}
		if ticket, accepted := manager.IssueQuotaObservationTicketForAuth(resolved); accepted {
			t.Fatalf("removed canonical snapshot received ticket: %+v", ticket)
		}
	})

	t.Run("exact key first", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		canonical, errCanonical := manager.Register(ctx, &Auth{
			ID: "remove-twin", Provider: "opencode-go", Status: StatusActive,
		})
		if errCanonical != nil {
			t.Fatalf("Register(canonical) error = %v", errCanonical)
		}
		paddedID := "  remove-twin  "
		padded, errPadded := manager.Register(ctx, &Auth{
			ID: paddedID, Provider: "opencode-go", Status: StatusActive,
		})
		if errPadded != nil {
			t.Fatalf("Register(padded) error = %v", errPadded)
		}

		manager.Remove(ctx, paddedID)
		if _, ok := manager.GetByID(paddedID); ok {
			t.Fatal("Remove() retained exact padded auth")
		}
		if _, ok := manager.GetByID("remove-twin"); !ok {
			t.Fatal("Remove() exact padded key deleted canonical twin")
		}
		if ticket, accepted := manager.IssueQuotaObservationTicketForAuth(padded); accepted {
			t.Fatalf("removed padded snapshot received ticket: %+v", ticket)
		}
		if ticket, accepted := manager.IssueQuotaObservationTicketForAuth(canonical); !accepted {
			t.Fatalf("canonical twin snapshot rejected after exact remove: %+v", ticket)
		}
	})
}

func TestManagerSameIDReplacementPublishesInLifecycleQuotaCommitOrder(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "same-id-replacement"
	before := issueLifecycleQuotaTicket(t, manager, authID)

	const replacements = 64
	var wg sync.WaitGroup
	wg.Add(replacements)
	for i := range replacements {
		go func() {
			defer wg.Done()
			provider := fmt.Sprintf("provider-%d", i)
			_, errRegister := manager.Register(ctx, &Auth{
				ID:       authID,
				Provider: provider,
				Metadata: map[string]any{"replacement": provider},
			})
			if errRegister != nil {
				t.Errorf("Register() error = %v", errRegister)
			}
		}()
	}
	wg.Wait()

	after := issueLifecycleQuotaTicket(t, manager, authID)
	if want := before.Generation + replacements; after.Generation != want {
		t.Fatalf("generation = %d, want %d", after.Generation, want)
	}
	if after.StartOrder <= before.StartOrder {
		t.Fatalf("start order = %d, want greater than %d", after.StartOrder, before.StartOrder)
	}
	managerAuth, ok := manager.GetByID(authID)
	if !ok || managerAuth == nil {
		t.Fatal("manager auth missing after replacements")
	}
	schedulerAuth := schedulerAuthSnapshot(t, manager, authID)
	if schedulerAuth.Provider != managerAuth.Provider {
		t.Fatalf("scheduler provider = %q, manager provider = %q", schedulerAuth.Provider, managerAuth.Provider)
	}
	if schedulerAuth.Metadata["replacement"] != managerAuth.Metadata["replacement"] {
		t.Fatalf("scheduler replacement = %v, manager replacement = %v", schedulerAuth.Metadata["replacement"], managerAuth.Metadata["replacement"])
	}
}

func TestManagerLoadSeedsLifecycleQuotaGeneration(t *testing.T) {
	authID := "loaded-generation"
	store := &lifecycleLockCheckingStore{items: []*Auth{{ID: authID, Provider: "claude"}}}
	manager := NewManager(store, nil, nil)
	store.manager = manager
	before := issueLifecycleQuotaTicket(t, manager, authID)

	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	after := issueLifecycleQuotaTicket(t, manager, authID)
	assertLaterLifecycleQuotaTicket(t, after, before)
	loaded, ok := manager.GetByID(authID)
	if !ok || loaded == nil {
		t.Fatal("loaded auth missing")
	}
	if loaded.quotaGeneration != after.Generation {
		t.Fatalf("loaded auth generation = %d, want %d", loaded.quotaGeneration, after.Generation)
	}
	boundTicket, ok := manager.IssueQuotaObservationTicketForAuth(loaded)
	if !ok || boundTicket.Generation != after.Generation {
		t.Fatalf("IssueQuotaObservationTicketForAuth() = %+v, %v; want generation %d", boundTicket, ok, after.Generation)
	}
	if store.listObservedCommitLock.Load() {
		t.Fatal("Load() held the per-auth commit lock during store.List()")
	}
	schedulerAuth := schedulerAuthSnapshot(t, manager, authID)
	if schedulerAuth.Provider != "claude" {
		t.Fatalf("scheduler provider = %q, want claude", schedulerAuth.Provider)
	}
}

func TestAuthCloneCarriesQuotaGenerationWithoutSerialization(t *testing.T) {
	auth := &Auth{ID: "clone-generation", Provider: "claude", quotaGeneration: 42}
	clone := auth.Clone()
	if clone == nil || clone.quotaGeneration != auth.quotaGeneration {
		t.Fatalf("Clone() generation = %d, want %d", clone.quotaGeneration, auth.quotaGeneration)
	}

	payload, errMarshal := json.Marshal(auth)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	var decoded Auth
	if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if decoded.quotaGeneration != 0 {
		t.Fatalf("decoded generation = %d, want process-local zero value", decoded.quotaGeneration)
	}
}

func TestManagerLifecycleQuotaPersistenceRunsOutsideCommitLock(t *testing.T) {
	authID := "lifecycle-persistence-lock"
	store := &lifecycleLockCheckingStore{}
	manager := NewManager(store, nil, nil)
	store.manager = manager
	cooldownStore := &lifecycleCooldownLockCheckingStore{manager: manager, authID: authID}
	manager.SetCooldownStateStore(cooldownStore)
	ctx := context.Background()

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "claude",
		Metadata: map[string]any{"persist": true},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if _, errUpdate := manager.Update(ctx, &Auth{
		ID:       authID,
		Provider: "gemini",
		Metadata: map[string]any{"persist": true},
	}); errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	manager.Remove(ctx, authID)

	if got := store.saveCount.Load(); got != 2 {
		t.Fatalf("store.Save() count = %d, want 2", got)
	}
	if store.saveObservedCommitLock.Load() {
		t.Fatal("auth persistence observed the per-auth commit lock held")
	}
	if cooldownStore.saveObservedCommitLock.Load() {
		t.Fatal("cooldown persistence observed the per-auth commit lock held")
	}
}

func issueLifecycleQuotaTicket(t *testing.T, manager *Manager, authID string) QuotaObservationTicket {
	t.Helper()
	ticket, ok := manager.IssueQuotaObservationTicket(authID, "test-provider")
	if !ok {
		t.Fatalf("IssueQuotaObservationTicket(%q) rejected valid identity", authID)
	}
	return ticket
}

func assertLaterLifecycleQuotaTicket(t *testing.T, got, previous QuotaObservationTicket) {
	t.Helper()
	if got.Generation <= previous.Generation {
		t.Fatalf("generation = %d, want greater than %d", got.Generation, previous.Generation)
	}
	if got.StartOrder <= previous.StartOrder {
		t.Fatalf("start order = %d, want greater than %d", got.StartOrder, previous.StartOrder)
	}
}

func schedulerAuthSnapshot(t *testing.T, manager *Manager, authID string) *Auth {
	t.Helper()
	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()
	provider := manager.scheduler.authProviders[authID]
	providerState := manager.scheduler.providers[provider]
	if providerState == nil || providerState.auths[authID] == nil || providerState.auths[authID].auth == nil {
		t.Fatalf("scheduler auth %q missing", authID)
	}
	return providerState.auths[authID].auth.Clone()
}

type lifecycleLockCheckingStore struct {
	manager                *Manager
	items                  []*Auth
	saveCount              atomic.Int32
	listObservedCommitLock atomic.Bool
	saveObservedCommitLock atomic.Bool
}

func (s *lifecycleLockCheckingStore) List(context.Context) ([]*Auth, error) {
	if s.manager != nil {
		for _, auth := range s.items {
			if auth == nil {
				continue
			}
			if !tryLifecycleCommitLock(s.manager, auth.ID) {
				s.listObservedCommitLock.Store(true)
			}
		}
	}
	items := make([]*Auth, 0, len(s.items))
	for _, auth := range s.items {
		items = append(items, auth.Clone())
	}
	return items, nil
}

func (s *lifecycleLockCheckingStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saveCount.Add(1)
	if s.manager != nil && auth != nil && !tryLifecycleCommitLock(s.manager, auth.ID) {
		s.saveObservedCommitLock.Store(true)
	}
	return auth.ID, nil
}

func (s *lifecycleLockCheckingStore) Delete(context.Context, string) error {
	return nil
}

type lifecycleCooldownLockCheckingStore struct {
	manager                *Manager
	authID                 string
	saveObservedCommitLock atomic.Bool
}

func (*lifecycleCooldownLockCheckingStore) Load(context.Context) ([]CooldownStateRecord, error) {
	return nil, nil
}

func (s *lifecycleCooldownLockCheckingStore) Save(context.Context, []CooldownStateRecord) error {
	if !tryLifecycleCommitLock(s.manager, s.authID) {
		s.saveObservedCommitLock.Store(true)
	}
	return nil
}

func tryLifecycleCommitLock(manager *Manager, authID string) bool {
	state := manager.quotaCommitState(authID)
	if !state.commitMu.TryLock() {
		return false
	}
	state.commitMu.Unlock()
	return true
}
