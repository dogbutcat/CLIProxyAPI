package auth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestManagerQuotaObservationTicketReusesStableAuthState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	firstState := manager.quotaCommitState("shared-auth")
	secondState := manager.quotaCommitState("shared-auth")
	otherState := manager.quotaCommitState("other-auth")
	if firstState != secondState {
		t.Fatal("quotaCommitState() did not reuse state for the same auth ID")
	}
	if firstState == otherState {
		t.Fatal("quotaCommitState() reused state across different auth IDs")
	}

	first, ok := manager.IssueQuotaObservationTicket("shared-auth", "opencode-go")
	if !ok {
		t.Fatal("IssueQuotaObservationTicket() rejected a valid identity")
	}
	second, ok := manager.IssueQuotaObservationTicket("shared-auth", "claude")
	if !ok {
		t.Fatal("IssueQuotaObservationTicket() rejected a second valid identity")
	}
	if first.AuthID != "shared-auth" || first.Provider != "opencode-go" {
		t.Fatalf("first ticket identity = %+v, want shared-auth/opencode-go", first)
	}
	if second.AuthID != "shared-auth" || second.Provider != "claude" {
		t.Fatalf("second ticket identity = %+v, want shared-auth/claude", second)
	}
	if first.Generation != second.Generation || first.Generation == 0 {
		t.Fatalf("ticket generations = %d, %d; want one non-zero generation", first.Generation, second.Generation)
	}
	if first.Revision != 0 || second.Revision != 0 {
		t.Fatalf("ticket revisions = %d, %d; want initial auth-wide revision 0", first.Revision, second.Revision)
	}
	if first.StartOrder != 1 || second.StartOrder != 2 {
		t.Fatalf("ticket start orders = %d, %d; want 1, 2", first.StartOrder, second.StartOrder)
	}
}

func TestManagerQuotaObservationTicketStartOrderIsUniqueUnderConcurrency(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	const ticketCount = 128
	tickets := make(chan QuotaObservationTicket, ticketCount)
	var wg sync.WaitGroup
	wg.Add(ticketCount)
	for range ticketCount {
		go func() {
			defer wg.Done()
			ticket, ok := manager.IssueQuotaObservationTicket("concurrent-auth", "opencode-go")
			if !ok {
				t.Errorf("IssueQuotaObservationTicket() rejected a valid identity")
				return
			}
			tickets <- ticket
		}()
	}
	wg.Wait()
	close(tickets)

	seen := make(map[uint64]bool, ticketCount)
	for ticket := range tickets {
		if ticket.AuthID != "concurrent-auth" || ticket.Provider != "opencode-go" {
			t.Errorf("ticket identity = %+v, want concurrent-auth/opencode-go", ticket)
		}
		if ticket.Generation != 1 || ticket.Revision != 0 {
			t.Errorf("ticket causal state = generation %d revision %d, want 1/0", ticket.Generation, ticket.Revision)
		}
		if seen[ticket.StartOrder] {
			t.Errorf("duplicate start order %d", ticket.StartOrder)
		}
		seen[ticket.StartOrder] = true
	}
	for order := uint64(1); order <= ticketCount; order++ {
		if !seen[order] {
			t.Errorf("missing start order %d", order)
		}
	}
}

func TestManagerQuotaObservationTicketForAuthSnapshotAcceptsCurrent(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	resolved, errRegister := manager.Register(context.Background(), &Auth{
		ID: "snapshot-current", Provider: "opencode-go", Status: StatusActive,
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	ticket, ok := manager.IssueQuotaObservationTicketForAuth(resolved)
	if !ok {
		t.Fatal("IssueQuotaObservationTicketForAuth() rejected current snapshot")
	}
	if ticket.AuthID != resolved.ID || ticket.Provider != resolved.Provider ||
		ticket.Generation != resolved.quotaGeneration || ticket.StartOrder != 1 {
		t.Fatalf("bound ticket = %+v, want current snapshot generation and first order", ticket)
	}
}

func TestManagerQuotaObservationTicketForAuthSnapshotPreservesExactID(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "  snapshot-exact-id  "
	resolved, errRegister := manager.Register(ctx, &Auth{
		ID: authID, Provider: "opencode-go", Status: StatusActive,
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	ticket, ok := manager.IssueQuotaObservationTicketForAuth(resolved)
	if !ok {
		t.Fatal("IssueQuotaObservationTicketForAuth() rejected exact manager ID")
	}
	if ticket.AuthID != authID {
		t.Fatalf("ticket AuthID = %q, want exact %q", ticket.AuthID, authID)
	}
	manager.SetQuotaScore(authID, 23)
	fallbackScore, fallbackOK := manager.QuotaScore(authID)
	if !fallbackOK || fallbackScore != 23 {
		t.Fatalf("QuotaScore(raw ID fallback) = %v, %v; want 23, true", fallbackScore, fallbackOK)
	}
	if !manager.ApplyQuotaObservationBatch(ctx, quotaScoreOnlyBatch(ticket, 61)) {
		t.Fatal("ApplyQuotaObservationBatch() rejected exact-ID bound ticket")
	}
	score, scoreOK := manager.QuotaScore(authID)
	trimmedScore, trimmedScoreOK := manager.QuotaScore(strings.TrimSpace(authID))
	if !scoreOK || score != 61 || !trimmedScoreOK || trimmedScore != 23 {
		t.Fatalf("exact quota score = %v, %v; canonical quota score = %v, %v", score, scoreOK, trimmedScore, trimmedScoreOK)
	}

	manager.Remove(ctx, authID)
	if _, stillPublished := manager.GetByID(authID); stillPublished {
		t.Fatal("Remove() retained exact-ID auth")
	}
	if staleTicket, accepted := manager.IssueQuotaObservationTicketForAuth(resolved); accepted {
		t.Fatalf("IssueQuotaObservationTicketForAuth() accepted removed exact-ID snapshot: %+v", staleTicket)
	}
	reRegistered, errReRegister := manager.Register(ctx, &Auth{
		ID: authID, Provider: "opencode-go", Status: StatusActive,
	})
	if errReRegister != nil {
		t.Fatalf("re-register error = %v", errReRegister)
	}
	currentTicket, currentAccepted := manager.IssueQuotaObservationTicketForAuth(reRegistered)
	if !currentAccepted || currentTicket.AuthID != authID || currentTicket.Generation <= ticket.Generation {
		t.Fatalf("re-registered exact-ID ticket = %+v, %v; want later exact generation", currentTicket, currentAccepted)
	}

	blankID := "   "
	blank, errBlank := manager.Register(ctx, &Auth{ID: blankID, Provider: "opencode-go", Status: StatusActive})
	if errBlank != nil {
		t.Fatalf("Register(blank ID) error = %v", errBlank)
	}
	state := manager.quotaCommitState(blankID)
	state.commitMu.Lock()
	beforeOrder := state.nextStartOrder
	state.commitMu.Unlock()
	if rejected, accepted := manager.IssueQuotaObservationTicketForAuth(blank); accepted {
		t.Fatalf("IssueQuotaObservationTicketForAuth() accepted blank ID: %+v", rejected)
	}
	state.commitMu.Lock()
	afterOrder := state.nextStartOrder
	state.commitMu.Unlock()
	if afterOrder != beforeOrder {
		t.Fatalf("blank-ID rejection advanced start order from %d to %d", beforeOrder, afterOrder)
	}
}

func TestManagerQuotaObservationTicketForAuthSnapshotRejectsStaleLifecycle(t *testing.T) {
	tests := []struct {
		name    string
		replace func(context.Context, *Manager, string) error
	}{
		{
			name: "update",
			replace: func(ctx context.Context, manager *Manager, authID string) error {
				_, errUpdate := manager.Update(ctx, &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive})
				return errUpdate
			},
		},
		{
			name: "same ID replacement",
			replace: func(ctx context.Context, manager *Manager, authID string) error {
				_, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive})
				return errRegister
			},
		},
		{
			name: "remove and re-register",
			replace: func(ctx context.Context, manager *Manager, authID string) error {
				manager.Remove(ctx, authID)
				_, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive})
				return errRegister
			},
		},
		{
			name: "provider change",
			replace: func(ctx context.Context, manager *Manager, authID string) error {
				_, errUpdate := manager.Update(ctx, &Auth{ID: authID, Provider: "claude", Status: StatusActive})
				return errUpdate
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			ctx := context.Background()
			authID := "snapshot-stale-" + tt.name
			oldSnapshot, errRegister := manager.Register(ctx, &Auth{
				ID: authID, Provider: "opencode-go", Status: StatusActive,
			})
			if errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			if errReplace := tt.replace(ctx, manager, authID); errReplace != nil {
				t.Fatalf("replacement error = %v", errReplace)
			}

			assertSnapshotTicketRejectionConsumesNoOrder(t, manager, oldSnapshot)
		})
	}
}

