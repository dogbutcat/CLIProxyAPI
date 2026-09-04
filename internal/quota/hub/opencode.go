package hub

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const openCodeProvider = "opencode-go"

const (
	openCodeErrorAdapterUnavailable = "adapter_unavailable"
	openCodeErrorAdapter            = "adapter_error"
	openCodeErrorValidation         = "validation_error"
	openCodeErrorCompletionPanic    = "completion_panic"
)

// OpenCodeQuotaCompletion consumes one retained poller result exactly once and
// reports whether the canonical auth sink accepted it.
type OpenCodeQuotaCompletion func(context.Context, *quota.PollResult) bool

// BeginOpenCodeQuotaObservation captures the current auth generation and quota
// revision before the retained OpenCode poller starts its network request.
func BeginOpenCodeQuotaObservation(
	manager *auth.Manager,
	resolvedAuth *auth.Auth,
	source SourceKind,
	thresholdConfigured bool,
	threshold float64,
) OpenCodeQuotaCompletion {
	return beginOpenCodeQuotaObservationWithAdapter(
		manager,
		resolvedAuth,
		source,
		thresholdConfigured,
		threshold,
		activeOpenCodeResultAdapter(),
	)
}

func beginOpenCodeQuotaObservationWithAdapter(
	manager *auth.Manager,
	resolvedAuth *auth.Auth,
	source SourceKind,
	thresholdConfigured bool,
	threshold float64,
	adapter openCodeResultAdapter,
) OpenCodeQuotaCompletion {
	if manager == nil || resolvedAuth == nil || resolvedAuth.Disabled || resolvedAuth.Status == auth.StatusDisabled ||
		!strings.EqualFold(strings.TrimSpace(resolvedAuth.Provider), openCodeProvider) ||
		(source != OpenCodeManual && source != OpenCodeScheduled) ||
		(thresholdConfigured && !validOpenCodePercentage(threshold)) {
		return nil
	}

	ticket, issued := manager.IssueQuotaObservationTicketForAuth(resolvedAuth)
	if !issued {
		return nil
	}
	attempt := &openCodeQuotaAttempt{
		manager:             manager,
		adapter:             adapter,
		authID:              ticket.AuthID,
		provider:            ticket.Provider,
		ticket:              ticket,
		source:              source,
		thresholdConfigured: thresholdConfigured,
		threshold:           threshold,
	}
	return attempt.complete
}

type openCodeQuotaAttempt struct {
	once                sync.Once
	manager             *auth.Manager
	adapter             openCodeResultAdapter
	authID              string
	provider            string
	ticket              auth.QuotaObservationTicket
	source              SourceKind
	thresholdConfigured bool
	threshold           float64
}

func (attempt *openCodeQuotaAttempt) complete(ctx context.Context, result *quota.PollResult) (accepted bool) {
	defer func() {
		if recover() != nil {
			logOpenCodeFailure(attempt.provider, attempt.source, openCodeErrorCompletionPanic)
			accepted = false
		}
	}()
	run := false
	attempt.once.Do(func() {
		run = true
		accepted = attempt.consume(ctx, result)
	})
	return run && accepted
}

func (attempt *openCodeQuotaAttempt) consume(ctx context.Context, result *quota.PollResult) bool {
	completedAt := time.Now().UTC()
	observation, available, errObserve := attempt.adapter.observeResult(openCodeResultMetadata{
		Source:              attempt.source,
		CompletedAt:         completedAt,
		ThresholdConfigured: attempt.thresholdConfigured,
		Threshold:           attempt.threshold,
	}, result)
	if !available {
		logOpenCodeFailure(attempt.provider, attempt.source, openCodeErrorAdapterUnavailable)
		return false
	}
	if errObserve != nil {
		logOpenCodeFailure(attempt.provider, attempt.source, openCodeErrorAdapter)
		return false
	}

	batch, errBatch := (ObservationBatch{
		AuthID:   attempt.authID,
		Provider: attempt.provider,
		Ticket:   attempt.ticket,
		Metadata: ObservationMetadata{
			Source:      attempt.source,
			CompletedAt: completedAt,
		},
		Observation: observation,
	}).ToAuthBatch()
	if errBatch != nil {
		logOpenCodeFailure(attempt.provider, attempt.source, openCodeErrorValidation)
		return false
	}

	applyContext := context.Background()
	if ctx != nil {
		applyContext = context.WithoutCancel(ctx)
	}
	return attempt.manager.ApplyQuotaObservationBatch(applyContext, batch)
}

func logOpenCodeFailure(provider string, source SourceKind, errorClass string) {
	defer func() {
		_ = recover()
	}()
	log.WithFields(log.Fields{
		"provider":    strings.TrimSpace(provider),
		"source":      uint8(source),
		"error_class": errorClass,
	}).Warn("quota hub OpenCode observation rejected")
}
