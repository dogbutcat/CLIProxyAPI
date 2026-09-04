package auth

import (
	"context"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// SeqRandomStartSelector advances sequentially through a provider pool after a
// random initial pick for that pool. Quota scores may weight only the initial
// pick; they never replace the sequential loop after the pool has been seeded.
type SeqRandomStartSelector struct {
	mu               sync.Mutex
	lastID           map[string]string
	seeded           map[string]bool
	quotaScoreLookup func(authID string) (float64, bool)
	maxKeys          int
}

// Pick selects an auth from the highest-priority available pool. Availability,
// priority filtering, model cooldown errors, and stable ID sorting are owned by
// the shared selector helpers.
func (s *SeqRandomStartSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	available, err := getAvailableAuths(auths, provider, model, time.Now())
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	if len(available) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	key := seqRandomPoolKey(available)
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(provider))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastID == nil {
		s.lastID = make(map[string]string)
		s.seeded = make(map[string]bool)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if !s.seeded[key] && len(s.lastID) >= limit {
		s.lastID = make(map[string]string)
		s.seeded = make(map[string]bool)
	}

	if !s.seeded[key] {
		selected := available[weightedRandomAuthIndex(available, s.quotaScoreLookup)]
		s.lastID[key] = selected.ID
		s.seeded[key] = true
		return selected, nil
	}

	selected := available[seqFindNextIndex(available, s.lastID[key])]
	s.lastID[key] = selected.ID
	return selected, nil
}

func (s *SeqRandomStartSelector) setQuotaScoreLookup(lookup func(authID string) (float64, bool)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotaScoreLookup = lookup
}

func weightedRandomAuthIndex(available []*Auth, lookup func(authID string) (float64, bool)) int {
	if len(available) <= 1 {
		return 0
	}
	weights := make([]float64, len(available))
	hasValidScore := false
	var total float64
	for index, auth := range available {
		var score float64
		var ok bool
		if lookup != nil && auth != nil {
			score, ok = lookup(auth.ID)
		}
		weight, valid := quotaScoreSelectionWeight(score, ok)
		if !valid {
			weight = 0
		}
		weights[index] = weight
		total += weight
		hasValidScore = hasValidScore || valid
	}
	if !hasValidScore || total <= 0 {
		return rand.IntN(len(available))
	}
	return weightedRandomIndex(weights, total)
}

func weightedRandomIndex(weights []float64, total float64) int {
	if len(weights) <= 1 {
		return 0
	}
	if total <= 0 {
		return rand.IntN(len(weights))
	}
	target := rand.Float64() * total
	var cumulative float64
	for index, weight := range weights {
		cumulative += weight
		if target < cumulative {
			return index
		}
	}
	return len(weights) - 1
}

func seqFindNextIndex(available []*Auth, lastID string) int {
	if len(available) == 0 {
		return 0
	}
	for index, auth := range available {
		if auth != nil && auth.ID == lastID {
			return (index + 1) % len(available)
		}
	}
	for index, auth := range available {
		if auth != nil && auth.ID > lastID {
			return index
		}
	}
	return 0
}

func seqRandomPoolKey(available []*Auth) string {
	seen := make(map[string]struct{}, len(available))
	providers := make([]string, 0, len(available))
	for _, auth := range available {
		if auth == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	if len(providers) == 0 {
		return ""
	}
	sort.Strings(providers)
	return strings.Join(providers, "|")
}