func TestManagerQuotaObservationTicketForAuthSnapshotRejectsInvalidIdentityAndDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	current, errRegister := manager.Register(ctx, &Auth{
		ID: "snapshot-validation", Provider: "opencode-go", Status: StatusActive,
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	tests := []struct {
		name     string
		resolved *Auth
	}{
		{name: "nil", resolved: nil},
		{name: "empty ID", resolved: &Auth{Provider: current.Provider, quotaGeneration: current.quotaGeneration}},
		{name: "empty provider", resolved: &Auth{ID: current.ID, quotaGeneration: current.quotaGeneration}},
		{name: "zero generation", resolved: &Auth{ID: current.ID, Provider: current.Provider}},
		{name: "provider mismatch", resolved: &Auth{ID: current.ID, Provider: "claude", quotaGeneration: current.quotaGeneration}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSnapshotTicketRejectionConsumesNoOrder(t, manager, tt.resolved)
		})
	}

	for _, disabled := range []*Auth{
		{ID: "snapshot-disabled-flag", Provider: "opencode-go", Status: StatusActive, Disabled: true},
		{ID: "snapshot-disabled-status", Provider: "opencode-go", Status: StatusDisabled},
	} {
		resolved, errDisabled := manager.Register(ctx, disabled)
		if errDisabled != nil {
			t.Fatalf("Register(%q) error = %v", disabled.ID, errDisabled)
		}
		assertSnapshotTicketRejectionConsumesNoOrder(t, manager, resolved)
	}
}

func TestManagerQuotaObservationTicketForAuthSnapshotUpdateRace(t *testing.T) {
	for iteration := range 64 {
		manager := NewManager(nil, nil, nil)
		ctx := context.Background()
		authID := fmt.Sprintf("snapshot-update-race-%d", iteration)
		oldSnapshot, errRegister := manager.Register(ctx, &Auth{
			ID: authID, Provider: "opencode-go", Status: StatusActive,
		})
		if errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}

		start := make(chan struct{})
		issued := make(chan struct {
			ticket QuotaObservationTicket
			ok     bool
		}, 1)
		updated := make(chan error, 1)
		go func() {
			<-start
			ticket, ok := manager.IssueQuotaObservationTicketForAuth(oldSnapshot)
			issued <- struct {
				ticket QuotaObservationTicket
				ok     bool
			}{ticket: ticket, ok: ok}
		}()
		go func() {
			<-start
			_, errUpdate := manager.Update(ctx, &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive})
			updated <- errUpdate
		}()
		close(start)

		result := <-issued
		if errUpdate := <-updated; errUpdate != nil {
			t.Fatalf("Update() error = %v", errUpdate)
		}
		current, okCurrent := manager.GetByID(authID)
		if !okCurrent || current == nil {
			t.Fatal("current auth missing after update")
		}
		if result.ok {
			if result.ticket.Generation != oldSnapshot.quotaGeneration || result.ticket.Generation == current.quotaGeneration {
				t.Fatalf("old snapshot received ticket %+v for current generation %d", result.ticket, current.quotaGeneration)
			}
			if manager.ApplyQuotaObservationBatch(ctx, quotaScoreOnlyBatch(result.ticket, 50)) {
				t.Fatal("old-generation race ticket remained valid after update")
			}
		}
	}
}

func TestManagerQuotaObservationTicketForAuthSnapshotRemoveRace(t *testing.T) {
	for iteration := range 64 {
		manager := NewManager(nil, nil, nil)
		ctx := context.Background()
		authID := fmt.Sprintf("snapshot-remove-race-%d", iteration)
		oldSnapshot, errRegister := manager.Register(ctx, &Auth{
			ID: authID, Provider: "opencode-go", Status: StatusActive,
		})
		if errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}

		start := make(chan struct{})
		issued := make(chan struct {
			ticket QuotaObservationTicket
			ok     bool
		}, 1)
		removed := make(chan struct{})
		go func() {
			<-start
			ticket, ok := manager.IssueQuotaObservationTicketForAuth(oldSnapshot)
			issued <- struct {
				ticket QuotaObservationTicket
				ok     bool
			}{ticket: ticket, ok: ok}
		}()
		go func() {
			<-start
			manager.Remove(ctx, authID)
			close(removed)
		}()
		close(start)

		result := <-issued
		<-removed
		if _, okCurrent := manager.GetByID(authID); okCurrent {
			t.Fatal("auth remained published after remove")
		}
		if result.ok {
			if result.ticket.Generation != oldSnapshot.quotaGeneration {
				t.Fatalf("old snapshot received ticket %+v for another generation", result.ticket)
			}
			if manager.ApplyQuotaObservationBatch(ctx, quotaScoreOnlyBatch(result.ticket, 50)) {
				t.Fatal("old-generation race ticket remained valid after remove")
			}
		}
	}
}

func assertSnapshotTicketRejectionConsumesNoOrder(t *testing.T, manager *Manager, resolved *Auth) {
	t.Helper()
	authID := "snapshot-validation"
	provider := "opencode-go"
	if resolved != nil {
		if id := strings.TrimSpace(resolved.ID); id != "" {
			authID = id
		}
		if resolved.Provider != "" {
			provider = resolved.Provider
		}
	}
	before, _ := manager.IssueQuotaObservationTicket(authID, provider)
	if ticket, ok := manager.IssueQuotaObservationTicketForAuth(resolved); ok {
		t.Fatalf("IssueQuotaObservationTicketForAuth() accepted stale or invalid snapshot: %+v", ticket)
	}
	after, _ := manager.IssueQuotaObservationTicket(authID, provider)
	if after.StartOrder != before.StartOrder+1 {
		t.Fatalf("rejection advanced start order from %d to %d", before.StartOrder, after.StartOrder)
	}
}

func TestManagerRuntimeQuotaCommitRevisionTransitions(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "runtime-quota-revision"
	model := "runtime-quota-model"
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "opencode-go"}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	assertQuotaRevision := func(want uint64) {
		t.Helper()
		ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
		if !ok {
			t.Fatal("IssueQuotaObservationTicket() rejected valid identity")
		}
		if ticket.Revision != want {
			t.Fatalf("quota revision = %d, want %d", ticket.Revision, want)
		}
	}
	assertQuotaRevision(0)

	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "opencode-go",
		Model:    model,
		Success:  false,
		Error:    &Error{Message: "transient", HTTPStatus: http.StatusInternalServerError},
	})
	assertQuotaRevision(0)
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Model: model, Success: true})
	assertQuotaRevision(0)

	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "opencode-go",
		Model:    model,
		Success:  false,
		Error:    &Error{Code: quotaPollerErrorCode, Message: "quota", HTTPStatus: http.StatusTooManyRequests},
	})
	assertQuotaRevision(0)
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "opencode-go",
		Model:    model,
		Success:  false,
		Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
	})
	assertQuotaRevision(1)

	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Model: model, Success: true})
	assertQuotaRevision(2)
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Model: model, Success: true})
	assertQuotaRevision(2)
}

func TestManagerAuthLevelRuntimeQuotaCommitRevisionTransitions(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "auth-level-runtime-quota-revision"
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "opencode-go"}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	assertRevision := func(want uint64) {
		t.Helper()
		ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
		if !ok || ticket.Revision != want {
			t.Fatalf("quota ticket = %+v, %v; want revision %d", ticket, ok, want)
		}
	}
	assertRevision(0)

	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "opencode-go",
		Success:  false,
		Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
	})
	assertRevision(1)
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil || !auth.Quota.Exceeded || auth.Quota.Reason != "quota" {
		t.Fatalf("auth quota = %+v, want exceeded runtime quota", auth)
	}

	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Success: true})
	assertRevision(2)
	auth, ok = manager.GetByID(authID)
	if !ok || auth == nil || quotaStateIsSet(auth.Quota) || auth.Unavailable {
		t.Fatalf("auth state = %+v, want recovered quota", auth)
	}

	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Success: true})
	assertRevision(2)
}

func TestManagerNonQuota429DoesNotAdvanceQuotaCommitRevision(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
	}{
		{
			name: "cloudflare challenge",
			err:  &Error{Message: "cloudflare challenge", HTTPStatus: http.StatusTooManyRequests},
		},
		{
			name: "request scoped",
			err:  &Error{Code: requestScopedErrorCode, Message: "request limited", HTTPStatus: http.StatusTooManyRequests},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			authID := "non-quota-429-" + tt.name
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go"}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}

			manager.MarkResult(context.Background(), Result{
				AuthID:   authID,
				Provider: "opencode-go",
				Success:  false,
				Error:    tt.err,
			})
			ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
			if !ok || ticket.Revision != 0 {
				t.Fatalf("quota ticket = %+v, %v; want revision 0", ticket, ok)
			}
		})
	}
}

func TestManagerQuotaCommitOrdersMutationAndPublication(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "quota-commit-order"
	model := "quota-commit-order-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "opencode-go", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "opencode-go"}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	manager.scheduler.mu.Lock()
	quotaDone := make(chan struct{})
	go func() {
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: "opencode-go",
			Model:    model,
			Success:  false,
			Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
		})
		close(quotaDone)
	}()
	waitForQuotaState(t, manager, authID, model, true)

	successDone := make(chan struct{})
	go func() {
		manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Model: model, Success: true})
		close(successDone)
	}()
	select {
	case <-successDone:
		manager.scheduler.mu.Unlock()
		t.Fatal("newer success mutated auth before the older quota publication completed")
	case <-time.After(50 * time.Millisecond):
	}
	manager.scheduler.mu.Unlock()

	select {
	case <-quotaDone:
	case <-time.After(2 * time.Second):
		t.Fatal("quota result did not complete after scheduler publication unblocked")
	}
	select {
	case <-successDone:
	case <-time.After(2 * time.Second):
		t.Fatal("success result did not complete after quota commit")
	}

	waitForQuotaState(t, manager, authID, model, false)
	schedulerAuth := schedulerAuthSnapshot(t, manager, authID)
	if state := schedulerAuth.ModelStates[model]; state == nil || quotaStateIsSet(state.Quota) || state.Unavailable {
		t.Fatalf("scheduler model state = %+v, want recovered", state)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count = %d, want 1", count)
	}
	ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if !ok || ticket.Revision != 2 {
		t.Fatalf("quota ticket = %+v, %v; want revision 2", ticket, ok)
	}
}

