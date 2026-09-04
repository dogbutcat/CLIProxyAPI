package usage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type monitoringQuery struct {
	Filter           plusstore.AnalyticsFilter
	Limit            int
	Cursor           *plusstore.AnalyticsEventCursor
	IncludeSelectors bool
	IncludeEvents    bool
	Full             bool
}

type monitoringAnalyticsAPIRequest struct {
	FromMS           int64                         `json:"from_ms"`
	ToMS             int64                         `json:"to_ms"`
	NowMS            int64                         `json:"now_ms"`
	TimeZone         string                        `json:"time_zone"`
	SearchQuery      string                        `json:"search_query"`
	SearchAPIKeyHash string                        `json:"search_api_key_hash"`
	Filters          monitoringAnalyticsAPIFilters `json:"filters"`
	Include          monitoringAnalyticsAPIInclude `json:"include"`
}

type monitoringAnalyticsAPIFilters struct {
	Models           []string `json:"models"`
	Providers        []string `json:"providers"`
	Accounts         []string `json:"accounts"`
	CredentialIDs    []string `json:"credential_ids"`
	AuthFiles        []string `json:"auth_files"`
	AuthIndices      []string `json:"auth_indices"`
	APIKeyHashes     []string `json:"api_key_hashes"`
	SourceHashes     []string `json:"source_hashes"`
	ProjectIDs       []string `json:"project_ids"`
	RequestTypes     []string `json:"request_types"`
	HeaderErrorKinds []string `json:"header_error_kinds"`
	HeaderErrorCodes []string `json:"header_error_codes"`
	HeaderQuotaPlans []string `json:"header_quota_plans"`
	HeaderTraceIDs   []string `json:"header_trace_ids"`
	IncludeFailed    *bool    `json:"include_failed"`
	FailedOnly       bool     `json:"failed_only"`
	MinLatencyMS     int64    `json:"min_latency_ms"`
	CacheStatus      string   `json:"cache_status"`
}

type monitoringAnalyticsAPIInclude struct {
	Summary            bool                                    `json:"summary"`
	SummaryProfile     string                                  `json:"summary_profile"`
	SummaryComparison  bool                                    `json:"summary_comparison"`
	Timeline           bool                                    `json:"timeline"`
	HourlyDistribution bool                                    `json:"hourly_distribution"`
	ModelShare         bool                                    `json:"model_share"`
	ChannelShare       bool                                    `json:"channel_share"`
	ModelStats         bool                                    `json:"model_stats"`
	FailureSources     bool                                    `json:"failure_sources"`
	AccountStats       bool                                    `json:"account_stats"`
	CredentialStats    bool                                    `json:"credential_stats"`
	CredentialTimeline bool                                    `json:"credential_timeline"`
	APIKeyStats        bool                                    `json:"api_key_stats"`
	FilterOptions      bool                                    `json:"filter_options"`
	FilterSelectors    bool                                    `json:"filter_selectors"`
	Heatmap            bool                                    `json:"heatmap"`
	AnomalyPoints      bool                                    `json:"anomaly_points"`
	TaskBuckets        bool                                    `json:"task_buckets"`
	RecentFailures     int                                     `json:"recent_failures"`
	EventsPage         *monitoringAnalyticsAPIEventsPage       `json:"events_page"`
	DrilldownPreview   *monitoringAnalyticsAPIDrilldownPreview `json:"drilldown_preview"`
	Granularity        string                                  `json:"granularity"`
}

type monitoringAnalyticsAPIEventsPage struct {
	Limit    int    `json:"limit"`
	BeforeMS *int64 `json:"before_ms"`
	BeforeID *int64 `json:"before_id"`
}

type monitoringAnalyticsAPIDrilldownPreview struct {
	FromMS int64 `json:"from_ms"`
	ToMS   int64 `json:"to_ms"`
	Limit  int   `json:"limit"`
}

type monitoringCursorEnvelope struct {
	TimestampMS int64 `json:"timestamp_ms"`
	ID          int64 `json:"id"`
}

func parseMonitoringQuery(c *gin.Context, defaultEvents bool) (monitoringQuery, bool) {
	query := monitoringQuery{
		Filter: plusstore.AnalyticsFilter{
			Provider:    queryFirst(c, "provider"),
			Account:     queryFirst(c, "account", "account_id", "accountId", "auth_index", "authIndex", "key", "key_id", "keyId"),
			RequestType: queryFirst(c, "request_type", "requestType", "endpoint", "path", "method"),
			Search:      queryFirst(c, "search", "q"),
			CacheStatus: queryFirst(c, "cache", "cache_status", "cacheStatus"),
		},
		IncludeEvents: defaultEvents,
		Limit:         plusstore.DefaultMonitoringLimit,
	}
	if fromMS, ok := optionalMonitoringInt64(c, "from_ms", "fromMs"); !ok {
		return monitoringQuery{}, false
	} else {
		query.Filter.FromMS = fromMS
	}
	if toMS, ok := optionalMonitoringInt64(c, "to_ms", "toMs"); !ok {
		return monitoringQuery{}, false
	} else {
		query.Filter.ToMS = toMS
	}
	failed, ok := parseMonitoringFailed(queryFirst(c, "failed", "failure"))
	if !ok {
		badRequest(c, "failed must be true, false, or all")
		return monitoringQuery{}, false
	}
	query.Filter.Failed = failed
	if limit, ok := optionalMonitoringInt(c, "limit"); !ok {
		return monitoringQuery{}, false
	} else if limit > 0 {
		query.Limit = plusstore.NormalizeMonitoringLimit(limit)
	}
	if cursorRaw := strings.TrimSpace(queryFirst(c, "cursor", "page_cursor", "pageCursor")); cursorRaw != "" {
		cursor, err := decodeMonitoringCursor(cursorRaw)
		if err != nil {
			badRequest(c, "cursor is invalid")
			return monitoringQuery{}, false
		}
		query.Cursor = cursor
	}
	for _, include := range splitMonitoringIncludes(queryFirst(c, "include")) {
		switch include {
		case "compact":
			query.Full = false
			query.IncludeEvents = defaultEvents
		case "full":
			query.Full = true
			query.IncludeSelectors = true
			query.IncludeEvents = true
		case "selectors", "options":
			query.IncludeSelectors = true
		case "events", "page", "realtime":
			query.IncludeEvents = true
		case "no_events", "no-events":
			query.IncludeEvents = false
		}
	}
	return query, true
}

