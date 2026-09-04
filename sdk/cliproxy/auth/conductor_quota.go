package auth

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

const quotaPollerErrorCode = "opencode_go_quota"
const quotaObservationErrorCode = "quota_hub"
const quotaScoreSelectionFloor = 1.0
const quotaScoreMax = 100.0

// QuotaRefreshCallback asks a provider-owned runtime to refresh quota for authID.
type QuotaRefreshCallback func(authID string)

// QuotaResultUpdate carries a provider runtime quota threshold update.
type QuotaResultUpdate struct {
	AuthID            string
	Provider          string
	Score             float64
	ThresholdExceeded bool
	RetryAfter        *time.Duration
}

// QuotaObservationTicket identifies the auth-wide quota state visible when an
// observation starts. Callers issue the ticket before starting network I/O.
type QuotaObservationTicket struct {
	AuthID     string
	Provider   string
	Generation uint64
	Revision   uint64
	StartOrder uint64
}

// QuotaObservationScope identifies the state owned by one observation mutation.
type QuotaObservationScope uint8

const (
	QuotaObservationScopeAuth QuotaObservationScope = iota + 1
	QuotaObservationScopeModel
)

// QuotaObservationCompleteness describes how much authority an observation has.
type QuotaObservationCompleteness uint8

const (
	QuotaObservationCompletenessScoreOnly QuotaObservationCompleteness = iota + 1
	QuotaObservationCompletenessExhaustionEvidence
	QuotaObservationCompletenessAuthoritativeSnapshot
)

// QuotaObservationOutcome is the transport-neutral quota state in a mutation.
type QuotaObservationOutcome uint8

const (
	QuotaObservationOutcomeHealthy QuotaObservationOutcome = iota + 1
	QuotaObservationOutcomeExhausted
)

// QuotaObservationMutation carries one validated quota state observation.
type QuotaObservationMutation struct {
	Scope   QuotaObservationScope
	Model   string
	Outcome QuotaObservationOutcome
	ResetAt time.Time
}

// QuotaObservationBatch atomically applies score and auth-wide quota evidence.
type QuotaObservationBatch struct {
	Ticket       QuotaObservationTicket
	Score        *float64
	Completeness QuotaObservationCompleteness
	Mutations    []QuotaObservationMutation
}

type quotaCommitState struct {
	commitMu                      sync.Mutex
	generation                    uint64
	quotaRevision                 uint64
	nextStartOrder                uint64
	lastAppliedGeneration         uint64
	lastAppliedQuotaRevision      uint64
	lastAppliedAuthWideStartOrder uint64
}

func newQuotaCommitState() *quotaCommitState {
	return &quotaCommitState{generation: 1}
}

func (m *Manager) quotaCommitState(authID string) *quotaCommitState {
	candidate := newQuotaCommitState()
	state, _ := m.quotaCommitStates.LoadOrStore(authID, candidate)
	return state.(*quotaCommitState)
}

// IssueQuotaObservationTicket snapshots the stable auth-wide causal identity
// for a quota observation before its network request begins.
func (m *Manager) IssueQuotaObservationTicket(authID, provider string) (QuotaObservationTicket, bool) {
	if m == nil {
		return QuotaObservationTicket{}, false
	}
	authID = strings.TrimSpace(authID)
	provider = strings.TrimSpace(provider)
	if authID == "" || provider == "" {
		return QuotaObservationTicket{}, false
	}

	state := m.quotaCommitState(authID)
	state.commitMu.Lock()
	defer state.commitMu.Unlock()
	state.nextStartOrder++
	return QuotaObservationTicket{
		AuthID:     authID,
		Provider:   provider,
		Generation: state.generation,
		Revision:   state.quotaRevision,
		StartOrder: state.nextStartOrder,
	}, true
}

