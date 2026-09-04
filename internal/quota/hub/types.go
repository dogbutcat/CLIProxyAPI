package hub

import (
	"errors"
	"math"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ScopeKind identifies the quota state affected by an observation.
type ScopeKind uint8

const (
	ScopeAuth ScopeKind = iota + 1
	// ScopeModel is reserved for a separately approved model-scoped design.
	ScopeModel
)

// Valid reports whether scope is part of the canonical vocabulary.
func (scope ScopeKind) Valid() bool {
	return scope == ScopeAuth || scope == ScopeModel
}

// Active reports whether scope may be emitted by a current adapter.
func (scope ScopeKind) Active() bool {
	return scope == ScopeAuth
}

// SourceKind identifies where an observation attempt originated.
type SourceKind uint8

const (
	ManagementManual SourceKind = iota + 1
	OpenCodeManual
	OpenCodeScheduled
)

// Valid reports whether source is recognized.
func (source SourceKind) Valid() bool {
	switch source {
	case ManagementManual, OpenCodeManual, OpenCodeScheduled:
		return true
	default:
		return false
	}
}

// Completeness describes the authority carried by an observation.
type Completeness uint8

const (
	ScoreOnly Completeness = iota + 1
	ExhaustionEvidence
	AuthoritativeSnapshot
)

// Valid reports whether completeness is recognized.
func (completeness Completeness) Valid() bool {
	switch completeness {
	case ScoreOnly, ExhaustionEvidence, AuthoritativeSnapshot:
		return true
	default:
		return false
	}
}

// Outcome identifies the quota state reported for one scope.
type Outcome uint8

const (
	Healthy Outcome = iota + 1
	Exhausted
)

// Valid reports whether outcome is recognized.
func (outcome Outcome) Valid() bool {
	return outcome == Healthy || outcome == Exhausted
}

// manualQueryMetadata contains the normalized, non-sensitive request fields
// manual adapters may use for exact endpoint matching.
type manualQueryMetadata struct {
	Provider    string
	Method      string
	Scheme      string
	Host        string
	Hostname    string
	Port        string
	Path        string
	EscapedPath string
	RawQuery    string
}

// manualResponseMetadata contains the sanitized timing fields available to a
// manual adapter. ServerDate is optional; CompletedAt is local completion time.
type manualResponseMetadata struct {
	CompletedAt time.Time
	ServerDate  time.Time
}

// ObservationMetadata identifies the source and sanitized response timing used
// to produce a canonical batch.
type ObservationMetadata struct {
	Source      SourceKind
	CompletedAt time.Time
	ServerDate  time.Time
}

// Mutation carries one explicit scoped quota outcome.
type Mutation struct {
	Scope   ScopeKind
	Model   string
	Outcome Outcome
	ResetAt time.Time
}

// Observation is the provider-owned semantic projection of one response. Hub
// orchestration adds identity, ticket, and source metadata before submission.
type Observation struct {
	Score        *float64
	Completeness Completeness
	Mutations    []Mutation
}

// ObservationBatch is the complete canonical command produced by Hub.
type ObservationBatch struct {
	AuthID   string
	Provider string
	Ticket   auth.QuotaObservationTicket
	Metadata ObservationMetadata
	Observation
}

var (
	errAuthIDRequired         = errors.New("quota hub: auth ID is required")
	errProviderRequired       = errors.New("quota hub: provider is required")
	errTicketMismatch         = errors.New("quota hub: ticket identity does not match observation")
	errTicketInvalid          = errors.New("quota hub: ticket is invalid")
	errSourceInvalid          = errors.New("quota hub: source is invalid")
	errCompletedAtRequired    = errors.New("quota hub: completion time is required")
	errCompletenessInvalid    = errors.New("quota hub: completeness is invalid")
	errScoreInvalid           = errors.New("quota hub: score must be finite and between 0 and 100")
	errScoreOnlyInvalid       = errors.New("quota hub: score-only observation requires a score and no mutations")
	errMutationsRequired      = errors.New("quota hub: scoped mutations are required")
	errScopeInvalid           = errors.New("quota hub: scope is invalid")
	errScopeInactive          = errors.New("quota hub: model scope is not active")
	errAuthModelInvalid       = errors.New("quota hub: auth scope must not include a model")
	errOutcomeInvalid         = errors.New("quota hub: outcome is invalid")
	errHealthyEvidenceInvalid = errors.New("quota hub: healthy outcome requires an authoritative snapshot")
	errHealthyResetInvalid    = errors.New("quota hub: healthy outcome must not include a reset time")
	errExhaustedResetInvalid  = errors.New("quota hub: exhausted outcome requires a future reset time")
)

// Validate enforces the current auth-scoped canonical observation contract.
func (batch ObservationBatch) Validate() error {
	authID := strings.TrimSpace(batch.AuthID)
	provider := strings.TrimSpace(batch.Provider)
	if authID == "" {
		return errAuthIDRequired
	}
	if provider == "" {
		return errProviderRequired
	}
	if strings.TrimSpace(batch.Ticket.AuthID) != authID ||
		!strings.EqualFold(strings.TrimSpace(batch.Ticket.Provider), provider) {
		return errTicketMismatch
	}
	if batch.Ticket.Generation == 0 || batch.Ticket.StartOrder == 0 {
		return errTicketInvalid
	}
	if !batch.Metadata.Source.Valid() {
		return errSourceInvalid
	}
	if batch.Metadata.CompletedAt.IsZero() {
		return errCompletedAtRequired
	}
	if !batch.Completeness.Valid() {
		return errCompletenessInvalid
	}
	if batch.Score != nil && (math.IsNaN(*batch.Score) || math.IsInf(*batch.Score, 0) || *batch.Score < 0 || *batch.Score > 100) {
		return errScoreInvalid
	}
	if batch.Completeness == ScoreOnly {
		if batch.Score == nil || len(batch.Mutations) != 0 {
			return errScoreOnlyInvalid
		}
		return nil
	}
	if len(batch.Mutations) == 0 {
		return errMutationsRequired
	}

	for _, mutation := range batch.Mutations {
		if !mutation.Scope.Valid() {
			return errScopeInvalid
		}
		if !mutation.Scope.Active() {
			return errScopeInactive
		}
		if strings.TrimSpace(mutation.Model) != "" {
			return errAuthModelInvalid
		}
		if !mutation.Outcome.Valid() {
			return errOutcomeInvalid
		}
		switch mutation.Outcome {
		case Healthy:
			if batch.Completeness != AuthoritativeSnapshot {
				return errHealthyEvidenceInvalid
			}
			if !mutation.ResetAt.IsZero() {
				return errHealthyResetInvalid
			}
		case Exhausted:
			if mutation.ResetAt.IsZero() || !mutation.ResetAt.After(batch.Metadata.CompletedAt) {
				return errExhaustedResetInvalid
			}
		}
	}
	return nil
}

// ToAuthBatch validates and maps the canonical observation to the auth-owned
// transport-neutral sink command. Returned pointers and slices do not alias the
// canonical batch.
func (batch ObservationBatch) ToAuthBatch() (auth.QuotaObservationBatch, error) {
	if err := batch.Validate(); err != nil {
		return auth.QuotaObservationBatch{}, err
	}

	mapped := auth.QuotaObservationBatch{
		Ticket:       batch.Ticket,
		Completeness: mapCompleteness(batch.Completeness),
	}
	if batch.Score != nil {
		score := *batch.Score
		mapped.Score = &score
	}
	if len(batch.Mutations) != 0 {
		mapped.Mutations = make([]auth.QuotaObservationMutation, len(batch.Mutations))
		for index, mutation := range batch.Mutations {
			mapped.Mutations[index] = auth.QuotaObservationMutation{
				Scope:   mapScope(mutation.Scope),
				Model:   mutation.Model,
				Outcome: mapOutcome(mutation.Outcome),
				ResetAt: mutation.ResetAt,
			}
		}
	}
	return mapped, nil
}

func mapScope(scope ScopeKind) auth.QuotaObservationScope {
	switch scope {
	case ScopeAuth:
		return auth.QuotaObservationScopeAuth
	case ScopeModel:
		return auth.QuotaObservationScopeModel
	default:
		return 0
	}
}

func mapCompleteness(completeness Completeness) auth.QuotaObservationCompleteness {
	switch completeness {
	case ScoreOnly:
		return auth.QuotaObservationCompletenessScoreOnly
	case ExhaustionEvidence:
		return auth.QuotaObservationCompletenessExhaustionEvidence
	case AuthoritativeSnapshot:
		return auth.QuotaObservationCompletenessAuthoritativeSnapshot
	default:
		return 0
	}
}

func mapOutcome(outcome Outcome) auth.QuotaObservationOutcome {
	switch outcome {
	case Healthy:
		return auth.QuotaObservationOutcomeHealthy
	case Exhausted:
		return auth.QuotaObservationOutcomeExhausted
	default:
		return 0
	}
}