func monitoringAnalyticsResponse(view string, analytics plusstore.MonitoringAnalytics, selectors *plusstore.MonitoringSelectors, page *plusstore.MonitoringEventPage, metadata map[string]MonitoringAuthMetadata, generatedAtMS int64) gin.H {
	resp := gin.H{
		"view":            view,
		"generated_at_ms": generatedAtMS,
		"status":          monitoringStatus(analytics.Totals),
		"summary":         monitoringAggregateJSON(analytics.Totals),
		"models":          monitoringDimensionStatsJSON(analytics.ByModel),
		"providers":       monitoringDimensionStatsJSON(analytics.ByProvider),
		"request_types":   monitoringDimensionStatsJSON(analytics.ByRequest),
		"cache_statuses":  monitoringDimensionStatsJSON(analytics.ByCache),
		"failed":          monitoringDimensionStatsJSON(analytics.ByFailed),
		"recent_errors":   monitoringEventsJSON(enrichMonitoringEvents(analytics.RecentErrors, metadata)),
	}
	accounts := enrichMonitoringIdentityStats(analytics.ByAccount, metadata)
	keys := enrichMonitoringIdentityStats(analytics.ByKey, metadata)
	resp["accounts"] = monitoringIdentityStatsJSON(accounts)
	resp["keys"] = monitoringIdentityStatsJSON(keys)
	if selectors != nil {
		resp["selectors"] = monitoringSelectorsJSON(*selectors, metadata)
	}
	if page != nil {
		resp["events_page"] = monitoringEventPageJSON(*page, metadata)
		resp["events"] = resp["events_page"].(gin.H)["events"]
	}
	return resp
}

func monitoringSelectorsResponse(selectors plusstore.MonitoringSelectors, metadata map[string]MonitoringAuthMetadata, generatedAtMS int64) gin.H {
	return gin.H{
		"generated_at_ms": generatedAtMS,
		"selectors":       monitoringSelectorsJSON(selectors, metadata),
	}
}

func monitoringEventPageJSON(page plusstore.MonitoringEventPage, metadata map[string]MonitoringAuthMetadata) gin.H {
	resp := gin.H{
		"events":         monitoringEventsJSON(enrichMonitoringEvents(page.Events, metadata)),
		"items":          monitoringEventsJSON(enrichMonitoringEvents(page.Events, metadata)),
		"next_cursor":    nil,
		"next_before_ms": nil,
		"next_before_id": nil,
		"has_more":       page.HasMore,
	}
	if page.NextCursor != nil {
		resp["next_cursor"] = encodeMonitoringCursor(*page.NextCursor)
		resp["next_before_ms"] = page.NextCursor.TimestampMS
		resp["next_before_id"] = page.NextCursor.ID
	}
	return resp
}

func parseMonitoringAnalyticsAPIRequest(c *gin.Context) (monitoringAnalyticsAPIRequest, plusstore.AnalyticsFilter, bool) {
	var req monitoringAnalyticsAPIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid monitoring analytics request")
		return req, plusstore.AnalyticsFilter{}, false
	}
	if req.FromMS <= 0 || req.ToMS <= 0 || req.FromMS >= req.ToMS {
		badRequest(c, "from_ms and to_ms must be positive and ordered")
		return req, plusstore.AnalyticsFilter{}, false
	}
	filter := plusstore.AnalyticsFilter{
		FromMS:           req.FromMS,
		ToMS:             req.ToMS,
		Models:           req.Filters.Models,
		Providers:        req.Filters.Providers,
		Accounts:         req.Filters.Accounts,
		CredentialIDs:    req.Filters.CredentialIDs,
		AuthFiles:        req.Filters.AuthFiles,
		AuthIndices:      req.Filters.AuthIndices,
		APIKeyHash:       req.SearchAPIKeyHash,
		APIKeyHashes:     req.Filters.APIKeyHashes,
		SourceHashes:     req.Filters.SourceHashes,
		ProjectIDs:       req.Filters.ProjectIDs,
		RequestTypes:     req.Filters.RequestTypes,
		HeaderErrorKinds: req.Filters.HeaderErrorKinds,
		HeaderErrorCodes: req.Filters.HeaderErrorCodes,
		HeaderQuotaPlans: req.Filters.HeaderQuotaPlans,
		HeaderTraceIDs:   req.Filters.HeaderTraceIDs,
		Search:           req.SearchQuery,
		CacheStatus:      req.Filters.CacheStatus,
		MinLatencyMS:     req.Filters.MinLatencyMS,
	}
	if req.Filters.FailedOnly {
		value := true
		filter.Failed = &value
	} else if req.Filters.IncludeFailed != nil && !*req.Filters.IncludeFailed {
		value := false
		filter.Failed = &value
	}
	return req, plusstore.NormalizeAnalyticsFilter(filter), true
}