// IssueQuotaObservationTicketForAuth snapshots quota state only when resolved
// still identifies the enabled auth generation currently published by Manager.
func (m *Manager) IssueQuotaObservationTicketForAuth(resolved *Auth) (QuotaObservationTicket, bool) {
	if m == nil || resolved == nil {
		return QuotaObservationTicket{}, false
	}
	authID := resolved.ID
	provider := strings.TrimSpace(resolved.Provider)
	generation := resolved.quotaGeneration
	if strings.TrimSpace(authID) == "" || provider == "" || generation == 0 {
		return QuotaObservationTicket{}, false
	}

	state := m.quotaCommitState(authID)
	state.commitMu.Lock()
	defer state.commitMu.Unlock()

	m.mu.RLock()
	current := m.auths[authID]
	valid := current != nil &&
		current.ID == authID &&
		!current.Disabled && current.Status != StatusDisabled &&
		strings.EqualFold(strings.TrimSpace(current.Provider), provider) &&
		current.quotaGeneration == generation &&
		state.generation == generation
	if valid {
		state.nextStartOrder++
	}
	m.mu.RUnlock()
	if !valid {
		return QuotaObservationTicket{}, false
	}

	return QuotaObservationTicket{
		AuthID:     authID,
		Provider:   provider,
		Generation: generation,
		Revision:   state.quotaRevision,
		StartOrder: state.nextStartOrder,
	}, true
}

// ApplyQuotaObservationBatch applies one causally current auth-wide observation.
// Invalid or stale batches are rejected without changing any manager state.
func (m *Manager) ApplyQuotaObservationBatch(ctx context.Context, batch QuotaObservationBatch) bool {
	validated, ok := validateQuotaObservationBatch(batch, time.Now())
	if m == nil || !ok {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	commitState := m.quotaCommitState(validated.ticket.AuthID)
	commitState.commitMu.Lock()

	var authSnapshot *Auth
	var scoreSnapshot *float64
	modelsToClear := make([]string, 0)
	modelsToResume := make([]string, 0)
	cooldownStateChanged := false

	m.mu.Lock()
	auth := m.auths[validated.ticket.AuthID]
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), validated.ticket.Provider) ||
		commitState.generation != validated.ticket.Generation ||
		commitState.quotaRevision != validated.ticket.Revision ||
		validated.ticket.StartOrder > commitState.nextStartOrder ||
		(commitState.lastAppliedGeneration == validated.ticket.Generation &&
			commitState.lastAppliedQuotaRevision == validated.ticket.Revision &&
			validated.ticket.StartOrder <= commitState.lastAppliedAuthWideStartOrder) {
		m.mu.Unlock()
		commitState.commitMu.Unlock()
		return false
	}

	now := time.Now()
	applyCooldownMutation := !auth.Disabled && auth.Status != StatusDisabled && !m.cooldownDisabledForAuth(auth)
	if applyCooldownMutation && validated.exhausted && !validated.latestReset.After(now) {
		m.mu.Unlock()
		commitState.commitMu.Unlock()
		return false
	}
	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
	}

	if validated.score != nil {
		if m.quotaScores == nil {
			m.quotaScores = make(map[string]float64)
		}
		m.quotaScores[validated.ticket.AuthID] = *validated.score
		score := *validated.score
		scoreSnapshot = &score
	}

	if applyCooldownMutation {
		switch validated.completeness {
		case QuotaObservationCompletenessExhaustionEvidence:
			applyQuotaObservationExhaustion(auth, validated.latestReset, now)
		case QuotaObservationCompletenessAuthoritativeSnapshot:
			if validated.exhausted {
				applyQuotaObservationExhaustion(auth, validated.latestReset, now)
			} else {
				modelsToClear, modelsToResume = clearQuotaObservationState(auth, now)
			}
		}
	}

	commitState.lastAppliedGeneration = validated.ticket.Generation
	commitState.lastAppliedQuotaRevision = validated.ticket.Revision
	commitState.lastAppliedAuthWideStartOrder = validated.ticket.StartOrder
	authSnapshot = auth.Clone()
	if trackCooldownState {
		cooldownRecordsAfter := m.cooldownStateRecordsForAuthLocked(auth, now)
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, cooldownRecordsAfter)
	}
	m.mu.Unlock()

	m.syncQuotaObservationToScheduler(authSnapshot, scoreSnapshot)
	for _, model := range modelsToClear {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(validated.ticket.AuthID, model)
	}
	for _, model := range modelsToResume {
		registry.GetGlobalRegistry().ResumeClientModel(validated.ticket.AuthID, model)
	}
	commitState.commitMu.Unlock()

	if cooldownStateChanged {
		m.persistCooldownStates(ctx)
	}
	return true
}