func TestManagerQuotaCommitPersistenceDoesNotBlockNewerCommit(t *testing.T) {
	store := &quotaCommitBlockingStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager := NewManager(store, nil, nil)
	store.manager = manager
	ctx := context.Background()
	authID := "quota-persistence-race"
	model := "quota-persistence-race-model"
	cooldownStore := &lifecycleCooldownLockCheckingStore{manager: manager, authID: authID}
	manager.SetCooldownStateStore(cooldownStore)
	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "opencode-go",
		Metadata: map[string]any{"persist": true},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	store.blockQuota.Store(true)

	quotaDone := make(chan struct{})
	go func() {
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: "opencode-go",
			Model:    model,
			Success:  false,
			Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
		})
		close(quotaDone)
	}()
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("quota persistence did not block")
	}

	successDone := make(chan struct{})
	go func() {
		manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Model: model, Success: true})
		close(successDone)
	}()
	select {
	case <-successDone:
	case <-time.After(2 * time.Second):
		close(store.release)
		t.Fatal("newer quota commit blocked behind older auth persistence")
	}
	waitForQuotaState(t, manager, authID, model, false)
	close(store.release)
	select {
	case <-quotaDone:
	case <-time.After(2 * time.Second):
		t.Fatal("older quota result did not complete after persistence unblocked")
	}
	if store.saveObservedCommitLock.Load() {
		t.Fatal("auth persistence observed the per-auth commit lock held")
	}
	if cooldownStore.saveObservedCommitLock.Load() {
		t.Fatal("cooldown persistence observed the per-auth commit lock held")
	}
}

func TestManagerQuotaCommitCooldownPersistenceDoesNotBlockNewerCommit(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "quota-cooldown-persistence-race"
	model := "quota-cooldown-persistence-race-model"
	store := &quotaCommitBlockingCooldownStore{
		manager: manager,
		authID:  authID,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(store.unblock)
	manager.SetCooldownStateStore(store)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "opencode-go", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "opencode-go"}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	quotaDone := make(chan struct{})
	go func() {
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: "opencode-go",
			Model:    model,
			Success:  false,
			Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
		})
		close(quotaDone)
	}()
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("quota cooldown persistence did not block")
	}

	successDone := make(chan struct{})
	go func() {
		manager.MarkResult(ctx, Result{AuthID: authID, Provider: "opencode-go", Model: model, Success: true})
		close(successDone)
	}()
	waitForQuotaState(t, manager, authID, model, false)
	ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if !ok || ticket.Revision != 2 {
		t.Fatalf("quota ticket = %+v, %v; want newer commit revision 2", ticket, ok)
	}
	schedulerAuth := schedulerAuthSnapshot(t, manager, authID)
	if state := schedulerAuth.ModelStates[model]; state == nil || quotaStateIsSet(state.Quota) || state.Unavailable {
		t.Fatalf("scheduler model state = %+v, want recovered while old cooldown save is blocked", state)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count = %d, want 1 while old cooldown save is blocked", count)
	}
	if store.saveObservedCommitLock.Load() {
		t.Fatal("cooldown persistence observed the per-auth commit lock held")
	}

	store.unblock()
	select {
	case <-quotaDone:
	case <-time.After(2 * time.Second):
		t.Fatal("older quota result did not complete after cooldown persistence unblocked")
	}
	select {
	case <-successDone:
	case <-time.After(2 * time.Second):
		t.Fatal("newer success did not complete after cooldown persistence unblocked")
	}
}

func waitForQuotaState(t *testing.T, manager *Manager, authID, model string, exceeded bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		auth, ok := manager.GetByID(authID)
		if ok && auth != nil {
			state := auth.ModelStates[model]
			if state != nil && state.Quota.Exceeded == exceeded {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("model quota exceeded did not become %v", exceeded)
}

type quotaCommitBlockingStore struct {
	manager                *Manager
	blockQuota             atomic.Bool
	started                chan struct{}
	release                chan struct{}
	startOnce              sync.Once
	saveObservedCommitLock atomic.Bool
}

func (*quotaCommitBlockingStore) List(context.Context) ([]*Auth, error) {
	return nil, nil
}

func (s *quotaCommitBlockingStore) Save(_ context.Context, auth *Auth) (string, error) {
	if s.manager != nil && auth != nil && !tryLifecycleCommitLock(s.manager, auth.ID) {
		s.saveObservedCommitLock.Store(true)
	}
	if auth != nil && s.blockQuota.Load() && quotaStateIsSet(auth.Quota) {
		s.startOnce.Do(func() { close(s.started) })
		<-s.release
	}
	if auth == nil {
		return "", nil
	}
	return auth.ID, nil
}

func (*quotaCommitBlockingStore) Delete(context.Context, string) error {
	return nil
}

type quotaCommitBlockingCooldownStore struct {
	manager                *Manager
	authID                 string
	started                chan struct{}
	release                chan struct{}
	startOnce              sync.Once
	releaseOnce            sync.Once
	saveCount              atomic.Int32
	saveObservedCommitLock atomic.Bool
}

func (*quotaCommitBlockingCooldownStore) Load(context.Context) ([]CooldownStateRecord, error) {
	return nil, nil
}

func (s *quotaCommitBlockingCooldownStore) Save(context.Context, []CooldownStateRecord) error {
	if !tryLifecycleCommitLock(s.manager, s.authID) {
		s.saveObservedCommitLock.Store(true)
	}
	if s.saveCount.Add(1) == 1 {
		s.startOnce.Do(func() { close(s.started) })
		<-s.release
	}
	return nil
}

func (s *quotaCommitBlockingCooldownStore) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func TestManagerApplyQuotaResultTransitionsThroughCooldownPersistence(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	store := &recordingCooldownStateStore{}
	manager.cooldownStore = store
	ctx := context.Background()
	authID := "opencode-quota-auth"
	model := "opencode-quota-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "opencode-go", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	store.saveCount.Store(0)
	retryAfter := time.Hour

	manager.ApplyQuotaResult(ctx, QuotaResultUpdate{
		AuthID:            authID,
		Provider:          "opencode-go",
		Score:             0.5,
		ThresholdExceeded: true,
		RetryAfter:        &retryAfter,
	})
	if got, ok := manager.QuotaScore(authID); !ok || got != 0.5 {
		t.Fatalf("QuotaScore() = %v, %v; want 0.5, true", got, ok)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("save count after threshold crossing = %d, want 1", got)
	}
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("missing updated auth")
	}
	if updated.Failed != 1 {
		t.Fatalf("auth failed count after threshold crossing = %d, want 1", updated.Failed)
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Quota.Exceeded || state.Quota.Reason != "quota" || !state.Unavailable {
		t.Fatalf("model quota state = %+v, want exceeded quota", state)
	}
	_, errAvailable := manager.availableAuthsForRouteModel([]*Auth{updated}, "opencode-go", model, time.Now())
	var cooldownErr *modelCooldownError
	if !errors.As(errAvailable, &cooldownErr) {
		t.Fatalf("availableAuthsForRouteModel() during quota error = %T, want *modelCooldownError", errAvailable)
	}

	manager.ApplyQuotaResult(ctx, QuotaResultUpdate{
		AuthID:            authID,
		Provider:          "opencode-go",
		Score:             0.4,
		ThresholdExceeded: true,
		RetryAfter:        &retryAfter,
	})
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("save count after duplicate threshold state = %d, want 1", got)
	}
	if got, ok := manager.QuotaScore(authID); !ok || got != 0.4 {
		t.Fatalf("QuotaScore() after duplicate = %v, %v; want 0.4, true", got, ok)
	}
	updated, _ = manager.GetByID(authID)
	if updated.Failed != 1 {
		t.Fatalf("auth failed count after duplicate threshold state = %d, want 1", updated.Failed)
	}

	manager.ApplyQuotaResult(ctx, QuotaResultUpdate{
		AuthID:            authID,
		Provider:          "opencode-go",
		Score:             25,
		ThresholdExceeded: false,
	})
	if got := store.saveCount.Load(); got != 2 {
		t.Fatalf("save count after recovery = %d, want 2", got)
	}
	updated, _ = manager.GetByID(authID)
	if updated.Success != 1 || updated.Failed != 1 {
		t.Fatalf("auth counts after recovery = success %d failed %d, want 1/1", updated.Success, updated.Failed)
	}
	state = updated.ModelStates[model]
	if state == nil || state.Quota.Exceeded || state.Unavailable || state.Status != StatusActive {
		t.Fatalf("model quota state after recovery = %+v, want active", state)
	}
	available, errAvailable := manager.availableAuthsForRouteModel([]*Auth{updated}, "opencode-go", model, time.Now())
	if errAvailable != nil {
		t.Fatalf("availableAuthsForRouteModel() after recovery error = %v", errAvailable)
	}
	if len(available) != 1 || available[0].ID != authID {
		t.Fatalf("availableAuthsForRouteModel() after recovery = %+v, want %q", available, authID)
	}

	manager.ApplyQuotaResult(ctx, QuotaResultUpdate{
		AuthID:            authID,
		Provider:          "opencode-go",
		Score:             30,
		ThresholdExceeded: false,
	})
	if got := store.saveCount.Load(); got != 2 {
		t.Fatalf("save count after duplicate recovery = %d, want 2", got)
	}
	updated, _ = manager.GetByID(authID)
	if updated.Success != 1 || updated.Failed != 1 {
		t.Fatalf("auth counts after duplicate recovery = success %d failed %d, want 1/1", updated.Success, updated.Failed)
	}
}

func TestManagerSetQuotaScoreNormalizesAndDoesNotMutateMetadata(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	authID := "quota-normalized-auth"
	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "opencode-go",
		Status:   StatusActive,
		Metadata: map[string]any{"quota_score": "preserve"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	tests := []struct {
		name  string
		score float64
		want  float64
	}{
		{name: "nan", score: math.NaN(), want: 0},
		{name: "positive inf", score: math.Inf(1), want: 0},
		{name: "negative inf", score: math.Inf(-1), want: 0},
		{name: "negative", score: -5, want: 0},
		{name: "zero", score: 0, want: 0},
		{name: "fraction", score: 0.5, want: 0.5},
		{name: "clamped", score: 150, want: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager.SetQuotaScore(authID, tt.score)
			got, ok := manager.QuotaScore(authID)
			if !ok || got != tt.want {
				t.Fatalf("QuotaScore() = %v, %v; want %v, true", got, ok, tt.want)
			}
			updated, ok := manager.GetByID(authID)
			if !ok || updated == nil {
				t.Fatalf("GetByID(%s) missing auth", authID)
			}
			if len(updated.Metadata) != 1 || updated.Metadata["quota_score"] != "preserve" {
				t.Fatalf("auth Metadata = %#v, want preserved without quota score writes", updated.Metadata)
			}
		})
	}
}

func TestManagerQuotaRefreshCallbackFiresOnlyForRequest429(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	var calls atomic.Int64
	manager.SetQuotaRefreshCallback(func(authID string) {
		if authID != "opencode-auth" {
			t.Errorf("callback authID = %q, want opencode-auth", authID)
		}
		calls.Add(1)
	})

	manager.MarkResult(context.Background(), Result{
		AuthID:   "opencode-auth",
		Provider: "opencode-go",
		Success:  false,
		Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
	})
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls after request 429 = %d, want 1", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "opencode-auth",
		Provider: "opencode-go",
		Success:  false,
		Error:    &Error{Code: quotaPollerErrorCode, Message: "quota", HTTPStatus: http.StatusTooManyRequests},
	})
	manager.MarkResult(context.Background(), Result{
		AuthID:   "other-auth",
		Provider: "claude",
		Success:  false,
		Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
	})
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls after ignored 429s = %d, want 1", got)
	}
}