func monitoringAnalyticsAPIResponse(req monitoringAnalyticsAPIRequest, analytics plusstore.MonitoringAnalytics, selectors *plusstore.MonitoringSelectors, page *plusstore.MonitoringEventPage, failures *plusstore.MonitoringEventPage, drilldownPreview *plusstore.MonitoringEventPage, metadata map[string]MonitoringAuthMetadata, generatedAtMS int64) gin.H {
	include := req.Include
	granularity := include.Granularity
	if granularity == "" {
		granularity = "hour"
	}
	accountStats := enrichMonitoringIdentityStats(analytics.ByAccount, metadata)
	apiKeyStats := enrichMonitoringIdentityStats(analytics.ByKey, metadata)
	resp := gin.H{
		"generated_at_ms": generatedAtMS,
		"granularity":     granularity,
	}
	if include.Summary || include.SummaryProfile != "" || !include.AccountStats && !include.APIKeyStats && page == nil && selectors == nil {
		resp["summary"] = monitoringAggregateJSON(analytics.Totals)
	}
	if include.SummaryComparison {
		resp["summary_comparison"] = monitoringSummaryComparisonJSON(req)
	}
	if include.ModelShare {
		resp["model_share"] = monitoringModelShareJSON(analytics.ByModel)
	}
	if include.ChannelShare {
		resp["channel_share"] = monitoringChannelShareJSON(accountStats)
	}
	if include.ModelStats {
		resp["model_stats"] = monitoringModelStatsJSON(analytics.ByModel)
	}
	if include.CredentialStats {
		resp["credential_stats"] = monitoringCredentialStatsJSON(accountStats)
	}
	if include.CredentialTimeline {
		resp["credential_timeline"] = monitoringCredentialTimelineJSON(accountStats, granularity)
	}
	if include.AccountStats {
		resp["account_stats"] = monitoringAccountStatsJSON(accountStats)
	}
	if include.APIKeyStats {
		resp["api_key_stats"] = monitoringAPIKeyStatsJSON(apiKeyStats)
	}
	if include.Timeline {
		resp["timeline"] = monitoringTimelineJSON(analytics.Timeline, granularity)
	}
	if include.HourlyDistribution {
		resp["hourly_distribution"] = monitoringTimelineJSON(analytics.Timeline, "hour")
	}
	if include.Heatmap {
		resp["heatmap"] = monitoringHeatmapJSON(analytics.Timeline, req.TimeZone)
	}
	if include.AnomalyPoints {
		resp["anomaly_points"] = monitoringAnomalyPointsJSON(analytics.Timeline, granularity)
	}
	if include.FailureSources {
		resp["failure_sources"] = monitoringFailureSourcesJSON(accountStats)
	}
	if include.TaskBuckets {
		resp["task_buckets"] = monitoringTimelineJSON(analytics.Timeline, granularity)
	}
	if selectors != nil {
		resp["filter_options"] = monitoringFilterOptionsJSON(*selectors, analytics, metadata)
	}
	if page != nil {
		resp["events"] = monitoringEventsPageAPIJSON(*page, metadata)
	}
	if failures != nil {
		resp["recent_failures"] = monitoringRecentFailuresJSON(failures.Events, metadata)
	}
	if drilldownPreview != nil {
		resp["drilldown_preview"] = monitoringEventsPageAPIJSON(*drilldownPreview, metadata)
	}
	return resp
}

func monitoringTimelineJSON(points []plusstore.MonitoringTimelinePoint, granularity string) []gin.H {
	bucketMS := int64(60 * 60 * 1000)
	if granularity == "day" {
		bucketMS = 24 * bucketMS
	}
	byBucket := make(map[int64]plusstore.AggregateMetrics, len(points))
	for _, point := range points {
		bucket := (point.BucketMS / bucketMS) * bucketMS
		metrics := byBucket[bucket]
		addMonitoringAggregateMetrics(&metrics, point.Metrics)
		byBucket[bucket] = metrics
	}
	buckets := make([]int64, 0, len(byBucket))
	for bucket := range byBucket {
		buckets = append(buckets, bucket)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
	out := make([]gin.H, 0, len(buckets))
	for _, bucket := range buckets {
		metrics := byBucket[bucket]
		success := metrics.EventCount - metrics.FailedCount
		if success < 0 {
			success = 0
		}
		var avgLatency any
		if metrics.LatencyCount > 0 {
			avgLatency = float64(metrics.LatencySumMS) / float64(metrics.LatencyCount)
		}
		out = append(out, gin.H{
			"bucket_ms":             bucket,
			"bucket_end_ms":         bucket + bucketMS,
			"label":                 "",
			"calls":                 metrics.EventCount,
			"tokens":                metrics.TotalTokens,
			"success":               success,
			"failure":               metrics.FailedCount,
			"input_tokens":          metrics.InputTokens,
			"output_tokens":         metrics.OutputTokens,
			"reasoning_tokens":      metrics.ReasoningTokens,
			"cached_tokens":         metrics.CachedTokens,
			"cache_read_tokens":     metrics.CacheReadTokens,
			"cache_creation_tokens": metrics.CacheCreationTokens,
			"total_tokens":          metrics.TotalTokens,
			"cost":                  0,
			"average_latency_ms":    avgLatency,
			"p95_latency_ms":        nil,
			"p95_ttft_ms":           nil,
			"success_rate":          rate(success, metrics.EventCount),
			"failure_rate":          rate(metrics.FailedCount, metrics.EventCount),
		})
	}
	return out
}

func addMonitoringAggregateMetrics(target *plusstore.AggregateMetrics, source plusstore.AggregateMetrics) {
	target.EventCount += source.EventCount
	target.FailedCount += source.FailedCount
	target.InputTokens += source.InputTokens
	target.OutputTokens += source.OutputTokens
	target.ReasoningTokens += source.ReasoningTokens
	target.CachedTokens += source.CachedTokens
	target.CacheTokens += source.CacheTokens
	target.CacheReadTokens += source.CacheReadTokens
	target.CacheCreationTokens += source.CacheCreationTokens
	target.TotalTokens += source.TotalTokens
	target.LatencySumMS += source.LatencySumMS
	target.LatencyCount += source.LatencyCount
	target.TTFTSumMS += source.TTFTSumMS
	target.TTFTCount += source.TTFTCount
	if target.FirstEventMS == 0 || (source.FirstEventMS > 0 && source.FirstEventMS < target.FirstEventMS) {
		target.FirstEventMS = source.FirstEventMS
	}
	if source.LastEventMS > target.LastEventMS {
		target.LastEventMS = source.LastEventMS
	}
}

func monitoringAggregateJSON(metrics plusstore.AggregateMetrics) gin.H {
	success := metrics.EventCount - metrics.FailedCount
	if success < 0 {
		success = 0
	}
	var avgLatency any
	if metrics.LatencyCount > 0 {
		avgLatency = float64(metrics.LatencySumMS) / float64(metrics.LatencyCount)
	}
	return gin.H{
		"total_calls":              metrics.EventCount,
		"success_calls":            success,
		"failure_calls":            metrics.FailedCount,
		"success_rate":             rate(success, metrics.EventCount),
		"input_tokens":             metrics.InputTokens,
		"output_tokens":            metrics.OutputTokens,
		"reasoning_tokens":         metrics.ReasoningTokens,
		"cached_tokens":            metrics.CachedTokens,
		"cache_read_tokens":        metrics.CacheReadTokens,
		"cache_creation_tokens":    metrics.CacheCreationTokens,
		"cache_write_tokens":       metrics.CacheCreationTokens,
		"total_tokens":             metrics.TotalTokens,
		"total_cost":               0,
		"average_cost_per_call":    0,
		"latency_samples":          metrics.LatencyCount,
		"average_latency_ms":       avgLatency,
		"rpm_30m":                  0,
		"tpm_30m":                  0,
		"avg_daily_requests":       0,
		"avg_daily_tokens":         0,
		"approx_tasks":             metrics.EventCount,
		"approx_task_failures":     metrics.FailedCount,
		"approx_task_success_rate": rate(success, metrics.EventCount),
		"zero_token_calls":         0,
		"zero_token_models":        []string{},
		"first_event_ms":           metrics.FirstEventMS,
		"last_event_ms":            metrics.LastEventMS,
	}
}

func monitoringStatus(metrics plusstore.AggregateMetrics) gin.H {
	state := "ok"
	if metrics.EventCount == 0 {
		state = "empty"
	}
	return gin.H{"state": state, "has_data": metrics.EventCount > 0}
}

func monitoringDimensionStatsJSON(stats []plusstore.MonitoringDimensionStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		out = append(out, gin.H{
			"key":     stat.Key,
			"summary": monitoringAggregateJSON(stat.Metrics),
		})
	}
	return out
}