type validatedQuotaObservationBatch struct {
	ticket       QuotaObservationTicket
	score        *float64
	completeness QuotaObservationCompleteness
	exhausted    bool
	latestReset  time.Time
}

func validateQuotaObservationBatch(batch QuotaObservationBatch, now time.Time) (validatedQuotaObservationBatch, bool) {
	ticket := batch.Ticket
	ticket.Provider = strings.TrimSpace(ticket.Provider)
	if strings.TrimSpace(ticket.AuthID) == "" || ticket.Provider == "" || ticket.Generation == 0 || ticket.StartOrder == 0 {
		return validatedQuotaObservationBatch{}, false
	}

	var score *float64
	if batch.Score != nil {
		if math.IsNaN(*batch.Score) || math.IsInf(*batch.Score, 0) || *batch.Score < 0 || *batch.Score > quotaScoreMax {
			return validatedQuotaObservationBatch{}, false
		}
		value := *batch.Score
		score = &value
	}

	if batch.Completeness == QuotaObservationCompletenessScoreOnly {
		if score == nil || len(batch.Mutations) != 0 {
			return validatedQuotaObservationBatch{}, false
		}
		return validatedQuotaObservationBatch{ticket: ticket, score: score, completeness: batch.Completeness}, true
	}
	if batch.Completeness != QuotaObservationCompletenessExhaustionEvidence &&
		batch.Completeness != QuotaObservationCompletenessAuthoritativeSnapshot {
		return validatedQuotaObservationBatch{}, false
	}
	if len(batch.Mutations) == 0 {
		return validatedQuotaObservationBatch{}, false
	}

	exhausted := false
	latestReset := time.Time{}
	for _, mutation := range batch.Mutations {
		if mutation.Scope != QuotaObservationScopeAuth || strings.TrimSpace(mutation.Model) != "" {
			return validatedQuotaObservationBatch{}, false
		}
		switch mutation.Outcome {
		case QuotaObservationOutcomeHealthy:
			if batch.Completeness == QuotaObservationCompletenessExhaustionEvidence || !mutation.ResetAt.IsZero() {
				return validatedQuotaObservationBatch{}, false
			}
		case QuotaObservationOutcomeExhausted:
			if mutation.ResetAt.IsZero() || !mutation.ResetAt.After(now) {
				return validatedQuotaObservationBatch{}, false
			}
			exhausted = true
			if mutation.ResetAt.After(latestReset) {
				latestReset = mutation.ResetAt
			}
		default:
			return validatedQuotaObservationBatch{}, false
		}
	}

	return validatedQuotaObservationBatch{
		ticket:       ticket,
		score:        score,
		completeness: batch.Completeness,
		exhausted:    exhausted,
		latestReset:  latestReset,
	}, true
}

func applyQuotaObservationExhaustion(auth *Auth, resetAt, now time.Time) {
	if auth == nil {
		return
	}
	existingQuota := auth.Quota
	if existingQuota.Reason == quotaObservationErrorCode && existingQuota.NextRecoverAt.After(resetAt) {
		resetAt = auth.Quota.NextRecoverAt
	}
	unrelatedState := authHasUnrelatedQuotaObservationState(auth)
	backoffLevel := 0
	if unrelatedState && isCloudflareChallengeResultError(auth.LastError) {
		backoffLevel = existingQuota.BackoffLevel
	}
	auth.Quota = QuotaState{
		Exceeded:      true,
		Reason:        quotaObservationErrorCode,
		NextRecoverAt: resetAt,
		BackoffLevel:  backoffLevel,
	}
	auth.UpdatedAt = now
	if unrelatedState {
		return
	}
	auth.Unavailable = true
	auth.Status = StatusError
	auth.StatusMessage = "quota exhausted"
	auth.NextRetryAfter = resetAt
	auth.LastError = &Error{
		Code:       quotaObservationErrorCode,
		Message:    "quota exhausted",
		Retryable:  true,
		HTTPStatus: http.StatusTooManyRequests,
	}
}