func TestManagerQuotaObservationBatchRejectsInvalidInputAtomically(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	store := &recordingCooldownStateStore{}
	manager.cooldownStore = store
	authID := "quota-observation-validation"
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if !ok {
		t.Fatal("IssueQuotaObservationTicket() rejected valid identity")
	}
	resetAt := time.Now().Add(time.Hour)
	invalidScore := math.NaN()

	tests := []struct {
		name  string
		batch QuotaObservationBatch
	}{
		{
			name: "invalid score",
			batch: QuotaObservationBatch{
				Ticket:       ticket,
				Score:        &invalidScore,
				Completeness: QuotaObservationCompletenessScoreOnly,
			},
		},
		{
			name: "model scope",
			batch: QuotaObservationBatch{
				Ticket:       ticket,
				Completeness: QuotaObservationCompletenessExhaustionEvidence,
				Mutations: []QuotaObservationMutation{{
					Scope:   QuotaObservationScopeModel,
					Model:   "model-a",
					Outcome: QuotaObservationOutcomeExhausted,
					ResetAt: resetAt,
				}},
			},
		},
		{
			name: "score only mutation",
			batch: QuotaObservationBatch{
				Ticket:       ticket,
				Score:        quotaScorePtr(20),
				Completeness: QuotaObservationCompletenessScoreOnly,
				Mutations: []QuotaObservationMutation{{
					Scope:   QuotaObservationScopeAuth,
					Outcome: QuotaObservationOutcomeHealthy,
				}},
			},
		},
		{
			name: "exhaustion without reset",
			batch: QuotaObservationBatch{
				Ticket:       ticket,
				Completeness: QuotaObservationCompletenessExhaustionEvidence,
				Mutations: []QuotaObservationMutation{{
					Scope:   QuotaObservationScopeAuth,
					Outcome: QuotaObservationOutcomeExhausted,
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if manager.ApplyQuotaObservationBatch(context.Background(), tt.batch) {
				t.Fatal("ApplyQuotaObservationBatch() accepted invalid batch")
			}
			if _, exists := manager.QuotaScore(authID); exists {
				t.Fatal("invalid batch changed quota score")
			}
			auth, _ := manager.GetByID(authID)
			if auth == nil || auth.Unavailable || quotaStateIsSet(auth.Quota) {
				t.Fatalf("invalid batch changed auth = %+v", auth)
			}
			if got := store.saveCount.Load(); got != 0 {
				t.Fatalf("invalid batch cooldown saves = %d, want 0", got)
			}
		})
	}

	valid := QuotaObservationBatch{
		Ticket:       ticket,
		Score:        quotaScorePtr(20),
		Completeness: QuotaObservationCompletenessScoreOnly,
	}
	if !manager.ApplyQuotaObservationBatch(context.Background(), valid) {
		t.Fatal("invalid batches consumed the valid ticket order")
	}
}

func TestManagerQuotaObservationBatchRejectsCausalMismatches(t *testing.T) {
	t.Run("provider mismatch", func(t *testing.T) {
		manager := newQuotaObservationTestManager(t, "quota-provider-mismatch")
		ticket, _ := manager.IssueQuotaObservationTicket("quota-provider-mismatch", "other-provider")
		if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(ticket, 12)) {
			t.Fatal("ApplyQuotaObservationBatch() accepted provider mismatch")
		}
		assertQuotaObservationNoScore(t, manager, "quota-provider-mismatch")
	})

	t.Run("generation mismatch", func(t *testing.T) {
		manager := newQuotaObservationTestManager(t, "quota-generation-mismatch")
		ticket, _ := manager.IssueQuotaObservationTicket("quota-generation-mismatch", "opencode-go")
		auth, _ := manager.GetByID("quota-generation-mismatch")
		if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
			t.Fatalf("Update() error = %v", errUpdate)
		}
		if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(ticket, 12)) {
			t.Fatal("ApplyQuotaObservationBatch() accepted stale generation")
		}
		assertQuotaObservationNoScore(t, manager, "quota-generation-mismatch")
	})

	t.Run("revision mismatch", func(t *testing.T) {
		manager := newQuotaObservationTestManager(t, "quota-revision-mismatch")
		ticket, _ := manager.IssueQuotaObservationTicket("quota-revision-mismatch", "opencode-go")
		manager.MarkResult(context.Background(), Result{
			AuthID:   "quota-revision-mismatch",
			Provider: "opencode-go",
			Success:  false,
			Error:    &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
		})
		if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(ticket, 12)) {
			t.Fatal("ApplyQuotaObservationBatch() accepted stale revision")
		}
		assertQuotaObservationNoScore(t, manager, "quota-revision-mismatch")
	})

	t.Run("out of order", func(t *testing.T) {
		manager := newQuotaObservationTestManager(t, "quota-order-mismatch")
		older, _ := manager.IssueQuotaObservationTicket("quota-order-mismatch", "opencode-go")
		newer, _ := manager.IssueQuotaObservationTicket("quota-order-mismatch", "opencode-go")
		if !manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(newer, 22)) {
			t.Fatal("ApplyQuotaObservationBatch() rejected newer observation")
		}
		if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(older, 11)) {
			t.Fatal("ApplyQuotaObservationBatch() accepted out-of-order observation")
		}
		if got, ok := manager.QuotaScore("quota-order-mismatch"); !ok || got != 22 {
			t.Fatalf("QuotaScore() = %v, %v; want 22, true", got, ok)
		}
	})

	t.Run("unissued future order", func(t *testing.T) {
		manager := newQuotaObservationTestManager(t, "quota-future-order")
		issued, _ := manager.IssueQuotaObservationTicket("quota-future-order", "opencode-go")
		forged := issued
		forged.StartOrder += 100
		if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(forged, 99)) {
			t.Fatal("ApplyQuotaObservationBatch() accepted unissued future order")
		}
		assertQuotaObservationNoScore(t, manager, "quota-future-order")
		if !manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(issued, 21)) {
			t.Fatal("rejected forged order consumed the issued ticket")
		}
		if got, ok := manager.QuotaScore("quota-future-order"); !ok || got != 21 {
			t.Fatalf("QuotaScore() = %v, %v; want 21, true", got, ok)
		}
	})
}