func monitoringModelShareJSON(stats []plusstore.MonitoringDimensionStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		out = append(out, gin.H{
			"model":  stat.Key,
			"calls":  stat.Metrics.EventCount,
			"tokens": stat.Metrics.TotalTokens,
			"cost":   0,
		})
	}
	return out
}

func monitoringModelStatsJSON(stats []plusstore.MonitoringDimensionStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		successCalls := stat.Metrics.EventCount - stat.Metrics.FailedCount
		if successCalls < 0 {
			successCalls = 0
		}
		out = append(out, gin.H{
			"model":                 stat.Key,
			"calls":                 stat.Metrics.EventCount,
			"tokens":                stat.Metrics.TotalTokens,
			"cost":                  0,
			"success_calls":         successCalls,
			"failure_calls":         stat.Metrics.FailedCount,
			"success_rate":          rate(successCalls, stat.Metrics.EventCount),
			"input_tokens":          stat.Metrics.InputTokens,
			"output_tokens":         stat.Metrics.OutputTokens,
			"cached_tokens":         stat.Metrics.CachedTokens,
			"cache_read_tokens":     stat.Metrics.CacheReadTokens,
			"cache_creation_tokens": stat.Metrics.CacheCreationTokens,
			"total_tokens":          stat.Metrics.TotalTokens,
		})
	}
	return out
}

func monitoringIdentityStatsJSON(stats []plusstore.MonitoringIdentityStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		out = append(out, gin.H{
			"key":                      stat.Key,
			"provider":                 stat.Provider,
			"auth_index":               stat.AuthIndex,
			"source":                   safeMonitoringSource(stat.Source),
			"source_hash":              stat.SourceHash,
			"api_key_hash":             stat.APIKeyHash,
			"account_snapshot":         stat.AccountSnapshot,
			"auth_label_snapshot":      stat.AuthLabelSnapshot,
			"auth_file_snapshot":       stat.AuthFileSnapshot,
			"auth_provider_snapshot":   stat.AuthProviderSnapshot,
			"auth_project_id_snapshot": stat.AuthProjectIDSnapshot,
			"workspace_id":             stat.AuthProjectIDSnapshot,
			"entry":                    firstNonEmptyUsageString(stat.AuthFileSnapshot, stat.AuthLabelSnapshot, stat.AuthIndex),
			"protocol":                 stat.Provider,
			"key_alias":                firstNonEmptyUsageString(stat.AuthLabelSnapshot, safeMonitoringSource(stat.Source)),
			"last_seen_ms":             stat.LastSeenMS,
			"summary":                  monitoringAggregateJSON(stat.Metrics),
		})
	}
	return out
}

func monitoringAccountStatsJSON(stats []plusstore.MonitoringIdentityStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		row := monitoringIdentityStatBaseJSON(stat)
		row["id"] = stat.Key
		out = append(out, row)
	}
	return out
}

func monitoringAPIKeyStatsJSON(stats []plusstore.MonitoringIdentityStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		row := monitoringIdentityStatBaseJSON(stat)
		row["id"] = firstNonEmptyUsageString(stat.APIKeyHash, stat.Key)
		row["api_key_hash"] = firstNonEmptyUsageString(stat.APIKeyHash, stat.Key)
		out = append(out, row)
	}
	return out
}

func monitoringChannelShareJSON(stats []plusstore.MonitoringIdentityStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		successCalls := stat.Metrics.EventCount - stat.Metrics.FailedCount
		if successCalls < 0 {
			successCalls = 0
		}
		out = append(out, gin.H{
			"auth_index":             stat.AuthIndex,
			"source":                 safeMonitoringSource(stat.Source),
			"source_hash":            stat.SourceHash,
			"account_snapshot":       stat.AccountSnapshot,
			"auth_label_snapshot":    stat.AuthLabelSnapshot,
			"auth_provider_snapshot": stat.AuthProviderSnapshot,
			"calls":                  stat.Metrics.EventCount,
			"success":                successCalls,
			"failure":                stat.Metrics.FailedCount,
			"tokens":                 stat.Metrics.TotalTokens,
			"cost":                   0,
			"average_latency_ms":     monitoringAverageLatency(stat.Metrics),
		})
	}
	return out
}

func monitoringCredentialStatsJSON(stats []plusstore.MonitoringIdentityStat) []gin.H {
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		row := monitoringIdentityStatBaseJSON(stat)
		row["id"] = firstNonEmptyUsageString(stat.AuthIndex, stat.SourceHash, stat.Key)
		row["auth_index"] = stat.AuthIndex
		row["source"] = safeMonitoringSource(stat.Source)
		row["source_hash"] = stat.SourceHash
		row["auth_file_snapshot"] = stat.AuthFileSnapshot
		row["auth_project_id_snapshot"] = stat.AuthProjectIDSnapshot
		out = append(out, row)
	}
	return out
}