func authHasUnrelatedQuotaObservationState(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.LastError != nil {
		if quotaObservationAuthErrorIsModelDerived(auth) {
			return false
		}
		return auth.LastError.Code != quotaObservationErrorCode && !isQuotaOnlyObservationState(auth.Quota, auth.LastError)
	}
	return auth.Status == StatusError && strings.TrimSpace(auth.StatusMessage) != "" && auth.Quota.Reason != quotaObservationErrorCode
}

func clearQuotaObservationState(auth *Auth, now time.Time) ([]string, []string) {
	if auth == nil {
		return nil, nil
	}
	preservedAuthState := captureUnrelatedAuthQuotaObservationState(auth)
	modelsToClear := make([]string, 0)
	modelsToResume := make([]string, 0)
	for model, state := range auth.ModelStates {
		if !isQuotaOnlyObservationState(state.Quota, state.LastError) {
			continue
		}
		modelKey := canonicalModelKey(model)
		modelsToClear = append(modelsToClear, modelKey)
		if state.Status == StatusDisabled {
			clearDisabledModelQuotaObservationState(state, now)
			continue
		}
		resetModelState(state, now)
		modelsToResume = append(modelsToResume, modelKey)
	}

	authQuotaOwned := auth.Quota.Reason == quotaObservationErrorCode || isQuotaOnlyObservationState(auth.Quota, auth.LastError)
	authErrorOwned := auth.LastError != nil && (auth.LastError.Code == quotaObservationErrorCode || isQuotaOnlyObservationState(auth.Quota, auth.LastError))
	if authQuotaOwned {
		auth.Quota = QuotaState{}
	}
	if authErrorOwned {
		auth.Unavailable = false
		auth.Status = StatusActive
		auth.StatusMessage = ""
		auth.NextRetryAfter = time.Time{}
		auth.LastError = nil
	}
	if authQuotaOwned || authErrorOwned || len(modelsToClear) > 0 {
		updateAggregatedAvailability(auth, now)
		preservedAuthState.restore(auth)
	}
	if authErrorOwned {
		preserveQuotaObservationModelError(auth)
	}
	if authQuotaOwned || authErrorOwned || len(modelsToClear) > 0 {
		auth.UpdatedAt = now
	}
	return dedupeStrings(modelsToClear), dedupeStrings(modelsToResume)
}

func clearDisabledModelQuotaObservationState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	quotaMessage := ""
	if state.LastError != nil {
		quotaMessage = state.LastError.Message
	}
	state.Unavailable = false
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	state.Quota = QuotaState{}
	if state.StatusMessage == quotaMessage {
		state.StatusMessage = ""
	}
	state.UpdatedAt = now
}

type unrelatedAuthQuotaObservationState struct {
	preserve       bool
	unavailable    bool
	nextRetryAfter time.Time
	quota          QuotaState
	preserveQuota  bool
}

