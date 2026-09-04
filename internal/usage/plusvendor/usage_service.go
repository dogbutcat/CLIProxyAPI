package plusvendor

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

const (
	MaxUsageQueryLimit              = 50_000
	DashboardRollingWindowMinutes   = 30
	DashboardRollingWindowMS        = int64(DashboardRollingWindowMinutes * 60 * 1000)
	DashboardHourWindowMS           = int64(60 * 60 * 1000)
	DashboardHealthTimelineBucketMS = int64(10 * 60 * 1000)
	DashboardHealthTimelineBuckets  = 24 * 6
	DashboardHealthRows             = 5
)

type QueryIssue struct {
	Component string `json:"component"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
}

type QueryStatus struct {
	State    string       `json:"state"`
	HasData  bool         `json:"has_data"`
	Partial  bool         `json:"partial"`
	Stale    bool         `json:"stale"`
	Errors   []QueryIssue `json:"errors,omitempty"`
	Warnings []QueryIssue `json:"warnings,omitempty"`
}

type DashboardSummaryQuery struct {
	TodayStartMS   int64
	NowMS          int64
	TopModels      int
	RecentFailures int
}

type DashboardSummary struct {
	Status           QueryStatus
	Aggregate        Aggregate
	Rolling          Aggregate
	Models           []ModelStat
	ModelStats       []ModelStat
	ProviderActivity []ProviderActivityStat
	Failures         []RecentFailure
	Traffic          []TimelinePoint
	HealthTimeline   []TimelinePoint
	ChannelHealth    []ChannelHealthStat
	FailureSources   []FailureSourceStat
	RollupCheckpoint int64
}

type Aggregate struct {
	TotalCalls          int64
	SuccessCalls        int64
	FailureCalls        int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	LatencySamples      int64
	AvgLatencyMS        *float64
	ZeroTokenCalls      int64
}

type ProviderActivityStat struct {
	Provider     string
	Calls        int64
	SuccessCalls int64
	FailureCalls int64
	TotalTokens  int64
}

type ModelStat struct {
	Model               string
	Calls               int64
	SuccessCalls        int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

type TimelinePoint struct {
	BucketMS int64
	Model    string
	Calls    int64
	Success  int64
	Failure  int64
	Tokens   int64
}

type RecentFailure struct {
	TimestampMS            int64
	Model                  string
	APIKeyHash             string
	Source                 string
	SourceHash             string
	AuthIndex              string
	Endpoint               string
	AccountSnapshot        string
	AuthLabelSnapshot      string
	AuthProviderSnapshot   string
	AuthProjectIDSnapshot  string
	FailSummary            string
	ResponseMetadata       *plusstore.ResponseHeaderMetadata
	HeaderQuotaPlanType    string
	HeaderErrorKind        string
	HeaderErrorCode        string
	HeaderTraceID          string
	LatencyMS              *int64
	FailStatusCode         *int64
	HeaderQuotaRecoverAtMS *int64
	HeaderQuotaUsedPercent *float64
}

type ChannelHealthStat struct {
	AuthIndex             string
	Source                string
	SourceHash            string
	APIKeyHash            string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	Calls                 int64
	Failures              int64
	TotalTokens           int64
	LatencySumMS          int64
	LatencySamples        int64
	LastSeenMS            int64
}

type FailureSourceStat struct {
	AuthIndex             string
	Source                string
	SourceHash            string
	APIKeyHash            string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	Calls                 int64
	Failures              int64
	TotalTokens           int64
	LatencySumMS          int64
	LatencySamples        int64
	LastSeenMS            int64
}

type dashboardStore interface {
	DashboardRollup(context.Context, plusstore.AggregateQuery) (plusstore.DashboardRollup, error)
	HourlyRollupCheckpoint(context.Context) (int64, error)
	EventsBetween(context.Context, int64, int64, int) ([]plusstore.Event, error)
}

type monitoringStore interface {
	MonitoringAnalytics(context.Context, plusstore.AnalyticsFilter) (plusstore.MonitoringAnalytics, error)
	MonitoringSelectors(context.Context, plusstore.AnalyticsFilter) (plusstore.MonitoringSelectors, error)
	MonitoringEventsPage(context.Context, plusstore.AnalyticsEventPageQuery) (plusstore.MonitoringEventPage, error)
}

type auxiliaryStore interface {
	LoadModelPrices(context.Context) (map[string]plusstore.ModelPrice, error)
	SaveModelPrices(context.Context, map[string]plusstore.ModelPrice) error
	UpsertSyncedModelPrices(context.Context, map[string]plusstore.ModelPrice) (plusstore.ModelPriceSyncResult, error)
	DeleteModelPrice(context.Context, string) error
	ModelPriceUsageSummary(context.Context, int) (plusstore.ModelPriceUsageSummary, error)
	LoadAPIKeyAliases(context.Context) ([]plusstore.APIKeyAlias, error)
	SaveAPIKeyAliases(context.Context, []plusstore.APIKeyAlias, []string, bool) ([]plusstore.APIKeyAlias, error)
	DeleteAPIKeyAlias(context.Context, string) error
	ExportEventsJSONL(context.Context, io.Writer) (int, error)
	ImportEventsJSONL(context.Context, io.Reader) (plusstore.UsageImportResult, error)
}

type UsageService struct {
	store      dashboardStore
	monitoring monitoringStore
	auxiliary  auxiliaryStore
}

func NewUsageService(store *plusstore.Store) *UsageService {
	return &UsageService{store: store, monitoring: store, auxiliary: store}
}

func newDashboardUsageService(store dashboardStore) *UsageService {
	return &UsageService{store: store}
}

func (s *UsageService) DashboardSummary(ctx context.Context, query DashboardSummaryQuery) (DashboardSummary, error) {
	if s == nil || s.store == nil {
		return DashboardSummary{}, fmt.Errorf("dashboard summary: store is nil")
	}
	query = normalizeDashboardSummaryQuery(query)
	today, err := s.store.DashboardRollup(ctx, plusstore.AggregateQuery{FromMS: query.TodayStartMS, ToMS: query.NowMS})
	if err != nil {
		return DashboardSummary{}, fmt.Errorf("dashboard summary rollup: %w", err)
	}
	rolling, err := s.store.DashboardRollup(ctx, plusstore.AggregateQuery{FromMS: query.NowMS - DashboardRollingWindowMS, ToMS: query.NowMS})
	if err != nil {
		return DashboardSummary{}, fmt.Errorf("dashboard rolling rollup: %w", err)
	}

	status := QueryStatus{State: "ok"}
	checkpointMS, err := s.store.HourlyRollupCheckpoint(ctx)
	if err != nil {
		status.Partial = true
		status.Warnings = append(status.Warnings, QueryIssue{Component: "store", Kind: "checkpoint_error", Message: err.Error()})
	}
	if checkpointMS > 0 && checkpointMS < floorHour(query.NowMS)-DashboardHourWindowMS {
		status.Stale = true
		status.Warnings = append(status.Warnings, QueryIssue{Component: "store", Kind: "rollup_stale", Message: "hourly rollup checkpoint is behind the current hour"})
	}

	var rawEvents []plusstore.Event
	rawEvents, err = s.store.EventsBetween(ctx, query.TodayStartMS, query.NowMS, MaxUsageQueryLimit)
	if err != nil {
		status.Partial = true
		status.Warnings = append(status.Warnings, QueryIssue{Component: "raw_events", Kind: "query_error", Message: err.Error()})
	} else if len(rawEvents) == MaxUsageQueryLimit {
		status.Partial = true
		status.Warnings = append(status.Warnings, QueryIssue{Component: "raw_events", Kind: "limit_reached", Message: "raw event query reached the 50000 row limit"})
	}

	modelStats := modelStatsFromRollup(today.ByModel)
	summary := DashboardSummary{
		Status:           status,
		Aggregate:        aggregateFromMetrics(today.Totals),
		Rolling:          aggregateFromMetrics(rolling.Totals),
		ModelStats:       modelStats,
		Models:           limitModelStats(modelStats, query.TopModels),
		ProviderActivity: providerActivityFromRollup(today.ByProvider),
		Failures:         recentFailuresFromEvents(rawEvents, query.RecentFailures),
		Traffic:          timelineFromHourly(today.Hourly, DashboardHourWindowMS),
		HealthTimeline:   healthTimelineFromEvents(rawEvents, DashboardHealthTimelineBucketMS),
		ChannelHealth:    channelHealthFromHourly(today.Hourly),
		FailureSources:   failureSourcesFromHourly(today.Hourly),
		RollupCheckpoint: checkpointMS,
	}
	summary.Status.HasData = summary.Aggregate.TotalCalls > 0
	if summary.Status.Partial {
		summary.Status.State = "partial"
	} else if !summary.Status.HasData {
		summary.Status.State = "empty"
	}
	return summary, nil
}

func (s *UsageService) MonitoringAnalytics(ctx context.Context, filter plusstore.AnalyticsFilter) (plusstore.MonitoringAnalytics, error) {
	if s == nil || s.monitoring == nil {
		return plusstore.MonitoringAnalytics{}, fmt.Errorf("monitoring analytics: store is nil")
	}
	return s.monitoring.MonitoringAnalytics(ctx, plusstore.NormalizeAnalyticsFilter(filter))
}

func (s *UsageService) MonitoringSelectors(ctx context.Context, filter plusstore.AnalyticsFilter) (plusstore.MonitoringSelectors, error) {
	if s == nil || s.monitoring == nil {
		return plusstore.MonitoringSelectors{}, fmt.Errorf("monitoring selectors: store is nil")
	}
	return s.monitoring.MonitoringSelectors(ctx, plusstore.NormalizeAnalyticsFilter(filter))
}

func (s *UsageService) MonitoringEventsPage(ctx context.Context, query plusstore.AnalyticsEventPageQuery) (plusstore.MonitoringEventPage, error) {
	if s == nil || s.monitoring == nil {
		return plusstore.MonitoringEventPage{}, fmt.Errorf("monitoring events page: store is nil")
	}
	query.Filter = plusstore.NormalizeAnalyticsFilter(query.Filter)
	query.Limit = plusstore.NormalizeMonitoringLimit(query.Limit)
	return s.monitoring.MonitoringEventsPage(ctx, query)
}

func normalizeDashboardSummaryQuery(query DashboardSummaryQuery) DashboardSummaryQuery {
	if query.TopModels <= 0 {
		query.TopModels = 5
	}
	if query.TopModels > 100 {
		query.TopModels = 100
	}
	if query.RecentFailures <= 0 {
		query.RecentFailures = 10
	}
	if query.RecentFailures > 100 {
		query.RecentFailures = 100
	}
	if query.NowMS <= 0 {
		query.NowMS = time.Now().UnixMilli()
	}
	return query
}

func aggregateFromMetrics(metrics plusstore.AggregateMetrics) Aggregate {
	success := metrics.EventCount - metrics.FailedCount
	if success < 0 {
		success = 0
	}
	out := Aggregate{
		TotalCalls:          metrics.EventCount,
		SuccessCalls:        success,
		FailureCalls:        metrics.FailedCount,
		InputTokens:         metrics.InputTokens,
		OutputTokens:        metrics.OutputTokens,
		ReasoningTokens:     metrics.ReasoningTokens,
		CachedTokens:        metrics.CachedTokens,
		CacheReadTokens:     metrics.CacheReadTokens,
		CacheCreationTokens: metrics.CacheCreationTokens,
		TotalTokens:         metrics.TotalTokens,
		LatencySamples:      metrics.LatencyCount,
	}
	if metrics.LatencyCount > 0 {
		avg := float64(metrics.LatencySumMS) / float64(metrics.LatencyCount)
		out.AvgLatencyMS = &avg
	}
	return out
}

func providerActivityFromRollup(rows []plusstore.DimensionUsageRollup) []ProviderActivityStat {
	out := make([]ProviderActivityStat, 0, len(rows))
	for _, row := range rows {
		agg := aggregateFromMetrics(row.Metrics)
		out = append(out, ProviderActivityStat{
			Provider:     row.Key,
			Calls:        agg.TotalCalls,
			SuccessCalls: agg.SuccessCalls,
			FailureCalls: agg.FailureCalls,
			TotalTokens:  agg.TotalTokens,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func modelStatsFromRollup(rows []plusstore.DimensionUsageRollup) []ModelStat {
	out := make([]ModelStat, 0, len(rows))
	for _, row := range rows {
		agg := aggregateFromMetrics(row.Metrics)
		out = append(out, ModelStat{
			Model:               row.Key,
			Calls:               agg.TotalCalls,
			SuccessCalls:        agg.SuccessCalls,
			InputTokens:         agg.InputTokens,
			OutputTokens:        agg.OutputTokens,
			ReasoningTokens:     agg.ReasoningTokens,
			CachedTokens:        agg.CachedTokens,
			CacheReadTokens:     agg.CacheReadTokens,
			CacheCreationTokens: agg.CacheCreationTokens,
			TotalTokens:         agg.TotalTokens,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func limitModelStats(rows []ModelStat, limit int) []ModelStat {
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]ModelStat, len(rows))
	copy(out, rows)
	return out
}

func timelineFromHourly(rows []plusstore.HourlyAggregate, bucketMS int64) []TimelinePoint {
	byBucket := map[int64]TimelinePoint{}
	for _, row := range rows {
		bucket := row.HourMS - row.HourMS%bucketMS
		point := byBucket[bucket]
		point.BucketMS = bucket
		point.Calls += row.Metrics.EventCount
		point.Failure += row.Metrics.FailedCount
		success := row.Metrics.EventCount - row.Metrics.FailedCount
		if success > 0 {
			point.Success += success
		}
		point.Tokens += row.Metrics.TotalTokens
		byBucket[bucket] = point
	}
	return sortedTimelinePoints(byBucket)
}

func healthTimelineFromEvents(events []plusstore.Event, bucketMS int64) []TimelinePoint {
	byBucket := map[int64]TimelinePoint{}
	for _, event := range events {
		bucket := event.TimestampMS - event.TimestampMS%bucketMS
		point := byBucket[bucket]
		point.BucketMS = bucket
		point.Calls++
		if event.Failed {
			point.Failure++
		} else {
			point.Success++
		}
		point.Tokens += event.TotalTokens
		byBucket[bucket] = point
	}
	return sortedTimelinePoints(byBucket)
}

func sortedTimelinePoints(points map[int64]TimelinePoint) []TimelinePoint {
	out := make([]TimelinePoint, 0, len(points))
	for _, point := range points {
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BucketMS < out[j].BucketMS })
	return out
}

func recentFailuresFromEvents(events []plusstore.Event, limit int) []RecentFailure {
	out := make([]RecentFailure, 0, limit)
	for _, event := range events {
		if !event.Failed {
			continue
		}
		var failStatus *int64
		if event.FailStatusCode > 0 {
			value := int64(event.FailStatusCode)
			failStatus = &value
		}
		out = append(out, RecentFailure{
			TimestampMS:            event.TimestampMS,
			Model:                  firstNonEmptyString(event.ResolvedModel, event.Model, "-"),
			APIKeyHash:             event.APIKeyHash,
			Source:                 event.Source,
			SourceHash:             event.SourceHash,
			AuthIndex:              event.AuthIndex,
			Endpoint:               event.Endpoint,
			AccountSnapshot:        event.AccountSnapshot,
			AuthLabelSnapshot:      event.AuthLabelSnapshot,
			AuthProviderSnapshot:   event.AuthProviderSnapshot,
			AuthProjectIDSnapshot:  event.AuthProjectIDSnapshot,
			FailSummary:            event.FailSummary,
			ResponseMetadata:       event.ResponseMetadata,
			HeaderQuotaPlanType:    event.HeaderQuotaPlanType,
			HeaderErrorKind:        event.HeaderErrorKind,
			HeaderErrorCode:        event.HeaderErrorCode,
			HeaderTraceID:          event.HeaderTraceID,
			LatencyMS:              event.LatencyMS,
			FailStatusCode:         failStatus,
			HeaderQuotaRecoverAtMS: positiveInt64Ptr(event.HeaderQuotaRecoverAtMS),
			HeaderQuotaUsedPercent: event.HeaderQuotaUsedPercent,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func channelHealthFromHourly(rows []plusstore.HourlyAggregate) []ChannelHealthStat {
	grouped := map[string]ChannelHealthStat{}
	for _, row := range rows {
		key := channelKey(row.Dimension)
		stat := grouped[key]
		if stat.AuthIndex == "" && stat.Source == "" && stat.SourceHash == "" {
			stat = channelStatFromDimension(row.Dimension)
		}
		addHealthMetrics(&stat, row.Metrics)
		grouped[key] = stat
	}
	out := make([]ChannelHealthStat, 0, len(grouped))
	for _, stat := range grouped {
		out = append(out, stat)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Failures != out[j].Failures {
			return out[i].Failures > out[j].Failures
		}
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].AuthIndex < out[j].AuthIndex
	})
	return out
}

func failureSourcesFromHourly(rows []plusstore.HourlyAggregate) []FailureSourceStat {
	grouped := map[string]FailureSourceStat{}
	for _, row := range rows {
		key := channelKey(row.Dimension)
		stat := grouped[key]
		if stat.AuthIndex == "" && stat.Source == "" && stat.SourceHash == "" {
			base := channelStatFromDimension(row.Dimension)
			stat = FailureSourceStat(base)
		}
		addFailureSourceMetrics(&stat, row.Metrics)
		grouped[key] = stat
	}
	out := make([]FailureSourceStat, 0, len(grouped))
	for _, stat := range grouped {
		if stat.Failures == 0 {
			continue
		}
		out = append(out, stat)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Failures != out[j].Failures {
			return out[i].Failures > out[j].Failures
		}
		return out[i].LastSeenMS > out[j].LastSeenMS
	})
	return out
}

func channelStatFromDimension(dim plusstore.AggregateDimensions) ChannelHealthStat {
	return ChannelHealthStat{
		AuthIndex:             dim.AuthIndex,
		Source:                dim.Source,
		SourceHash:            dim.SourceHash,
		APIKeyHash:            dim.APIKeyHash,
		AccountSnapshot:       dim.AccountSnapshot,
		AuthLabelSnapshot:     dim.AuthLabelSnapshot,
		AuthProviderSnapshot:  dim.AuthProviderSnapshot,
		AuthProjectIDSnapshot: dim.AuthProjectIDSnapshot,
	}
}

func channelKey(dim plusstore.AggregateDimensions) string {
	return strings.Join([]string{
		dim.AuthIndex,
		dim.Source,
		dim.SourceHash,
		dim.APIKeyHash,
		dim.AccountSnapshot,
		dim.AuthLabelSnapshot,
		dim.AuthProviderSnapshot,
		dim.AuthProjectIDSnapshot,
	}, "\x00")
}

func addHealthMetrics(stat *ChannelHealthStat, metrics plusstore.AggregateMetrics) {
	stat.Calls += metrics.EventCount
	stat.Failures += metrics.FailedCount
	stat.TotalTokens += metrics.TotalTokens
	stat.LatencySumMS += metrics.LatencySumMS
	stat.LatencySamples += metrics.LatencyCount
	if metrics.LastEventMS > stat.LastSeenMS {
		stat.LastSeenMS = metrics.LastEventMS
	}
}

func addFailureSourceMetrics(stat *FailureSourceStat, metrics plusstore.AggregateMetrics) {
	stat.Calls += metrics.EventCount
	stat.Failures += metrics.FailedCount
	stat.TotalTokens += metrics.TotalTokens
	stat.LatencySumMS += metrics.LatencySumMS
	stat.LatencySamples += metrics.LatencyCount
	if metrics.LastEventMS > stat.LastSeenMS {
		stat.LastSeenMS = metrics.LastEventMS
	}
}

func positiveInt64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func floorHour(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms - ms%DashboardHourWindowMS
}