func monitoringCredentialTimelineJSON(stats []plusstore.MonitoringIdentityStat, granularity string) []gin.H {
	bucketMS := monitoringBucketSizeMS(granularity)
	out := make([]gin.H, 0, len(stats))
	for _, stat := range stats {
		bucket := stat.LastSeenMS
		if bucket <= 0 {
			bucket = stat.Metrics.LastEventMS
		}
		if bucket > 0 {
			bucket = (bucket / bucketMS) * bucketMS
		}
		successCalls := stat.Metrics.EventCount - stat.Metrics.FailedCount
		if successCalls < 0 {
			successCalls = 0
		}
		out = append(out, gin.H{
			"id":                       firstNonEmptyUsageString(stat.AuthIndex, stat.SourceHash, stat.Key),
			"bucket_ms":                bucket,
			"bucket_end_ms":            bucket + bucketMS,
			"bucket_label":             "",
			"calls":                    stat.Metrics.EventCount,
			"tokens":                   stat.Metrics.TotalTokens,
			"success":                  successCalls,
			"failure":                  stat.Metrics.FailedCount,
			"input_tokens":             stat.Metrics.InputTokens,
			"output_tokens":            stat.Metrics.OutputTokens,
			"reasoning_tokens":         stat.Metrics.ReasoningTokens,
			"cached_tokens":            stat.Metrics.CachedTokens,
			"cache_read_tokens":        stat.Metrics.CacheReadTokens,
			"cache_creation_tokens":    stat.Metrics.CacheCreationTokens,
			"total_tokens":             stat.Metrics.TotalTokens,
			"cost":                     0,
			"average_latency_ms":       monitoringAverageLatency(stat.Metrics),
			"success_rate":             rate(successCalls, stat.Metrics.EventCount),
			"failure_rate":             rate(stat.Metrics.FailedCount, stat.Metrics.EventCount),
			"auth_file_snapshot":       stat.AuthFileSnapshot,
			"auth_index":               stat.AuthIndex,
			"source":                   safeMonitoringSource(stat.Source),
			"source_hash":              stat.SourceHash,
			"account_snapshot":         stat.AccountSnapshot,
			"auth_label_snapshot":      stat.AuthLabelSnapshot,
			"auth_provider_snapshot":   stat.AuthProviderSnapshot,
			"auth_project_id_snapshot": stat.AuthProjectIDSnapshot,
		})
	}
	return out
}

func monitoringIdentityStatBaseJSON(stat plusstore.MonitoringIdentityStat) gin.H {
	summary := monitoringAggregateJSON(stat.Metrics)
	successCalls := stat.Metrics.EventCount - stat.Metrics.FailedCount
	if successCalls < 0 {
		successCalls = 0
	}
	return gin.H{
		"account_snapshot":         stat.AccountSnapshot,
		"auth_label_snapshot":      stat.AuthLabelSnapshot,
		"auth_file_snapshot":       stat.AuthFileSnapshot,
		"auth_provider_snapshot":   stat.AuthProviderSnapshot,
		"auth_project_id_snapshot": stat.AuthProjectIDSnapshot,
		"auth_indices":             nonEmptyStrings(stat.AuthIndex),
		"sources":                  nonEmptyStrings(safeMonitoringSource(stat.Source)),
		"source_hashes":            nonEmptyStrings(stat.SourceHash),
		"calls":                    stat.Metrics.EventCount,
		"success_calls":            successCalls,
		"failure_calls":            stat.Metrics.FailedCount,
		"success_rate":             rate(successCalls, stat.Metrics.EventCount),
		"input_tokens":             stat.Metrics.InputTokens,
		"output_tokens":            stat.Metrics.OutputTokens,
		"reasoning_tokens":         stat.Metrics.ReasoningTokens,
		"cached_tokens":            stat.Metrics.CachedTokens,
		"cache_read_tokens":        stat.Metrics.CacheReadTokens,
		"cache_creation_tokens":    stat.Metrics.CacheCreationTokens,
		"total_tokens":             stat.Metrics.TotalTokens,
		"cost":                     0,
		"average_latency_ms":       summary["average_latency_ms"],
		"last_seen_ms":             stat.LastSeenMS,
		"models":                   []gin.H{},
	}
}

func monitoringHeatmapJSON(points []plusstore.MonitoringTimelinePoint, timeZone string) []gin.H {
	loc := time.UTC
	if trimmed := strings.TrimSpace(timeZone); trimmed != "" {
		if loaded, err := time.LoadLocation(trimmed); err == nil {
			loc = loaded
		}
	}
	byCell := map[string]plusstore.AggregateMetrics{}
	type cellKey struct {
		weekday int
		hour    int
	}
	keysByCell := map[string]cellKey{}
	for _, point := range points {
		if point.Metrics.EventCount <= 0 {
			continue
		}
		when := time.UnixMilli(point.BucketMS).In(loc)
		key := cellKey{weekday: int(when.Weekday()), hour: when.Hour()}
		mapKey := fmt.Sprintf("%d:%d", key.weekday, key.hour)
		metrics := byCell[mapKey]
		addMonitoringAggregateMetrics(&metrics, point.Metrics)
		byCell[mapKey] = metrics
		keysByCell[mapKey] = key
	}
	mapKeys := make([]string, 0, len(byCell))
	for key := range byCell {
		mapKeys = append(mapKeys, key)
	}
	sort.Slice(mapKeys, func(i, j int) bool {
		left := keysByCell[mapKeys[i]]
		right := keysByCell[mapKeys[j]]
		if left.weekday != right.weekday {
			return left.weekday < right.weekday
		}
		return left.hour < right.hour
	})
	out := make([]gin.H, 0, len(mapKeys))
	for _, mapKey := range mapKeys {
		key := keysByCell[mapKey]
		metrics := byCell[mapKey]
		successCalls := metrics.EventCount - metrics.FailedCount
		if successCalls < 0 {
			successCalls = 0
		}
		out = append(out, gin.H{
			"weekday":      key.weekday,
			"hour":         key.hour,
			"calls":        metrics.EventCount,
			"success":      successCalls,
			"failure":      metrics.FailedCount,
			"tokens":       metrics.TotalTokens,
			"cost":         0,
			"failure_rate": rate(metrics.FailedCount, metrics.EventCount),
		})
	}
	return out
}