func TestManagerQuotaObservationBatchResetExpiresWhileCommitBlocked(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-expired-under-lock")
	authID := "quota-expired-under-lock"
	cooldownStore := &recordingCooldownStateStore{}
	manager.cooldownStore = cooldownStore
	ticket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	state := manager.quotaCommitState(authID)
	state.commitMu.Lock()

	entered := make(chan struct{})
	accepted := make(chan bool, 1)
	resetAt := time.Now().Add(250 * time.Millisecond)
	go func() {
		close(entered)
		accepted <- manager.ApplyQuotaObservationBatch(context.Background(), QuotaObservationBatch{
			Ticket:       ticket,
			Score:        quotaScorePtr(3),
			Completeness: QuotaObservationCompletenessExhaustionEvidence,
			Mutations: []QuotaObservationMutation{{
				Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: resetAt,
			}},
		})
	}()
	<-entered
	time.Sleep(50 * time.Millisecond)
	select {
	case got := <-accepted:
		state.commitMu.Unlock()
		t.Fatalf("ApplyQuotaObservationBatch() returned %v before held commit lock was released", got)
	default:
	}
	time.Sleep(time.Until(resetAt) + 25*time.Millisecond)
	state.commitMu.Unlock()

	select {
	case got := <-accepted:
		if got {
			t.Fatal("ApplyQuotaObservationBatch() accepted reset that expired while waiting for commit lock")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ApplyQuotaObservationBatch() did not return after commit lock released")
	}
	assertQuotaObservationNoScore(t, manager, authID)
	auth, _ := manager.GetByID(authID)
	if auth == nil || auth.Unavailable || quotaStateIsSet(auth.Quota) {
		t.Fatalf("expired reset changed auth = %+v", auth)
	}
	if got := cooldownStore.saveCount.Load(); got != 0 {
		t.Fatalf("expired reset cooldown saves = %d, want 0", got)
	}

	issuedAfterRejection, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if !manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(issuedAfterRejection, 17)) {
		t.Fatal("expired reset rejection consumed a later issued ticket")
	}
}

func TestManagerQuotaObservationBatchUsesLatestResetAfterCommitBlock(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-latest-reset-under-lock")
	authID := "quota-latest-reset-under-lock"
	ticket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	state := manager.quotaCommitState(authID)
	state.commitMu.Lock()

	earlyReset := time.Now().Add(250 * time.Millisecond)
	lateReset := time.Now().Add(2 * time.Second)
	entered := make(chan struct{})
	accepted := make(chan bool, 1)
	go func() {
		close(entered)
		accepted <- manager.ApplyQuotaObservationBatch(context.Background(), QuotaObservationBatch{
			Ticket:       ticket,
			Completeness: QuotaObservationCompletenessExhaustionEvidence,
			Mutations: []QuotaObservationMutation{
				{Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: earlyReset},
				{Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: lateReset},
			},
		})
	}()
	<-entered
	time.Sleep(50 * time.Millisecond)
	time.Sleep(time.Until(earlyReset) + 25*time.Millisecond)
	state.commitMu.Unlock()

	select {
	case got := <-accepted:
		if !got {
			t.Fatal("ApplyQuotaObservationBatch() rejected batch with a later active blocker")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ApplyQuotaObservationBatch() did not return after commit lock released")
	}
	auth, _ := manager.GetByID(authID)
	if auth == nil || auth.Quota.Reason != quotaObservationErrorCode || !auth.Quota.NextRecoverAt.Equal(lateReset) ||
		!auth.NextRetryAfter.Equal(lateReset) {
		t.Fatalf("auth quota = %+v, want latest reset %v", auth, lateReset)
	}
}

func TestManagerQuotaObservationBatchScoreOnlyDoesNotChangeCooldown(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-score-only")
	manager.MarkResult(context.Background(), Result{
		AuthID:   "quota-score-only",
		Provider: "opencode-go",
		Success:  false,
		Error:    &Error{Message: "unauthorized", HTTPStatus: http.StatusUnauthorized},
	})
	before, _ := manager.GetByID("quota-score-only")
	ticket, _ := manager.IssueQuotaObservationTicket("quota-score-only", "opencode-go")
	if !manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(ticket, 33)) {
		t.Fatal("ApplyQuotaObservationBatch() rejected score-only observation")
	}
	after, _ := manager.GetByID("quota-score-only")
	if after.LastError == nil || after.LastError.HTTPStatus != http.StatusUnauthorized ||
		after.NextRetryAfter != before.NextRetryAfter || after.Unavailable != before.Unavailable {
		t.Fatalf("score-only observation changed cooldown: before=%+v after=%+v", before, after)
	}
	manager.scheduler.mu.Lock()
	schedulerScore, ok := manager.scheduler.quotaScoreLocked("quota-score-only")
	manager.scheduler.mu.Unlock()
	if !ok || schedulerScore != 33 {
		t.Fatalf("scheduler score = %v, %v; want 33, true", schedulerScore, ok)
	}
}

func TestManagerQuotaObservationBatchExhaustionAndAuthoritativeRecovery(t *testing.T) {
	hook := &resultCaptureHook{}
	manager := NewManager(nil, nil, hook)
	store := &recordingCooldownStateStore{}
	manager.cooldownStore = store
	authID := "quota-observation-transition"
	model := "quota-observation-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "opencode-go", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	ticket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	earlier := time.Now().Add(time.Hour)
	later := earlier.Add(time.Hour)
	exhausted := QuotaObservationBatch{
		Ticket:       ticket,
		Score:        quotaScorePtr(2),
		Completeness: QuotaObservationCompletenessExhaustionEvidence,
		Mutations: []QuotaObservationMutation{
			{Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: earlier},
			{Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: later},
		},
	}
	if !manager.ApplyQuotaObservationBatch(context.Background(), exhausted) {
		t.Fatal("ApplyQuotaObservationBatch() rejected exhaustion evidence")
	}
	auth, _ := manager.GetByID(authID)
	if auth == nil || !auth.Unavailable || auth.LastError == nil || auth.LastError.Code != quotaObservationErrorCode ||
		auth.Quota.Reason != quotaObservationErrorCode || !auth.NextRetryAfter.Equal(later) || !auth.Quota.NextRecoverAt.Equal(later) {
		t.Fatalf("auth after exhaustion = %+v, want QuotaHub auth-wide cooldown through %v", auth, later)
	}
	ticketAfterExhaustion, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if ticketAfterExhaustion.Revision != ticket.Revision {
		t.Fatalf("accepted observation revision = %d, want unchanged %d", ticketAfterExhaustion.Revision, ticket.Revision)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("cooldown saves after exhaustion = %d, want 1", got)
	}
	if len(hook.Results()) != 0 || auth.Success != 0 || auth.Failed != 0 {
		t.Fatalf("observation changed execution stats/hooks: auth=%+v results=%d", auth, len(hook.Results()))
	}

	healthy := QuotaObservationBatch{
		Ticket:       ticketAfterExhaustion,
		Score:        quotaScorePtr(90),
		Completeness: QuotaObservationCompletenessAuthoritativeSnapshot,
		Mutations: []QuotaObservationMutation{{
			Scope:   QuotaObservationScopeAuth,
			Outcome: QuotaObservationOutcomeHealthy,
		}},
	}
	if !manager.ApplyQuotaObservationBatch(context.Background(), healthy) {
		t.Fatal("ApplyQuotaObservationBatch() rejected authoritative recovery")
	}
	auth, _ = manager.GetByID(authID)
	if auth == nil || auth.Unavailable || quotaStateIsSet(auth.Quota) || auth.LastError != nil || auth.Status != StatusActive {
		t.Fatalf("auth after recovery = %+v, want active", auth)
	}
	if got := store.saveCount.Load(); got != 2 {
		t.Fatalf("cooldown saves after recovery = %d, want 2", got)
	}
	if len(hook.Results()) != 0 || auth.Success != 0 || auth.Failed != 0 {
		t.Fatalf("recovery changed execution stats/hooks: auth=%+v results=%d", auth, len(hook.Results()))
	}
}

func TestManagerQuotaObservationBatchRecoveryPreservesUnrelatedErrors(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-preserve-errors")
	authID := "quota-preserve-errors"
	unauthorizedModel := "unauthorized-model"
	cloudflareModel := "cloudflare-model"
	quotaModel := "quota-model"

	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: unauthorizedModel, Success: false,
		Error: &Error{Message: "unauthorized", HTTPStatus: http.StatusUnauthorized},
	})
	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: cloudflareModel, Success: false,
		Error: &Error{Message: "cloudflare challenge", HTTPStatus: http.StatusTooManyRequests},
	})
	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: quotaModel, Success: false,
		Error: &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
	})
	ticket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	healthy := QuotaObservationBatch{
		Ticket:       ticket,
		Completeness: QuotaObservationCompletenessAuthoritativeSnapshot,
		Mutations:    []QuotaObservationMutation{{Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeHealthy}},
	}
	if !manager.ApplyQuotaObservationBatch(context.Background(), healthy) {
		t.Fatal("ApplyQuotaObservationBatch() rejected authoritative recovery")
	}
	auth, _ := manager.GetByID(authID)
	if state := auth.ModelStates[quotaModel]; state == nil || !modelStateIsClean(state) {
		t.Fatalf("runtime quota state = %+v, want cleared", state)
	}
	if state := auth.ModelStates[unauthorizedModel]; state == nil || state.LastError == nil || state.LastError.HTTPStatus != http.StatusUnauthorized || !state.Unavailable {
		t.Fatalf("unauthorized state = %+v, want preserved", state)
	}
	if state := auth.ModelStates[cloudflareModel]; state == nil || state.LastError == nil || state.Quota.Reason != "cloudflare challenge" || !state.Unavailable {
		t.Fatalf("Cloudflare state = %+v, want preserved", state)
	}
	if auth.Status != StatusError || auth.LastError == nil || auth.LastError.HTTPStatus == http.StatusTooManyRequests && auth.LastError.Message == "quota" {
		t.Fatalf("aggregated unrelated error = %+v, want preserved non-quota error", auth)
	}
}