func captureUnrelatedAuthQuotaObservationState(auth *Auth) unrelatedAuthQuotaObservationState {
	if auth == nil || auth.LastError == nil || auth.LastError.Code == quotaObservationErrorCode ||
		isQuotaOnlyObservationState(auth.Quota, auth.LastError) || quotaObservationAuthErrorIsModelDerived(auth) {
		return unrelatedAuthQuotaObservationState{}
	}
	preservedQuota := auth.Quota
	preserveQuota := isCloudflareChallengeResultError(auth.LastError)
	if preserveQuota && preservedQuota.Reason == quotaObservationErrorCode {
		preservedQuota = QuotaState{
			Exceeded:      true,
			Reason:        "cloudflare challenge",
			NextRecoverAt: auth.NextRetryAfter,
			BackoffLevel:  auth.Quota.BackoffLevel,
		}
	}
	return unrelatedAuthQuotaObservationState{
		preserve:       true,
		unavailable:    auth.Unavailable,
		nextRetryAfter: auth.NextRetryAfter,
		quota:          preservedQuota,
		preserveQuota:  preserveQuota,
	}
}

func (state unrelatedAuthQuotaObservationState) restore(auth *Auth) {
	if !state.preserve || auth == nil {
		return
	}
	auth.Unavailable = state.unavailable
	auth.NextRetryAfter = state.nextRetryAfter
	if state.preserveQuota {
		auth.Quota = state.quota
	}
}

func quotaObservationAuthErrorIsModelDerived(auth *Auth) bool {
	if auth == nil || auth.LastError == nil {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil || state.LastError == nil {
			continue
		}
		if state.LastError.Code == auth.LastError.Code &&
			state.LastError.Message == auth.LastError.Message &&
			state.LastError.HTTPStatus == auth.LastError.HTTPStatus {
			return true
		}
	}
	return false
}

func preserveQuotaObservationModelError(auth *Auth) {
	if auth == nil {
		return
	}
	for _, state := range auth.ModelStates {
		if state == nil || state.LastError == nil {
			continue
		}
		auth.Status = StatusError
		auth.StatusMessage = state.StatusMessage
		auth.LastError = cloneError(state.LastError)
		return
	}
}

func isQuotaOnlyObservationState(quota QuotaState, lastErr *Error) bool {
	if !quotaStateIsSet(quota) || lastErr == nil || statusCodeFromResult(lastErr) != http.StatusTooManyRequests {
		return false
	}
	if lastErr.Code == requestScopedErrorCode || isCloudflareChallengeResultError(lastErr) {
		return false
	}
	return quota.Reason == "quota" || quota.Reason == quotaPollerErrorCode || quota.Reason == quotaObservationErrorCode
}

func (m *Manager) syncQuotaObservationToScheduler(auth *Auth, score *float64) {
	if m == nil || m.scheduler == nil || auth == nil {
		return
	}
	scheduler := m.scheduler
	scheduler.mu.Lock()
	if score != nil {
		if scheduler.quotaScores == nil {
			scheduler.quotaScores = make(map[string]float64)
		}
		scheduler.quotaScores[auth.ID] = *score
	}
	scheduler.upsertAuthLocked(auth, time.Now())
	scheduler.mu.Unlock()
}

// SetQuotaRefreshCallback replaces the callback used for immediate quota refreshes.
func (m *Manager) SetQuotaRefreshCallback(callback QuotaRefreshCallback) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.quotaRefreshCallback = callback
	m.mu.Unlock()
}

// SetQuotaScore records the latest quota score for authID.
func (m *Manager) SetQuotaScore(authID string, score float64) {
	if m == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	score = normalizeQuotaScore(score)
	m.mu.Lock()
	if m.quotaScores == nil {
		m.quotaScores = make(map[string]float64)
	}
	m.quotaScores[authID] = score
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.setQuotaScore(authID, score)
	}
}