func monitoringAnomalyPointsJSON(points []plusstore.MonitoringTimelinePoint, granularity string) []gin.H {
	bucketMS := monitoringBucketSizeMS(granularity)
	out := []gin.H{}
	for _, point := range points {
		if point.Metrics.EventCount <= 0 || point.Metrics.FailedCount <= 0 {
			continue
		}
		failureRate := rate(point.Metrics.FailedCount, point.Metrics.EventCount)
		severity := "low"
		if failureRate >= 0.5 || point.Metrics.FailedCount >= 10 {
			severity = "high"
		} else if failureRate >= 0.2 || point.Metrics.FailedCount >= 3 {
			severity = "medium"
		}
		out = append(out, gin.H{
			"bucket_ms":                 point.BucketMS,
			"bucket_end_ms":             point.BucketMS + bucketMS,
			"label":                     "",
			"severity":                  severity,
			"metric_keys":               []string{"failureRate"},
			"calls":                     point.Metrics.EventCount,
			"total_tokens":              point.Metrics.TotalTokens,
			"cost":                      0,
			"failure_rate":              failureRate,
			"request_change":            0,
			"cost_change":               0,
			"tokens_per_request_change": 0,
			"cache_hit_rate_change":     0,
			"failure_rate_change":       0,
			"latency_p95_change":        0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i]["failure_rate"].(float64)
		right := out[j]["failure_rate"].(float64)
		if left != right {
			return left > right
		}
		return out[i]["calls"].(int64) > out[j]["calls"].(int64)
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func monitoringFailureSourcesJSON(stats []plusstore.MonitoringIdentityStat) []gin.H {
	out := []gin.H{}
	for _, stat := range stats {
		if stat.Metrics.FailedCount <= 0 {
			continue
		}
		out = append(out, gin.H{
			"source":                 safeMonitoringSource(stat.Source),
			"source_hash":            stat.SourceHash,
			"auth_index":             stat.AuthIndex,
			"account_snapshot":       stat.AccountSnapshot,
			"auth_label_snapshot":    stat.AuthLabelSnapshot,
			"auth_provider_snapshot": stat.AuthProviderSnapshot,
			"calls":                  stat.Metrics.EventCount,
			"failure":                stat.Metrics.FailedCount,
			"last_seen_ms":           stat.LastSeenMS,
			"average_latency_ms":     monitoringAverageLatency(stat.Metrics),
		})
	}
	return out
}

func monitoringSummaryComparisonJSON(req monitoringAnalyticsAPIRequest) gin.H {
	durationMS := req.ToMS - req.FromMS
	return gin.H{
		"previous_from_ms": req.FromMS - durationMS,
		"previous_to_ms":   req.FromMS,
	}
}

func monitoringBucketSizeMS(granularity string) int64 {
	if granularity == "day" {
		return int64(24 * 60 * 60 * 1000)
	}
	return int64(60 * 60 * 1000)
}

func monitoringAverageLatency(metrics plusstore.AggregateMetrics) any {
	if metrics.LatencyCount <= 0 {
		return nil
	}
	return float64(metrics.LatencySumMS) / float64(metrics.LatencyCount)
}

func monitoringEventsJSON(events []plusstore.MonitoringEvent) []gin.H {
	out := make([]gin.H, 0, len(events))
	for _, item := range events {
		event := item.Event
		row := gin.H{
			"id":                       item.ID,
			"request_id":               event.RequestID,
			"event_hash":               event.EventHash,
			"timestamp_ms":             event.TimestampMS,
			"timestamp":                event.Timestamp,
			"provider":                 event.Provider,
			"executor_type":            event.ExecutorType,
			"model":                    event.Model,
			"requested_model":          event.RequestedModel,
			"resolved_model":           event.ResolvedModel,
			"endpoint":                 event.Endpoint,
			"method":                   event.Method,
			"path":                     event.Path,
			"auth_type":                event.AuthType,
			"auth_index":               event.AuthIndex,
			"source":                   safeMonitoringSource(event.Source),
			"source_hash":              event.SourceHash,
			"api_key_hash":             event.APIKeyHash,
			"account_snapshot":         event.AccountSnapshot,
			"auth_label_snapshot":      event.AuthLabelSnapshot,
			"auth_file_snapshot":       event.AuthFileSnapshot,
			"auth_provider_snapshot":   event.AuthProviderSnapshot,
			"auth_project_id_snapshot": event.AuthProjectIDSnapshot,
			"workspace_id":             event.AuthProjectIDSnapshot,
			"entry":                    firstNonEmptyUsageString(event.AuthFileSnapshot, event.AuthLabelSnapshot, event.AuthIndex),
			"protocol":                 event.Provider,
			"key_alias":                firstNonEmptyUsageString(event.AuthLabelSnapshot, safeMonitoringSource(event.Source)),
			"reasoning_effort":         event.ReasoningEffort,
			"service_tier":             event.ServiceTier,
			"response_service_tier":    event.ResponseServiceTier,
			"input_tokens":             event.InputTokens,
			"output_tokens":            event.OutputTokens,
			"reasoning_tokens":         event.ReasoningTokens,
			"cached_tokens":            event.CachedTokens,
			"cache_read_tokens":        event.CacheReadTokens,
			"cache_creation_tokens":    event.CacheCreationTokens,
			"total_tokens":             event.TotalTokens,
			"cache_status":             item.CacheState,
			"failed":                   event.Failed,
			"fail_status_code":         event.FailStatusCode,
			"fail_summary":             event.FailSummary,
			"response_metadata":        event.ResponseMetadata,
			"header_quota_plan_type":   event.HeaderQuotaPlanType,
			"header_error_kind":        event.HeaderErrorKind,
			"header_error_code":        event.HeaderErrorCode,
			"header_trace_id":          event.HeaderTraceID,
			"created_at_ms":            event.CreatedAtMS,
		}
		if event.LatencyMS != nil {
			row["latency_ms"] = *event.LatencyMS
		}
		if event.TTFTMS != nil {
			row["ttft_ms"] = *event.TTFTMS
		}
		if event.HeaderQuotaRecoverAtMS > 0 {
			row["header_quota_recover_at_ms"] = event.HeaderQuotaRecoverAtMS
		}
		if event.HeaderQuotaUsedPercent != nil {
			row["header_quota_used_percent"] = *event.HeaderQuotaUsedPercent
		}
		out = append(out, row)
	}
	return out
}

func monitoringEventsPageAPIJSON(page plusstore.MonitoringEventPage, metadata map[string]MonitoringAuthMetadata) gin.H {
	resp := monitoringEventPageJSON(page, metadata)
	resp["total_count"] = nil
	return resp
}

func monitoringRecentFailuresJSON(events []plusstore.MonitoringEvent, metadata map[string]MonitoringAuthMetadata) []gin.H {
	out := make([]gin.H, 0, len(events))
	for _, row := range monitoringEventsJSON(enrichMonitoringEvents(events, metadata)) {
		out = append(out, row)
	}
	return out
}

func monitoringHeaderSnapshotsResponse(events []plusstore.MonitoringEvent, metadata map[string]MonitoringAuthMetadata, fromMS, toMS, generatedAtMS int64) gin.H {
	items := make([]gin.H, 0, len(events))
	for _, row := range monitoringEventsJSON(enrichMonitoringEvents(events, metadata)) {
		if row["response_metadata"] == nil &&
			row["header_quota_recover_at_ms"] == nil &&
			row["header_quota_used_percent"] == nil &&
			row["header_quota_plan_type"] == "" &&
			row["header_error_kind"] == "" &&
			row["header_error_code"] == "" &&
			row["header_trace_id"] == "" {
			continue
		}
		items = append(items, gin.H{
			"event_hash":                 row["event_hash"],
			"timestamp_ms":               row["timestamp_ms"],
			"auth_file_snapshot":         row["auth_file_snapshot"],
			"auth_index":                 row["auth_index"],
			"account_snapshot":           row["account_snapshot"],
			"auth_label_snapshot":        row["auth_label_snapshot"],
			"auth_provider_snapshot":     row["auth_provider_snapshot"],
			"auth_project_id_snapshot":   row["auth_project_id_snapshot"],
			"source":                     row["source"],
			"source_hash":                row["source_hash"],
			"response_metadata":          row["response_metadata"],
			"header_quota_recover_at_ms": row["header_quota_recover_at_ms"],
			"header_quota_used_percent":  row["header_quota_used_percent"],
			"header_quota_plan_type":     row["header_quota_plan_type"],
			"header_error_kind":          row["header_error_kind"],
			"header_error_code":          row["header_error_code"],
			"header_trace_id":            row["header_trace_id"],
		})
	}
	return gin.H{
		"generated_at_ms": generatedAtMS,
		"from_ms":         fromMS,
		"to_ms":           toMS,
		"items":           items,
	}
}

func monitoringFilterOptionsJSON(selectors plusstore.MonitoringSelectors, analytics plusstore.MonitoringAnalytics, metadata map[string]MonitoringAuthMetadata) gin.H {
	accountStats := enrichMonitoringIdentityStats(analytics.ByAccount, metadata)
	apiKeyStats := enrichMonitoringIdentityStats(analytics.ByKey, metadata)
	return gin.H{
		"account_stats":      monitoringAccountStatsJSON(accountStats),
		"api_key_stats":      monitoringAPIKeyStatsJSON(apiKeyStats),
		"credential_stats":   monitoringCredentialStatsJSON(accountStats),
		"channel_share":      monitoringChannelShareJSON(accountStats),
		"model_stats":        monitoringModelStatsJSON(analytics.ByModel),
		"models":             monitoringSelectorValues(selectors.Models),
		"api_key_hashes":     monitoringSelectorValuesFromIdentity(analytics.ByKey),
		"providers":          monitoringSelectorValues(selectors.Providers),
		"auth_files":         monitoringAuthFilesFromIdentity(analytics.ByAccount),
		"accounts":           monitoringSelectorValues(selectors.Accounts),
		"account_count":      len(analytics.ByAccount),
		"api_key_count":      len(analytics.ByKey),
		"request_types":      monitoringSelectorValues(selectors.RequestTypes),
		"header_error_kinds": []string{},
		"header_error_codes": []string{},
		"header_quota_plans": []string{},
		"header_trace_ids":   []string{},
	}
}

func monitoringSelectorsJSON(selectors plusstore.MonitoringSelectors, metadata map[string]MonitoringAuthMetadata) gin.H {
	return gin.H{
		"models":         monitoringSelectorOptionsJSON(selectors.Models),
		"providers":      monitoringSelectorOptionsJSON(selectors.Providers),
		"accounts":       monitoringSelectorOptionsJSON(enrichMonitoringSelectorAccounts(selectors.Accounts, metadata)),
		"request_types":  monitoringSelectorOptionsJSON(selectors.RequestTypes),
		"cache_statuses": monitoringSelectorOptionsJSON(selectors.CacheStatuses),
		"failed":         monitoringSelectorOptionsJSON(selectors.Failed),
	}
}

func monitoringSelectorOptionsJSON(options []plusstore.MonitoringSelectorOption) []gin.H {
	out := make([]gin.H, 0, len(options))
	for _, option := range options {
		out = append(out, gin.H{"value": option.Value, "label": option.Label, "count": option.Count})
	}
	return out
}

func monitoringSelectorValues(options []plusstore.MonitoringSelectorOption) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		if option.Value != "" && option.Value != "-" {
			out = append(out, option.Value)
		}
	}
	return out
}

func monitoringSelectorValuesFromIdentity(stats []plusstore.MonitoringIdentityStat) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, stat := range stats {
		value := strings.TrimSpace(stat.APIKeyHash)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func monitoringAuthFilesFromIdentity(stats []plusstore.MonitoringIdentityStat) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, stat := range stats {
		value := strings.TrimSpace(stat.AuthFileSnapshot)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func nonEmptyStrings(values ...string) []string {
	out := []string{}
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" && trimmed != "-" {
			out = append(out, trimmed)
		}
	}
	return out
}

func safeMonitoringSource(value string) string {
	return plusstore.MaskSource(value)
}

func enrichMonitoringIdentityStats(stats []plusstore.MonitoringIdentityStat, metadataByAuthIndex map[string]MonitoringAuthMetadata) []plusstore.MonitoringIdentityStat {
	if len(stats) == 0 || len(metadataByAuthIndex) == 0 {
		return stats
	}
	out := make([]plusstore.MonitoringIdentityStat, len(stats))
	copy(out, stats)
	for i, stat := range out {
		metadata, ok := metadataByAuthIndex[strings.TrimSpace(stat.AuthIndex)]
		if !ok {
			continue
		}
		applyMonitoringMetadataToIdentity(&stat, metadata)
		out[i] = stat
	}
	return out
}

func enrichMonitoringEvents(events []plusstore.MonitoringEvent, metadataByAuthIndex map[string]MonitoringAuthMetadata) []plusstore.MonitoringEvent {
	if len(events) == 0 || len(metadataByAuthIndex) == 0 {
		return events
	}
	out := make([]plusstore.MonitoringEvent, len(events))
	copy(out, events)
	for i, item := range out {
		metadata, ok := metadataByAuthIndex[strings.TrimSpace(item.Event.AuthIndex)]
		if !ok {
			continue
		}
		applyMonitoringMetadataToEvent(&item.Event, metadata)
		out[i] = item
	}
	return out
}

func enrichMonitoringSelectorAccounts(options []plusstore.MonitoringSelectorOption, metadataByAuthIndex map[string]MonitoringAuthMetadata) []plusstore.MonitoringSelectorOption {
	if len(options) == 0 || len(metadataByAuthIndex) == 0 {
		return options
	}
	out := make([]plusstore.MonitoringSelectorOption, len(options))
	copy(out, options)
	for i, option := range out {
		if metadata, ok := metadataByAuthIndex[strings.TrimSpace(option.Value)]; ok {
			option.Label = firstNonEmptyUsageString(metadata.AccountName, metadata.AuthLabel, option.Label)
			out[i] = option
		}
	}
	return out
}

func applyMonitoringMetadataToIdentity(stat *plusstore.MonitoringIdentityStat, metadata MonitoringAuthMetadata) {
	if stat == nil {
		return
	}
	if isMonitoringSnapshotFillable(stat.AccountSnapshot, metadata) {
		stat.AccountSnapshot = firstNonEmptyUsageString(metadata.AccountName, stat.AccountSnapshot)
	}
	if isMonitoringSnapshotFillable(stat.AuthLabelSnapshot, metadata) {
		stat.AuthLabelSnapshot = firstNonEmptyUsageString(metadata.AuthLabel, stat.AuthLabelSnapshot)
	}
	if isMonitoringSnapshotFillable(stat.AuthFileSnapshot, metadata) {
		stat.AuthFileSnapshot = firstNonEmptyUsageString(metadata.AuthFile, metadata.GeneratedName, stat.AuthFileSnapshot)
	}
	if isMonitoringSnapshotFillable(stat.AuthProviderSnapshot, metadata) {
		stat.AuthProviderSnapshot = firstNonEmptyUsageString(metadata.Provider, metadata.AuthProvider, metadata.Protocol, stat.AuthProviderSnapshot)
	}
	if isMonitoringSnapshotFillable(stat.AuthProjectIDSnapshot, metadata) {
		stat.AuthProjectIDSnapshot = firstNonEmptyUsageString(metadata.ProjectID, stat.AuthProjectIDSnapshot)
	}
	stat.Provider = firstNonEmptyUsageString(metadata.Provider, stat.Provider)
}

func applyMonitoringMetadataToEvent(event *plusstore.Event, metadata MonitoringAuthMetadata) {
	if event == nil {
		return
	}
	if isMonitoringSnapshotFillable(event.AccountSnapshot, metadata) {
		event.AccountSnapshot = firstNonEmptyUsageString(metadata.AccountName, event.AccountSnapshot)
	}
	if isMonitoringSnapshotFillable(event.AuthLabelSnapshot, metadata) {
		event.AuthLabelSnapshot = firstNonEmptyUsageString(metadata.AuthLabel, event.AuthLabelSnapshot)
	}
	if isMonitoringSnapshotFillable(event.AuthFileSnapshot, metadata) {
		event.AuthFileSnapshot = firstNonEmptyUsageString(metadata.AuthFile, metadata.GeneratedName, event.AuthFileSnapshot)
	}
	if isMonitoringSnapshotFillable(event.AuthProviderSnapshot, metadata) {
		event.AuthProviderSnapshot = firstNonEmptyUsageString(metadata.Provider, metadata.AuthProvider, metadata.Protocol, event.AuthProviderSnapshot)
	}
	if isMonitoringSnapshotFillable(event.AuthProjectIDSnapshot, metadata) {
		event.AuthProjectIDSnapshot = firstNonEmptyUsageString(metadata.ProjectID, event.AuthProjectIDSnapshot)
	}
	event.Provider = firstNonEmptyUsageString(metadata.Provider, event.Provider)
}

func optionalMonitoringInt64(c *gin.Context, names ...string) (int64, bool) {
	raw := strings.TrimSpace(queryFirst(c, names...))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		badRequest(c, names[0]+" must be a non-negative integer")
		return 0, false
	}
	return value, true
}