func TestManagerQuotaObservationBatchRecoveryRecomputesAggregateAfterLaterUnrelatedError(t *testing.T) {
	t.Run("model unauthorized", func(t *testing.T) {
		manager := newQuotaObservationTestManager(t, "quota-later-model-error")
		authID := "quota-later-model-error"
		quotaModel := "quota-first-model"
		unauthorizedModel := "unauthorized-later-model"
		manager.MarkResult(context.Background(), Result{
			AuthID: authID, Provider: "opencode-go", Model: quotaModel, Success: false,
			Error: &Error{Message: "quota first", HTTPStatus: http.StatusTooManyRequests},
		})
		manager.MarkResult(context.Background(), Result{
			AuthID: authID, Provider: "opencode-go", Model: unauthorizedModel, Success: false,
			Error: &Error{Message: "unauthorized later", HTTPStatus: http.StatusUnauthorized},
		})

		applyAuthoritativeHealthyObservation(t, manager, authID)
		auth, _ := manager.GetByID(authID)
		if state := auth.ModelStates[quotaModel]; state == nil || !modelStateIsClean(state) {
			t.Fatalf("quota model state = %+v, want cleared", state)
		}
		if state := auth.ModelStates[unauthorizedModel]; state == nil || state.LastError == nil || state.LastError.HTTPStatus != http.StatusUnauthorized {
			t.Fatalf("later unauthorized model state = %+v, want preserved", state)
		}
		if quotaStateIsSet(auth.Quota) {
			t.Fatalf("auth aggregate quota = %+v, want cleared", auth.Quota)
		}
		if auth.LastError == nil || auth.LastError.HTTPStatus != http.StatusUnauthorized || auth.Status != StatusError {
			t.Fatalf("auth unrelated aggregate error = %+v, want preserved unauthorized", auth)
		}
	})

	for _, tt := range []struct {
		name        string
		status      int
		message     string
		quotaReason string
	}{
		{name: "auth payment", status: http.StatusPaymentRequired, message: "payment later"},
		{name: "auth cloudflare", status: http.StatusTooManyRequests, message: "cloudflare challenge", quotaReason: "cloudflare challenge"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			authID := "quota-later-" + tt.name
			manager := newQuotaObservationTestManager(t, authID)
			manager.MarkResult(context.Background(), Result{
				AuthID: authID, Provider: "opencode-go", Model: "quota-first-model", Success: false,
				Error: &Error{Message: "quota first", HTTPStatus: http.StatusTooManyRequests},
			})
			manager.MarkResult(context.Background(), Result{
				AuthID: authID, Provider: "opencode-go", Success: false,
				Error: &Error{Message: tt.message, HTTPStatus: tt.status},
			})
			before, _ := manager.GetByID(authID)

			applyAuthoritativeHealthyObservation(t, manager, authID)
			auth, _ := manager.GetByID(authID)
			if state := auth.ModelStates["quota-first-model"]; state == nil || !modelStateIsClean(state) {
				t.Fatalf("quota model state = %+v, want cleared", state)
			}
			if auth.LastError == nil || auth.LastError.HTTPStatus != tt.status || auth.StatusMessage != before.StatusMessage ||
				auth.Unavailable != before.Unavailable || !auth.NextRetryAfter.Equal(before.NextRetryAfter) {
				t.Fatalf("later auth error changed: before=%+v after=%+v", before, auth)
			}
			if tt.quotaReason == "" {
				if quotaStateIsSet(auth.Quota) {
					t.Fatalf("stale model quota remained on auth aggregate = %+v", auth.Quota)
				}
			} else if auth.Quota.Reason != tt.quotaReason {
				t.Fatalf("auth quota reason = %q, want preserved %q", auth.Quota.Reason, tt.quotaReason)
			}
		})
	}
}

func TestManagerQuotaObservationBatchAuthWideBlockerSurvivesModelFailure(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		disableCooling bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "transient", status: http.StatusInternalServerError},
		{name: "transient cooling disabled", status: http.StatusInternalServerError, disableCooling: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authID := "quota-hub-model-failure-" + tt.name
			manager := NewManager(nil, nil, nil)
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID: authID, Provider: "opencode-go", Status: StatusActive,
			}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			resetAt := time.Now().Add(time.Hour)
			applyAuthWideQuotaObservationExhaustion(t, manager, authID, resetAt)
			if tt.disableCooling {
				setQuotaObservationTestDisableCooling(t, manager, authID, true)
			}

			model := "unrelated-model"
			manager.MarkResult(context.Background(), Result{
				AuthID: authID, Provider: "opencode-go", Model: model, Success: false,
				Error: &Error{Message: tt.name, HTTPStatus: tt.status},
			})
			auth, _ := manager.GetByID(authID)
			if auth.Quota.Reason != quotaObservationErrorCode || !auth.Quota.NextRecoverAt.Equal(resetAt) ||
				!auth.Unavailable || !auth.NextRetryAfter.Equal(resetAt) {
				t.Fatalf("auth-wide blocker after model failure = %+v, want preserved through %v", auth, resetAt)
			}
			state := auth.ModelStates[model]
			if state == nil || state.LastError == nil || state.LastError.HTTPStatus != tt.status {
				t.Fatalf("unrelated model state = %+v, want preserved %d error", state, tt.status)
			}
			if tt.disableCooling && state.Unavailable {
				t.Fatalf("cooling-disabled model state = %+v, want available error state", state)
			}

			if tt.disableCooling {
				setQuotaObservationTestDisableCooling(t, manager, authID, false)
			}
			applyAuthoritativeHealthyObservation(t, manager, authID)
			auth, _ = manager.GetByID(authID)
			if quotaStateIsSet(auth.Quota) {
				t.Fatalf("authoritative recovery left auth-wide quota = %+v", auth.Quota)
			}
			state = auth.ModelStates[model]
			if state == nil || state.LastError == nil || state.LastError.HTTPStatus != tt.status {
				t.Fatalf("authoritative recovery changed unrelated model state = %+v", state)
			}
			if tt.disableCooling && auth.Unavailable {
				t.Fatalf("cooling-disabled recovery left stale auth blocker = %+v", auth)
			}
		})
	}
}

func TestManagerQuotaObservationBatchAuthUnrelatedStateSurvivesExhaustionAndRecovery(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		message     string
		quotaReason string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, message: "auth unauthorized"},
		{name: "cloudflare", status: http.StatusTooManyRequests, message: "cloudflare challenge", quotaReason: "cloudflare challenge"},
		{name: "transient", status: http.StatusInternalServerError, message: "auth transient"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authID := "quota-auth-unrelated-" + tt.name
			manager := newQuotaObservationTestManager(t, authID)
			manager.MarkResult(context.Background(), Result{
				AuthID: authID, Provider: "opencode-go", Success: false,
				Error: &Error{Message: tt.message, HTTPStatus: tt.status},
			})
			before, _ := manager.GetByID(authID)
			resetAt := time.Now().Add(time.Hour)
			applyAuthWideQuotaObservationExhaustion(t, manager, authID, resetAt)

			exhausted, _ := manager.GetByID(authID)
			if exhausted.Quota.Reason != quotaObservationErrorCode || !exhausted.Quota.NextRecoverAt.Equal(resetAt) {
				t.Fatalf("auth Hub quota = %+v, want blocker through %v", exhausted.Quota, resetAt)
			}
			if exhausted.LastError == nil || exhausted.LastError.HTTPStatus != tt.status ||
				exhausted.Status != before.Status || exhausted.StatusMessage != before.StatusMessage ||
				exhausted.Unavailable != before.Unavailable || !exhausted.NextRetryAfter.Equal(before.NextRetryAfter) {
				t.Fatalf("Hub exhaustion overwrote unrelated auth state: before=%+v after=%+v", before, exhausted)
			}
			if tt.quotaReason != "" && exhausted.Quota.BackoffLevel != before.Quota.BackoffLevel {
				t.Fatalf("Hub exhaustion backoff = %d, want carried %d", exhausted.Quota.BackoffLevel, before.Quota.BackoffLevel)
			}
			if simulatedNow := before.NextRetryAfter.Add(time.Second); simulatedNow.Before(resetAt) {
				record, ok := authCooldownStateRecord(exhausted, simulatedNow)
				if !ok || record.Quota.Reason != quotaObservationErrorCode || !record.NextRetryAfter.Equal(before.NextRetryAfter) {
					t.Fatalf("Hub cooldown record after unrelated retry expiry = %+v, %v", record, ok)
				}
			}

			applyAuthoritativeHealthyObservation(t, manager, authID)
			recovered, _ := manager.GetByID(authID)
			if recovered.LastError == nil || recovered.LastError.HTTPStatus != tt.status ||
				recovered.Status != before.Status || recovered.StatusMessage != before.StatusMessage ||
				recovered.Unavailable != before.Unavailable || !recovered.NextRetryAfter.Equal(before.NextRetryAfter) {
				t.Fatalf("Hub recovery cleared unrelated auth state: before=%+v after=%+v", before, recovered)
			}
			if tt.quotaReason == "" {
				if quotaStateIsSet(recovered.Quota) {
					t.Fatalf("Hub recovery left quota = %+v", recovered.Quota)
				}
			} else if recovered.Quota.Reason != tt.quotaReason ||
				!recovered.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) ||
				recovered.Quota.BackoffLevel != before.Quota.BackoffLevel {
				t.Fatalf("Cloudflare quota after recovery = %+v, want %+v", recovered.Quota, before.Quota)
			}
		})
	}
}

