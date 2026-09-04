package hub

import (
	"errors"
	"math"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

var (
	errOpenCodeResultMetadataInvalid = errors.New("quota hub: invalid OpenCode result metadata")
	errOpenCodePollResultInvalid     = errors.New("quota hub: invalid OpenCode poll result")
)

func observeOpenCodeResult(metadata openCodeResultMetadata, result *quota.PollResult) (Observation, error) {
	if (metadata.Source != OpenCodeManual && metadata.Source != OpenCodeScheduled) || metadata.CompletedAt.IsZero() {
		return Observation{}, errOpenCodeResultMetadataInvalid
	}
	if metadata.ThresholdConfigured && !validOpenCodePercentage(metadata.Threshold) {
		return Observation{}, errOpenCodeResultMetadataInvalid
	}
	if result == nil || result.Error != nil || result.Quota == nil {
		return Observation{}, errOpenCodePollResultInvalid
	}

	windows := []*quota.OpenCodeGoWindow{
		result.Quota.Rolling,
		result.Quota.Weekly,
		result.Quota.Monthly,
	}
	var score float64
	var resetAt time.Time
	found := false
	for _, window := range windows {
		if window == nil {
			continue
		}
		if math.IsNaN(window.PercentRemaining) || math.IsInf(window.PercentRemaining, 0) {
			return Observation{}, errOpenCodePollResultInvalid
		}

		remaining := min(max(window.PercentRemaining, 0), 100)
		if !found || remaining < score {
			score = remaining
			resetAt = window.ResetTime
			found = true
		}
	}
	if !found {
		return Observation{}, errOpenCodePollResultInvalid
	}

	observation := Observation{Score: &score, Completeness: ScoreOnly}
	if !metadata.ThresholdConfigured {
		return observation, nil
	}
	if score > metadata.Threshold {
		observation.Completeness = AuthoritativeSnapshot
		observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Healthy}}
		return observation, nil
	}
	if !resetAt.After(metadata.CompletedAt) {
		return observation, nil
	}

	observation.Completeness = ExhaustionEvidence
	observation.Mutations = []Mutation{{Scope: ScopeAuth, Outcome: Exhausted, ResetAt: resetAt}}
	return observation, nil
}

func validOpenCodePercentage(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}
