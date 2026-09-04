package hub

import (
	"io"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

type manualQueryMatchFunc func(manualQueryMetadata) bool
type manualQueryObserveFunc func(manualResponseMetadata, io.Reader) (Observation, error)

// manualQueryAdapter is a stateless pair of pure provider functions. Keeping
// function values instead of interfaces prevents a mutable adapter object from
// being shared across copies of the compile-time table.
type manualQueryAdapter struct {
	provider string
	match    manualQueryMatchFunc
	observe  manualQueryObserveFunc
}

func (adapter manualQueryAdapter) matches(query manualQueryMetadata) bool {
	return adapter.match != nil && adapter.match(query)
}

func (adapter manualQueryAdapter) observeResponse(metadata manualResponseMetadata, body io.Reader) (Observation, error) {
	return adapter.observe(metadata, body)
}

// manualAdapterTable has no exported construction, registration, lookup, or
// observation surface. BeginManualQuery is the only planned external seam.
type manualAdapterTable struct {
	entries []manualQueryAdapter
}

func newManualAdapterTable(entries ...manualQueryAdapter) manualAdapterTable {
	cloned := make([]manualQueryAdapter, len(entries))
	copy(cloned, entries)
	return manualAdapterTable{entries: cloned}
}

// activeManualAdapterTable constructs the only production adapter bundle.
// Provider leaves add pure function entries here; no runtime registry exists.
func activeManualAdapterTable() manualAdapterTable {
	return newManualAdapterTable(
		codexManualQueryAdapter(),
		claudeManualQueryAdapter(),
		kimiManualQueryAdapter(),
	)
}

func (table manualAdapterTable) match(query manualQueryMetadata) (manualQueryAdapter, bool) {
	provider := strings.TrimSpace(query.Provider)
	if provider == "" {
		return manualQueryAdapter{}, false
	}
	for _, adapter := range table.entries {
		if adapter.provider == "" || adapter.observe == nil ||
			!strings.EqualFold(adapter.provider, provider) {
			continue
		}
		if adapter.matches(query) {
			return adapter, true
		}
	}
	return manualQueryAdapter{}, false
}

// openCodeResultMetadata keeps source policy explicit without disguising the
// typed poll result as an HTTP response.
type openCodeResultMetadata struct {
	Source              SourceKind
	CompletedAt         time.Time
	ThresholdConfigured bool
	Threshold           float64
}

type openCodeResultObserveFunc func(openCodeResultMetadata, *quota.PollResult) (Observation, error)

// openCodeResultAdapter is the single compile-time typed source adapter seam.
// Higher-level Hub submission owns identity, tickets, and auth sink application.
type openCodeResultAdapter struct {
	observe openCodeResultObserveFunc
}

func (adapter openCodeResultAdapter) observeResult(metadata openCodeResultMetadata, result *quota.PollResult) (Observation, bool, error) {
	if adapter.observe == nil {
		return Observation{}, false, nil
	}
	observation, err := adapter.observe(metadata, result)
	return observation, true, err
}

// activeOpenCodeResultAdapter constructs the production typed adapter. T2-6
// supplies its pure implementation without adding a registry or worker.
func activeOpenCodeResultAdapter() openCodeResultAdapter {
	return openCodeResultAdapter{observe: observeOpenCodeResult}
}