func TestManagerQuotaObservationBatchAuthCloudflarePreservesHubOwner(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-hub-auth-cloudflare")
	authID := "quota-hub-auth-cloudflare"
	hubReset := time.Now().Add(time.Hour)
	applyAuthWideQuotaObservationExhaustion(t, manager, authID, hubReset)

	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Success: false,
		Error: &Error{Message: "cloudflare challenge", HTTPStatus: http.StatusTooManyRequests},
	})
	auth, _ := manager.GetByID(authID)
	if auth.Quota.Reason != quotaObservationErrorCode || !auth.Quota.NextRecoverAt.Equal(hubReset) || auth.Quota.BackoffLevel == 0 {
		t.Fatalf("auth quota after Cloudflare = %+v, want Hub owner/reset with Cloudflare backoff", auth.Quota)
	}
	if auth.LastError == nil || !isCloudflareChallengeResultError(auth.LastError) || auth.StatusMessage != "cloudflare challenge" {
		t.Fatalf("auth Cloudflare error fields = %+v, want preserved", auth)
	}
	cloudflareReset := auth.NextRetryAfter
	if cloudflareReset.IsZero() || !cloudflareReset.Before(hubReset) {
		t.Fatalf("Cloudflare reset = %v, want before Hub reset %v", cloudflareReset, hubReset)
	}
	for _, at := range []time.Time{time.Now(), cloudflareReset.Add(time.Second)} {
		blocked, _, next := isAuthBlockedForModel(auth, "", at)
		if !blocked || !next.Equal(hubReset) {
			t.Fatalf("auth block at %v = %v through %v, want Hub block through %v", at, blocked, next, hubReset)
		}
	}

	applyAuthoritativeHealthyObservation(t, manager, authID)
	recovered, _ := manager.GetByID(authID)
	if recovered.Quota.Reason != "cloudflare challenge" || !recovered.Quota.NextRecoverAt.Equal(cloudflareReset) ||
		recovered.Quota.BackoffLevel != auth.Quota.BackoffLevel || recovered.LastError == nil || !isCloudflareChallengeResultError(recovered.LastError) {
		t.Fatalf("Cloudflare state after Hub recovery = %+v, want restored deadline %v", recovered, cloudflareReset)
	}
}

func TestManagerQuotaObservationBatchCooldownPolicyIgnoresMutation(t *testing.T) {
	tests := []struct {
		name     string
		disabled bool
		status   Status
		metadata map[string]any
	}{
		{name: "disabled flag", disabled: true, status: StatusActive},
		{name: "disabled status", status: StatusDisabled},
		{name: "cooling disabled", status: StatusActive, metadata: map[string]any{"disable_cooling": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authID := "quota-policy-" + tt.name
			manager := NewManager(nil, nil, nil)
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID: authID, Provider: "opencode-go", Disabled: tt.disabled, Status: tt.status, Metadata: tt.metadata,
			}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			store := &recordingCooldownStateStore{}
			manager.cooldownStore = store
			ticket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
			if !manager.ApplyQuotaObservationBatch(context.Background(), QuotaObservationBatch{
				Ticket:       ticket,
				Score:        quotaScorePtr(18),
				Completeness: QuotaObservationCompletenessExhaustionEvidence,
				Mutations: []QuotaObservationMutation{{
					Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: time.Now().Add(time.Hour),
				}},
			}) {
				t.Fatal("ApplyQuotaObservationBatch() rejected policy-ignored accepted batch")
			}
			auth, _ := manager.GetByID(authID)
			if auth.Disabled != tt.disabled || auth.Status != tt.status || auth.Unavailable || quotaStateIsSet(auth.Quota) || auth.LastError != nil {
				t.Fatalf("policy-ignored batch changed cooldown state = %+v", auth)
			}
			if score, ok := manager.QuotaScore(authID); !ok || score != 18 {
				t.Fatalf("QuotaScore() = %v, %v; want 18, true", score, ok)
			}
			if got := store.saveCount.Load(); got != 0 {
				t.Fatalf("policy-ignored cooldown saves = %d, want 0", got)
			}
			if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(ticket, 19)) {
				t.Fatal("policy-ignored accepted batch did not consume causal order")
			}
		})
	}
}

func TestManagerQuotaObservationBatchModelSuccessClearsAuthWideBlockerAndRevision(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-hub-model-success")
	authID := "quota-hub-model-success"
	applyAuthWideQuotaObservationExhaustion(t, manager, authID, time.Now().Add(time.Hour))
	staleTicket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")

	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: "successful-model", Success: true,
	})
	auth, _ := manager.GetByID(authID)
	if auth == nil || auth.Unavailable || quotaStateIsSet(auth.Quota) {
		t.Fatalf("model success left auth-wide quota blocker = %+v", auth)
	}
	currentTicket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if currentTicket.Revision != staleTicket.Revision+1 {
		t.Fatalf("quota revision after model success = %d, want %d", currentTicket.Revision, staleTicket.Revision+1)
	}
	if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(staleTicket, 75)) {
		t.Fatal("ApplyQuotaObservationBatch() accepted ticket invalidated by model success")
	}
	assertQuotaObservationNoScore(t, manager, authID)
}

func TestManagerQuotaObservationBatchModelSuccessInvalidatesTicketWithOtherModelQuota(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-hub-success-other-quota")
	authID := "quota-hub-success-other-quota"
	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: "quota-model", Success: false,
		Error: &Error{Message: "runtime quota", HTTPStatus: http.StatusTooManyRequests},
	})
	applyAuthWideQuotaObservationExhaustion(t, manager, authID, time.Now().Add(time.Hour))
	staleTicket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")

	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: "different-success-model", Success: true,
	})
	auth, _ := manager.GetByID(authID)
	if auth.Quota.Reason == quotaObservationErrorCode {
		t.Fatalf("model success left Hub-owned blocker = %+v", auth.Quota)
	}
	if state := auth.ModelStates["quota-model"]; state == nil || !state.Quota.Exceeded {
		t.Fatalf("other model quota state = %+v, want preserved", state)
	}
	currentTicket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if currentTicket.Revision != staleTicket.Revision+1 {
		t.Fatalf("quota revision after Hub replacement = %d, want %d", currentTicket.Revision, staleTicket.Revision+1)
	}
	if manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(staleTicket, 64)) {
		t.Fatal("ApplyQuotaObservationBatch() accepted ticket invalidated by Hub replacement")
	}
}

func TestManagerQuotaObservationBatchDisabledModelClearsQuotaWithoutResume(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	authID := "quota-disabled-model-recovery"
	disabledModel := "disabled-quota-model"
	unrelatedModel := "later-unrelated-model"
	next := time.Now().Add(time.Hour)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: authID, Provider: "opencode-go", Status: StatusActive,
		ModelStates: map[string]*ModelState{
			disabledModel: {
				Status: StatusDisabled, Unavailable: true, StatusMessage: "runtime quota",
				NextRetryAfter: next,
				LastError:      &Error{Message: "runtime quota", HTTPStatus: http.StatusTooManyRequests},
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next},
			},
		},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "opencode-go", []*registry.ModelInfo{{ID: disabledModel}, {ID: unrelatedModel}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	reg.SetModelQuotaExceeded(authID, disabledModel)
	reg.SuspendClientModel(authID, disabledModel, "disabled")

	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: unrelatedModel, Success: false,
		Error: &Error{Message: "later unauthorized", HTTPStatus: http.StatusUnauthorized},
	})
	applyAuthoritativeHealthyObservation(t, manager, authID)

	auth, _ := manager.GetByID(authID)
	disabledState := auth.ModelStates[disabledModel]
	if disabledState == nil || disabledState.Status != StatusDisabled || quotaStateIsSet(disabledState.Quota) ||
		disabledState.LastError != nil || disabledState.Unavailable || !disabledState.NextRetryAfter.IsZero() {
		t.Fatalf("disabled model after recovery = %+v, want disabled with quota fields cleared", disabledState)
	}
	unrelatedState := auth.ModelStates[unrelatedModel]
	if unrelatedState == nil || unrelatedState.LastError == nil || unrelatedState.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("later unrelated model state = %+v, want preserved", unrelatedState)
	}
	if quotaStateIsSet(auth.Quota) || auth.LastError == nil || auth.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("auth aggregate after disabled model recovery = %+v", auth)
	}
	if count := reg.GetModelCount(disabledModel); count != 0 {
		t.Fatalf("disabled model registry count = %d, want still suspended", count)
	}
}

