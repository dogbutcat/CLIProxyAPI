package plusstore

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

const (
	AggregateSchemaVersion = 1
	HourMS                 = int64(60 * 60 * 1000)
)

type AggregateDimensions struct {
	Provider              string `json:"provider,omitempty"`
	ExecutorType          string `json:"executor_type,omitempty"`
	Model                 string `json:"model,omitempty"`
	RequestedModel        string `json:"requested_model,omitempty"`
	ResolvedModel         string `json:"resolved_model,omitempty"`
	Endpoint              string `json:"endpoint,omitempty"`
	AuthType              string `json:"auth_type,omitempty"`
	AuthIndex             string `json:"auth_index,omitempty"`
	AccountID             string `json:"account_id,omitempty"`
	Source                string `json:"source,omitempty"`
	SourceHash            string `json:"source_hash,omitempty"`
	APIKeyHash            string `json:"api_key_hash,omitempty"`
	AccountSnapshot       string `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot     string `json:"auth_label_snapshot,omitempty"`
	AuthFileSnapshot      string `json:"auth_file_snapshot,omitempty"`
	AuthProviderSnapshot  string `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot string `json:"auth_project_id_snapshot,omitempty"`
	Failed                bool   `json:"failed"`
	FailStatusCode        int    `json:"fail_status_code,omitempty"`
	CacheStatus           string `json:"cache_status,omitempty"`
}

type AggregateMetrics struct {
	EventCount          int64 `json:"event_count"`
	FailedCount         int64 `json:"failed_count"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheTokens         int64 `json:"cache_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	LatencySumMS        int64 `json:"latency_sum_ms"`
	LatencyCount        int64 `json:"latency_count"`
	TTFTSumMS           int64 `json:"ttft_sum_ms"`
	TTFTCount           int64 `json:"ttft_count"`
	FirstEventMS        int64 `json:"first_event_ms"`
	LastEventMS         int64 `json:"last_event_ms"`
}

type HourlyAggregate struct {
	HourMS    int64               `json:"hour_ms"`
	Dimension AggregateDimensions `json:"dimension"`
	Metrics   AggregateMetrics    `json:"metrics"`
}

type AggregateQuery struct {
	FromMS      int64
	ToMS        int64
	Provider    string
	Model       string
	AccountID   string
	CacheStatus string
	Failed      *bool
}

func (m *AggregateMetrics) addEvent(event Event) {
	m.EventCount++
	if event.Failed {
		m.FailedCount++
	}
	m.InputTokens += event.InputTokens
	m.OutputTokens += event.OutputTokens
	m.ReasoningTokens += event.ReasoningTokens
	m.CachedTokens += event.CachedTokens
	m.CacheTokens += event.CacheTokens
	m.CacheReadTokens += event.CacheReadTokens
	m.CacheCreationTokens += event.CacheCreationTokens
	m.TotalTokens += event.TotalTokens
	if event.LatencyMS != nil {
		m.LatencySumMS += *event.LatencyMS
		m.LatencyCount++
	}
	if event.TTFTMS != nil {
		m.TTFTSumMS += *event.TTFTMS
		m.TTFTCount++
	}
	if m.FirstEventMS == 0 || event.TimestampMS < m.FirstEventMS {
		m.FirstEventMS = event.TimestampMS
	}
	if event.TimestampMS > m.LastEventMS {
		m.LastEventMS = event.TimestampMS
	}
}

