package auth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// SetRetryConfig updates additional credential retry rounds, the per-round credential limit, and the cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration, maxRetryCredentials int) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryCredentials < 0 {
		maxRetryCredentials = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryCredentials.Store(int32(maxRetryCredentials))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	provider := strings.TrimSpace(executor.Identifier())
	if provider == "" {
		return
	}

	var replaced ProviderExecutor
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	m.mu.Unlock()

	if replaced == nil || replaced == executor {
		return
	}
	if closer, ok := replaced.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("register auth: %w", errWeight)
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	now := time.Now()
	if auth.Generation == 0 {
		auth.Generation = 1
	}
	if auth.CreatedAt.IsZero() {
		auth.CreatedAt = now
	}
	auth.UpdatedAt = now
	cooldownStateChanged := normalizeModelStates(auth)
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	commitState := m.quotaCommitState(auth.ID)
	commitState.commitMu.Lock()
	m.mu.Lock()
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing, exists := m.auths[auth.ID]; exists && existing != nil && existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if auth.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = auth.RegistrationEpoch
	}
	m.authEpochs[auth.ID]++
	auth.RegistrationEpoch = m.authEpochs[auth.ID]
	auth.Generation = 1
	authClone := auth.Clone()
	advanceAuthGenerationLocked(commitState)
	authClone.quotaGeneration = commitState.generation
	resolvedAuth := authClone.Clone()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone.Clone())
	}
	commitState.commitMu.Unlock()
	m.queueRefreshReschedule(auth.ID)
	_ = m.persist(ctx, resolvedAuth)
	m.hook.OnAuthRegistered(ctx, resolvedAuth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return resolvedAuth, nil
}

type updateAuthMode int

const (
	updateModeReplace updateAuthMode = iota
	updateModeRefresh
	updateModePrepare
)

// UpdatePreparedAuth atomically merges request preparation results into the latest runtime auth
// under the manager lock, preserving concurrent modifications without modifying refresh lifecycle fields.
func (m *Manager) UpdatePreparedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	return m.updateInternal(ctx, base, updated, updateModePrepare)
}

// UpdateRefreshedAuth atomically merges refresh results into the latest runtime auth
// under the manager lock, preserving concurrent modifications (proxy_url, notes, weights, etc.).
func (m *Manager) UpdateRefreshedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	return m.updateInternal(ctx, base, updated, updateModeRefresh)
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	return m.updateInternal(ctx, nil, auth, updateModeReplace)
}

func (m *Manager) updateInternal(ctx context.Context, base, auth *Auth, mode updateAuthMode) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("update auth: %w", errWeight)
	}
	commitState := m.quotaCommitState(auth.ID)
	commitState.commitMu.Lock()
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		commitState.commitMu.Unlock()
		return nil, nil
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if (mode == updateModeRefresh || mode == updateModePrepare) && base != nil && existing.RegistrationEpoch != base.RegistrationEpoch {
		m.mu.Unlock()
		return nil, fmt.Errorf("update auth %s: stale registration epoch %d != %d", auth.ID, base.RegistrationEpoch, existing.RegistrationEpoch)
	}
	if mode == updateModeRefresh {
		merged := MergeRefreshedAuth(base, existing, auth)
		if merged != nil {
			auth = merged
			NormalizeCredentialMetadata(auth.Metadata)
		}
	} else if mode == updateModePrepare {
		merged := MergePreparedAuth(base, existing, auth)
		if merged != nil {
			auth = merged
			NormalizeCredentialMetadata(auth.Metadata)
		}
	}
	if auth.RegistrationEpoch != 0 && auth.RegistrationEpoch < m.authEpochs[auth.ID] {
		m.mu.Unlock()
		commitState.commitMu.Unlock()
		return nil, fmt.Errorf("update auth %s: stale registration epoch %d < %d", auth.ID, auth.RegistrationEpoch, m.authEpochs[auth.ID])
	}
	if auth.RegistrationEpoch >= m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = auth.RegistrationEpoch
	} else if auth.RegistrationEpoch == 0 {
		auth.RegistrationEpoch = m.authEpochs[auth.ID]
	}
	if !auth.indexAssigned && auth.Index == "" {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	auth.Success = existing.Success
	auth.Failed = existing.Failed
	auth.recentRequests = existing.recentRequests
	if auth.Generation <= existing.Generation {
		auth.Generation = existing.Generation + 1
	} else {
		auth.Generation++
	}
	if !existing.Disabled && existing.Status != StatusDisabled && !auth.Disabled && auth.Status != StatusDisabled {
		if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
			auth.ModelStates = existing.ModelStates
		}
		if existing.Quota.Exceeded && existing.Quota.Reason == "credential_quota" && existing.Quota.NextRecoverAt.After(time.Now()) {
			auth.Unavailable = existing.Unavailable
			auth.NextRetryAfter = existing.NextRetryAfter
			auth.Quota = existing.Quota
			if auth.Status == StatusActive {
				auth.Status = existing.Status
			}
		}
	}
	now := time.Now()
	auth.UpdatedAt = now
	cooldownStateChanged := normalizeModelStates(auth)
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	authClone := auth.Clone()
	advanceAuthGenerationLocked(commitState)
	authClone.quotaGeneration = commitState.generation
	resolvedAuth := authClone.Clone()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone.Clone())
	}
	commitState.commitMu.Unlock()
	m.queueRefreshReschedule(auth.ID)
	_ = m.persist(ctx, resolvedAuth)
	m.hook.OnAuthUpdated(ctx, resolvedAuth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return resolvedAuth, nil
}