func TestManagerQuotaObservationBatchRestartPreservesHubOwnership(t *testing.T) {
	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.cooldownStore = store
	authID := "quota-hub-restart-ownership"
	model := "restart-unauthorized-model"
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	applyAuthWideQuotaObservationExhaustion(t, manager, authID, time.Now().Add(time.Hour))
	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: model, Success: false,
		Error: &Error{Message: "model unauthorized", HTTPStatus: http.StatusUnauthorized},
	})
	beforeRestart, _ := manager.GetByID(authID)
	if beforeRestart.LastError == nil || beforeRestart.LastError.Code != quotaObservationErrorCode {
		t.Fatalf("auth overlay owner before restart = %+v, want Hub", beforeRestart)
	}
	if state := beforeRestart.ModelStates[model]; state == nil || state.LastError == nil || state.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("model state before restart = %+v, want unauthorized", state)
	}

	store.mu.Lock()
	loadRecords := cloneCooldownStateRecords(store.records)
	store.mu.Unlock()
	restoredStore := &recordingCooldownStateStore{load: loadRecords}
	restored := NewManager(nil, nil, nil)
	restored.cooldownStore = restoredStore
	if _, errRegister := restored.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("restored Register() error = %v", errRegister)
	}
	if errRestore := restored.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() error = %v", errRestore)
	}
	afterRestart, _ := restored.GetByID(authID)
	if afterRestart.Quota.Reason != quotaObservationErrorCode || afterRestart.LastError == nil || afterRestart.LastError.Code != quotaObservationErrorCode {
		t.Fatalf("restored auth overlay = %+v, want Hub ownership", afterRestart)
	}
	if state := afterRestart.ModelStates[model]; state == nil || state.LastError == nil || state.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("restored model state = %+v, want unauthorized", state)
	}

	applyAuthoritativeHealthyObservation(t, restored, authID)
	recovered, _ := restored.GetByID(authID)
	if quotaStateIsSet(recovered.Quota) || recovered.LastError == nil || recovered.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("recovered restored state = %+v, want model unauthorized without Hub blocker", recovered)
	}
}

func TestManagerQuotaObservationBatchModelFailureBeforeHubDoesNotBecomeAuthBlocker(t *testing.T) {
	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.cooldownStore = store
	authID := "quota-model-before-hub"
	model := "model-before-hub"
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	manager.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "opencode-go", Model: model, Success: false,
		Error: &Error{Message: "model unauthorized before Hub", HTTPStatus: http.StatusUnauthorized},
	})
	applyAuthWideQuotaObservationExhaustion(t, manager, authID, time.Now().Add(time.Hour))
	auth, _ := manager.GetByID(authID)
	if auth.LastError == nil || auth.LastError.Code != quotaObservationErrorCode || auth.StatusMessage != "quota exhausted" {
		t.Fatalf("Hub did not take ownership of auth overlay = %+v", auth)
	}
	if state := auth.ModelStates[model]; state == nil || state.LastError == nil || state.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("model diagnostic = %+v, want preserved separately", state)
	}

	store.mu.Lock()
	authOnlyRecords := make([]CooldownStateRecord, 0, len(store.records))
	for _, record := range store.records {
		if record.Model == "" {
			authOnlyRecords = append(authOnlyRecords, record)
		}
	}
	store.mu.Unlock()
	if len(authOnlyRecords) != 1 || authOnlyRecords[0].LastError == nil || authOnlyRecords[0].LastError.Code != quotaObservationErrorCode {
		t.Fatalf("persisted auth records = %+v, want one Hub-owned record", authOnlyRecords)
	}

	restoredStore := &recordingCooldownStateStore{load: cloneCooldownStateRecords(authOnlyRecords)}
	restored := NewManager(nil, nil, nil)
	restored.cooldownStore = restoredStore
	if _, errRegister := restored.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("restored Register() error = %v", errRegister)
	}
	if errRestore := restored.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() error = %v", errRestore)
	}
	applyAuthoritativeHealthyObservation(t, restored, authID)
	recovered, _ := restored.GetByID(authID)
	if recovered.Unavailable || quotaStateIsSet(recovered.Quota) || recovered.LastError != nil || recovered.Status != StatusActive {
		t.Fatalf("missing model record became auth-scoped blocker after recovery = %+v", recovered)
	}
}

func TestManagerQuotaObservationBatchBlockingSaveRace(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-observation-save-race")
	authID := "quota-observation-save-race"
	store := &quotaCommitBlockingCooldownStore{
		manager: manager,
		authID:  authID,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(store.unblock)
	manager.cooldownStore = store

	ticket, _ := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	exhaustionDone := make(chan bool, 1)
	go func() {
		exhaustionDone <- manager.ApplyQuotaObservationBatch(context.Background(), QuotaObservationBatch{
			Ticket:       ticket,
			Completeness: QuotaObservationCompletenessExhaustionEvidence,
			Mutations: []QuotaObservationMutation{{
				Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: time.Now().Add(time.Hour),
			}},
		})
	}()
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("cooldown save did not block")
	}
	newerTicket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if !ok {
		t.Fatal("IssueQuotaObservationTicket() blocked behind cooldown save")
	}
	if !manager.ApplyQuotaObservationBatch(context.Background(), quotaScoreOnlyBatch(newerTicket, 55)) {
		t.Fatal("newer score-only batch blocked behind cooldown save")
	}
	if store.saveObservedCommitLock.Load() {
		t.Fatal("cooldown save observed same-auth commit lock held")
	}
	store.unblock()
	select {
	case accepted := <-exhaustionDone:
		if !accepted {
			t.Fatal("exhaustion batch was rejected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exhaustion batch did not finish after save unblocked")
	}
}

func TestManagerQuotaObservationBatchConcurrentApplyRace(t *testing.T) {
	manager := newQuotaObservationTestManager(t, "quota-observation-concurrent")
	ticket, _ := manager.IssueQuotaObservationTicket("quota-observation-concurrent", "opencode-go")
	batch := quotaScoreOnlyBatch(ticket, 44)
	const workers = 32
	var accepted atomic.Int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			if manager.ApplyQuotaObservationBatch(context.Background(), batch) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("accepted concurrent batches = %d, want 1", got)
	}
}

func newQuotaObservationTestManager(t *testing.T, authID string) *Manager {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "opencode-go", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return manager
}

func quotaScoreOnlyBatch(ticket QuotaObservationTicket, score float64) QuotaObservationBatch {
	return QuotaObservationBatch{
		Ticket:       ticket,
		Score:        quotaScorePtr(score),
		Completeness: QuotaObservationCompletenessScoreOnly,
	}
}

func applyAuthoritativeHealthyObservation(t *testing.T, manager *Manager, authID string) {
	t.Helper()
	ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if !ok {
		t.Fatal("IssueQuotaObservationTicket() rejected valid identity")
	}
	if !manager.ApplyQuotaObservationBatch(context.Background(), QuotaObservationBatch{
		Ticket:       ticket,
		Completeness: QuotaObservationCompletenessAuthoritativeSnapshot,
		Mutations:    []QuotaObservationMutation{{Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeHealthy}},
	}) {
		t.Fatal("ApplyQuotaObservationBatch() rejected authoritative recovery")
	}
}

func applyAuthWideQuotaObservationExhaustion(t *testing.T, manager *Manager, authID string, resetAt time.Time) {
	t.Helper()
	ticket, ok := manager.IssueQuotaObservationTicket(authID, "opencode-go")
	if !ok {
		t.Fatal("IssueQuotaObservationTicket() rejected valid identity")
	}
	if !manager.ApplyQuotaObservationBatch(context.Background(), QuotaObservationBatch{
		Ticket:       ticket,
		Completeness: QuotaObservationCompletenessExhaustionEvidence,
		Mutations: []QuotaObservationMutation{{
			Scope: QuotaObservationScopeAuth, Outcome: QuotaObservationOutcomeExhausted, ResetAt: resetAt,
		}},
	}) {
		t.Fatal("ApplyQuotaObservationBatch() rejected auth-wide exhaustion")
	}
}

func setQuotaObservationTestDisableCooling(t *testing.T, manager *Manager, authID string, disabled bool) {
	t.Helper()
	manager.mu.Lock()
	auth := manager.auths[authID]
	if auth == nil {
		manager.mu.Unlock()
		t.Fatalf("auth %q missing", authID)
	}
	if disabled {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["disable_cooling"] = true
	} else {
		delete(auth.Metadata, "disable_cooling")
	}
	manager.mu.Unlock()
}

func quotaScorePtr(score float64) *float64 {
	return &score
}

func assertQuotaObservationNoScore(t *testing.T, manager *Manager, authID string) {
	t.Helper()
	if _, ok := manager.QuotaScore(authID); ok {
		t.Fatal("rejected batch changed quota score")
	}
}