func (m *AggregateMetrics) addMetrics(other AggregateMetrics) {
	m.EventCount += other.EventCount
	m.FailedCount += other.FailedCount
	m.InputTokens += other.InputTokens
	m.OutputTokens += other.OutputTokens
	m.ReasoningTokens += other.ReasoningTokens
	m.CachedTokens += other.CachedTokens
	m.CacheTokens += other.CacheTokens
	m.CacheReadTokens += other.CacheReadTokens
	m.CacheCreationTokens += other.CacheCreationTokens
	m.TotalTokens += other.TotalTokens
	m.LatencySumMS += other.LatencySumMS
	m.LatencyCount += other.LatencyCount
	m.TTFTSumMS += other.TTFTSumMS
	m.TTFTCount += other.TTFTCount
	if m.FirstEventMS == 0 || (other.FirstEventMS > 0 && other.FirstEventMS < m.FirstEventMS) {
		m.FirstEventMS = other.FirstEventMS
	}
	if other.LastEventMS > m.LastEventMS {
		m.LastEventMS = other.LastEventMS
	}
}

func aggregateDimensionForEvent(event Event) AggregateDimensions {
	return AggregateDimensions{
		Provider:              cleanDimension(event.Provider),
		ExecutorType:          cleanDimension(event.ExecutorType),
		Model:                 cleanDimension(firstNonEmptyString(event.ResolvedModel, event.Model, "-")),
		RequestedModel:        cleanDimension(event.RequestedModel),
		ResolvedModel:         cleanDimension(event.ResolvedModel),
		Endpoint:              cleanDimension(event.Endpoint),
		AuthType:              cleanDimension(event.AuthType),
		AuthIndex:             cleanDimension(event.AuthIndex),
		AccountID:             accountIDForEvent(event),
		Source:                cleanDimension(event.Source),
		SourceHash:            cleanDimension(event.SourceHash),
		APIKeyHash:            cleanDimension(event.APIKeyHash),
		AccountSnapshot:       cleanDimension(event.AccountSnapshot),
		AuthLabelSnapshot:     cleanDimension(event.AuthLabelSnapshot),
		AuthFileSnapshot:      cleanDimension(event.AuthFileSnapshot),
		AuthProviderSnapshot:  cleanDimension(event.AuthProviderSnapshot),
		AuthProjectIDSnapshot: cleanDimension(event.AuthProjectIDSnapshot),
		Failed:                event.Failed,
		FailStatusCode:        event.FailStatusCode,
		CacheStatus:           cacheStatusForEvent(event),
	}
}

func dimensionKey(d AggregateDimensions) string {
	parts := []string{
		d.Provider,
		d.ExecutorType,
		d.Model,
		d.RequestedModel,
		d.ResolvedModel,
		d.Endpoint,
		d.AuthType,
		d.AuthIndex,
		d.AccountID,
		d.Source,
		d.SourceHash,
		d.APIKeyHash,
		d.AccountSnapshot,
		d.AuthLabelSnapshot,
		d.AuthFileSnapshot,
		d.AuthProviderSnapshot,
		d.AuthProjectIDSnapshot,
		boolDimension(d.Failed),
		intDimension(d.FailStatusCode),
		d.CacheStatus,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func aggregateKey(hourMS int64, d AggregateDimensions) string {
	return intDimension64(hourMS) + "\x00" + dimensionKey(d)
}

func cacheStatusForEvent(event Event) string {
	if event.CacheCreationTokens > 0 && (event.CacheReadTokens > 0 || event.CacheTokens > 0 || event.CachedTokens > 0) {
		return "read_write"
	}
	if event.CacheCreationTokens > 0 {
		return "write"
	}
	if event.CacheReadTokens > 0 || event.CacheTokens > 0 || event.CachedTokens > 0 {
		return "read"
	}
	return "none"
}

func accountIDForEvent(event Event) string {
	return cleanDimension(firstNonEmptyString(
		event.AuthIndex,
		event.AccountSnapshot,
		event.AuthLabelSnapshot,
		event.AuthProjectIDSnapshot,
		event.APIKeyHash,
		event.SourceHash,
		event.Source,
	))
}

func cleanDimension(value string) string {
	return strings.TrimSpace(value)
}

func boolDimension(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func intDimension(value int) string {
	return strconv.Itoa(value)
}

func intDimension64(value int64) string {
	return strconv.FormatInt(value, 10)
}