// Remove deletes an auth from runtime state without persisting.
// Disk and token-store deletion must be handled by the caller.
func (m *Manager) Remove(ctx context.Context, id string) {
	if m == nil {
		return
	}
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return
	}

	authIDs := []string{trimmedID}
	if id != trimmedID {
		authIDs = append([]string{id}, authIDs...)
	}
	for _, authID := range authIDs {
		commitState := m.quotaCommitState(authID)
		commitState.commitMu.Lock()
		m.mu.Lock()
		existing := m.auths[authID]
		if existing == nil {
			m.mu.Unlock()
			commitState.commitMu.Unlock()
			continue
		}
		provider := strings.TrimSpace(existing.Provider)
		advanceAuthGenerationLocked(commitState)
		delete(m.auths, authID)
		if m.modelPoolOffsets != nil {
			delete(m.modelPoolOffsets, authID)
		}
		for sessionID, sessionAuths := range m.homeRuntimeAuths {
			if sessionAuths == nil {
				continue
			}
			delete(sessionAuths, authID)
			if len(sessionAuths) == 0 {
				delete(m.homeRuntimeAuths, sessionID)
			}
		}
		if m.authEpochs == nil {
			m.authEpochs = make(map[string]uint64)
		}
		if existing.RegistrationEpoch > m.authEpochs[authID] {
			m.authEpochs[authID] = existing.RegistrationEpoch
		}
		m.authEpochs[authID]++
		tombstoneEpoch := m.authEpochs[authID]
		m.mu.Unlock()

		if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
			m.rebuildAPIKeyModelAliasFromRuntimeConfig()
		}
		if m.scheduler != nil {
			m.scheduler.RecordRemovalTombstone(authID, tombstoneEpoch)
		}
		commitState.commitMu.Unlock()
		m.queueRefreshUnschedule(authID)
		m.invalidateSessionAffinity(authID)

		if provider != "" {
			if exec, ok := m.Executor(provider); ok && exec != nil {
				if closer, okCloser := exec.(ExecutionSessionCloser); okCloser {
					closer.CloseExecutionSession(CloseAllExecutionSessionsID)
				}
			}
		}
		m.persistCooldownStates(context.Background())
		return
	}
}

func (m *Manager) invalidateSessionAffinity(authID string) {
	if m == nil || authID == "" {
		return
	}
	sel := m.Selector()
	if invalidator, ok := sel.(interface{ InvalidateAuth(string) }); ok && invalidator != nil {
		invalidator.InvalidateAuth(authID)
	}
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.RLock()
	if m.store == nil {
		m.mu.RUnlock()
		return nil
	}
	store := m.store
	m.mu.RUnlock()

	items, err := store.List(ctx)
	if err != nil {
		return err
	}
	loadedAuths := make(map[string]*Auth, len(items))
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		NormalizeCredentialMetadata(auth.Metadata)
		if errWeight := ValidateAuthWeight(auth); errWeight != nil {
			continue
		}
		auth.EnsureIndex()
		loadedAuths[auth.ID] = auth.Clone()
	}

	type removalTombstone struct {
		id    string
		epoch uint64
	}

	m.mu.Lock()
	previousAuths := m.auths
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64, len(items))
	}
	for authID, auth := range loadedAuths {
		m.authEpochs[authID] = max(m.authEpochs[authID], auth.RegistrationEpoch) + 1
		auth.RegistrationEpoch = m.authEpochs[authID]
		auth.Generation = 1
	}
	var removedTombstones []removalTombstone
	for prevID := range previousAuths {
		if _, exists := loadedAuths[prevID]; !exists {
			m.authEpochs[prevID]++
			removedTombstones = append(removedTombstones, removalTombstone{
				id:    prevID,
				epoch: m.authEpochs[prevID],
			})
		}
	}
	authIDs := make([]string, 0, len(previousAuths)+len(loadedAuths))
	authIDSet := make(map[string]struct{}, len(previousAuths)+len(loadedAuths))
	for authID := range previousAuths {
		authIDSet[authID] = struct{}{}
	}
	for authID := range loadedAuths {
		authIDSet[authID] = struct{}{}
	}
	for authID := range authIDSet {
		authIDs = append(authIDs, authID)
	}
	sort.Strings(authIDs)
	m.mu.Unlock()

	commitStates := make([]*quotaCommitState, 0, len(authIDs))
	for _, authID := range authIDs {
		commitState := m.quotaCommitState(authID)
		commitState.commitMu.Lock()
		advanceAuthGenerationLocked(commitState)
		if loadedAuth := loadedAuths[authID]; loadedAuth != nil {
			loadedAuth.quotaGeneration = commitState.generation
		}
		commitStates = append(commitStates, commitState)
	}

	m.mu.Lock()
	m.auths = loadedAuths
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.rebuildAPIKeyModelAliasLocked(cfg)
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, rt := range removedTombstones {
			m.scheduler.RecordRemovalTombstone(rt.id, rt.epoch)
		}
	}
	loadedSnapshot := make([]*Auth, 0, len(loadedAuths))
	for _, auth := range loadedAuths {
		loadedSnapshot = append(loadedSnapshot, auth.Clone())
	}
	m.syncSchedulerFromSnapshot(loadedSnapshot)
	for i := len(commitStates) - 1; i >= 0; i-- {
		commitStates[i].commitMu.Unlock()
	}
	return nil
}