// QuotaScore returns the latest synchronized quota score for authID.
func (m *Manager) QuotaScore(authID string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	trimmedID := strings.TrimSpace(authID)
	if trimmedID == "" {
		return 0, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	score, ok := m.quotaScores[authID]
	if !ok && trimmedID != authID {
		score, ok = m.quotaScores[trimmedID]
	}
	return score, ok
}

func normalizeQuotaScore(score float64) float64 {
	if math.IsNaN(score) || math.IsInf(score, 0) || score <= 0 {
		return 0
	}
	if score > quotaScoreMax {
		return quotaScoreMax
	}
	return score
}

func quotaScoreSelectionWeight(score float64, ok bool) (float64, bool) {
	if !ok {
		return quotaScoreSelectionFloor, false
	}
	score = normalizeQuotaScore(score)
	if score <= 0 {
		return quotaScoreSelectionFloor, false
	}
	if score < quotaScoreSelectionFloor {
		return quotaScoreSelectionFloor, true
	}
	return score, true
}

// ApplyQuotaResult updates quota score and applies threshold state transitions
// through MarkResult so existing cooldown persistence remains authoritative.
func (m *Manager) ApplyQuotaResult(ctx context.Context, update QuotaResultUpdate) {
	if m == nil {
		return
	}
	update.AuthID = strings.TrimSpace(update.AuthID)
	if update.AuthID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	if strings.TrimSpace(update.Provider) == "" {
		update.Provider = "opencode-go"
	}

	m.SetQuotaScore(update.AuthID, update.Score)
	models := modelsForRegisteredAuth(update.AuthID)
	if !m.setQuotaThresholdState(update.AuthID, update.ThresholdExceeded) {
		if update.ThresholdExceeded && !m.quotaThresholdStateApplied(update.AuthID, models) {
			// Re-apply provider quota when a later upstream error overwrote the model state.
		} else {
			return
		}
	}

	if len(models) == 0 {
		m.markQuotaResultForModel(ctx, update, "")
		return
	}
	for _, model := range models {
		if ctx.Err() != nil {
			return
		}
		m.markQuotaResultForModel(ctx, update, model)
	}
}

func (m *Manager) quotaThresholdStateApplied(authID string, models []string) bool {
	if m == nil || strings.TrimSpace(authID) == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth := m.auths[authID]
	if auth == nil {
		return false
	}
	if len(models) == 0 {
		return auth.LastError != nil && auth.LastError.Code == quotaPollerErrorCode && auth.Quota.Exceeded
	}
	for _, model := range models {
		modelKey := strings.TrimSpace(model)
		if modelKey == "" {
			continue
		}
		state := auth.ModelStates[modelKey]
		if state == nil {
			state = auth.ModelStates[canonicalModelKey(modelKey)]
		}
		if state == nil || state.LastError == nil || state.LastError.Code != quotaPollerErrorCode || !state.Quota.Exceeded {
			return false
		}
	}
	return true
}

func (m *Manager) setQuotaThresholdState(authID string, exceeded bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.quotaThresholdStates == nil {
		m.quotaThresholdStates = make(map[string]bool)
	}
	current, ok := m.quotaThresholdStates[authID]
	if !ok {
		m.quotaThresholdStates[authID] = exceeded
		return exceeded
	}
	if current == exceeded {
		return false
	}
	m.quotaThresholdStates[authID] = exceeded
	return true
}

func (m *Manager) markQuotaResultForModel(ctx context.Context, update QuotaResultUpdate, model string) {
	if update.ThresholdExceeded {
		m.MarkResult(ctx, Result{
			AuthID:     update.AuthID,
			Provider:   update.Provider,
			Model:      model,
			Success:    false,
			RetryAfter: update.RetryAfter,
			Error: &Error{
				Code:       quotaPollerErrorCode,
				Message:    "quota exhausted",
				HTTPStatus: http.StatusTooManyRequests,
			},
		})
		return
	}
	m.MarkResult(ctx, Result{
		AuthID:   update.AuthID,
		Provider: update.Provider,
		Model:    model,
		Success:  true,
	})
}

func (m *Manager) triggerQuotaRefresh(ctx context.Context, result Result) {
	if m == nil || result.Success || result.AuthID == "" || statusCodeFromResult(result.Error) != http.StatusTooManyRequests {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(result.Provider), "opencode-go") {
		return
	}
	if result.Error != nil && result.Error.Code == quotaPollerErrorCode {
		return
	}
	m.mu.RLock()
	callback := m.quotaRefreshCallback
	m.mu.RUnlock()
	if callback == nil {
		return
	}
	callback(result.AuthID)
}