func optionalMonitoringInt(c *gin.Context, names ...string) (int, bool) {
	raw := strings.TrimSpace(queryFirst(c, names...))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		badRequest(c, names[0]+" must be a non-negative integer")
		return 0, false
	}
	return value, true
}

func parseMonitoringFailed(raw string) (*bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all", "any":
		return nil, true
	case "true", "1", "failed", "failure", "failures":
		value := true
		return &value, true
	case "false", "0", "success", "succeeded", "ok":
		value := false
		return &value, true
	default:
		return nil, false
	}
}

func queryFirst(c *gin.Context, names ...string) string {
	if c == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(c.Query(name)); value != "" {
			return value
		}
	}
	return ""
}

func splitMonitoringIncludes(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.ToLower(strings.TrimSpace(part)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func encodeMonitoringCursor(cursor plusstore.AnalyticsEventCursor) string {
	payload, err := json.Marshal(monitoringCursorEnvelope{TimestampMS: cursor.TimestampMS, ID: cursor.ID})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeMonitoringCursor(raw string) (*plusstore.AnalyticsEventCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	var envelope monitoringCursorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	if envelope.TimestampMS <= 0 || envelope.ID <= 0 {
		return nil, fmt.Errorf("invalid monitoring cursor")
	}
	return &plusstore.AnalyticsEventCursor{TimestampMS: envelope.TimestampMS, ID: envelope.ID}, nil
}

func monitoringUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage bridge is unavailable"})
}