type authPersistLock struct {
	mu             sync.Mutex
	lastEpoch      uint64
	lastGeneration uint64
	lastSkip       bool
}

func authPersistWatermarkOlder(epoch, generation, lastEpoch, lastGeneration uint64) bool {
	return epoch < lastEpoch || (epoch == lastEpoch && generation < lastGeneration)
}

func (l *authPersistLock) advance(epoch, generation uint64, skip bool) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if authPersistWatermarkOlder(epoch, generation, l.lastEpoch, l.lastGeneration) {
		return false
	}
	l.lastEpoch = epoch
	l.lastGeneration = generation
	l.lastSkip = skip
	return true
}

func (l *authPersistLock) hasNewerPersistent(epoch, generation uint64) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.lastSkip && authPersistWatermarkOlder(epoch, generation, l.lastEpoch, l.lastGeneration)
}

func advanceAuthGenerationLocked(state *quotaCommitState) {
	state.generation++
	if state.generation == 0 {
		state.generation = 1
	}
	state.quotaRevision = 0
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil {
		return nil
	}
	persistable, errPersistable := m.authShouldPersist(auth)
	if errPersistable != nil {
		return errPersistable
	}
	if !persistable {
		return nil
	}
	epoch := auth.RegistrationEpoch
	generation := auth.Generation
	pLock := m.authPersistLock(auth.ID)
	skip := shouldSkipPersist(ctx)
	if !pLock.advance(epoch, generation, skip) {
		return nil
	}
	if skip {
		return nil
	}
	// Keep the persistence watermark out of storage I/O so a stalled older save
	// cannot block newer quota commits from publishing runtime recovery.
	_, errSave := m.store.Save(ctx, auth)
	if errSave != nil {
		return errSave
	}
	if !pLock.hasNewerPersistent(epoch, generation) {
		return nil
	}
	latest, errLatest := m.newerPersistableAuthSnapshot(auth.ID, epoch, generation)
	if errLatest != nil || latest == nil {
		return errLatest
	}
	_, errSaveLatest := m.store.Save(ctx, latest)
	return errSaveLatest
}

func (m *Manager) authShouldPersist(auth *Auth) (bool, error) {
	if auth == nil {
		return false, nil
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return false, fmt.Errorf("persist auth: %w", errWeight)
	}
	if IsConfigAPIKeyAuth(auth) {
		return false, nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return false, nil
		}
	}
	if IsPluginVirtualAuth(auth) {
		return false, nil
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return false, nil
	}
	return true, nil
}

func (m *Manager) authPersistLock(authID string) *authPersistLock {
	lockVal, _ := m.persistLocks.LoadOrStore(authID, &authPersistLock{})
	pLock, _ := lockVal.(*authPersistLock)
	if pLock == nil {
		return &authPersistLock{}
	}
	return pLock
}

func (m *Manager) newerPersistableAuthSnapshot(authID string, epoch, generation uint64) (*Auth, error) {
	m.mu.RLock()
	current := m.auths[authID]
	if current == nil || !authPersistWatermarkOlder(epoch, generation, current.RegistrationEpoch, current.Generation) {
		m.mu.RUnlock()
		return nil, nil
	}
	snapshot := current.Clone()
	m.mu.RUnlock()
	persistable, errPersistable := m.authShouldPersist(snapshot)
	if errPersistable != nil || !persistable {
		return nil, errPersistable
	}
	return snapshot, nil
}
