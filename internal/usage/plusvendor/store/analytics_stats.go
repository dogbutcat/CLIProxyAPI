package plusstore

import (
	"context"
	"database/sql"
	"fmt"
)

type MonitoringAnalytics struct {
	Totals       AggregateMetrics
	Timeline     []MonitoringTimelinePoint
	ByModel      []MonitoringDimensionStat
	ByProvider   []MonitoringDimensionStat
	ByAccount    []MonitoringIdentityStat
	ByKey        []MonitoringIdentityStat
	ByRequest    []MonitoringDimensionStat
	ByCache      []MonitoringDimensionStat
	ByFailed     []MonitoringDimensionStat
	RecentErrors []MonitoringEvent
}

type MonitoringDimensionStat struct {
	Key     string
	Metrics AggregateMetrics
}

type MonitoringTimelinePoint struct {
	BucketMS int64
	Metrics  AggregateMetrics
}

type MonitoringIdentityStat struct {
	Key                   string
	Provider              string
	AuthIndex             string
	Source                string
	SourceHash            string
	APIKeyHash            string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	AuthFileSnapshot      string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	Metrics               AggregateMetrics
	LastSeenMS            int64
}

type MonitoringEvent struct {
	ID         int64
	Event      Event
	CacheState string
}

type MonitoringEventPage struct {
	Events     []MonitoringEvent
	NextCursor *AnalyticsEventCursor
	HasMore    bool
}

func (s *Store) MonitoringAnalytics(ctx context.Context, filter AnalyticsFilter) (MonitoringAnalytics, error) {
	if s == nil || s.db == nil {
		return MonitoringAnalytics{}, fmt.Errorf("monitoring analytics: store is nil")
	}
	filter = NormalizeAnalyticsFilter(filter)
	totals, err := s.monitoringTotals(ctx, filter)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	timeline, err := s.monitoringTimeline(ctx, filter, "hour")
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	byModel, err := s.monitoringDimensionStats(ctx, filter, "model", analyticsModelSQL(), 100)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	byProvider, err := s.monitoringDimensionStats(ctx, filter, "provider", analyticsProviderSQL(), 100)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	byRequest, err := s.monitoringDimensionStats(ctx, filter, "request type", analyticsRequestTypeSQL(), 100)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	byCache, err := s.monitoringDimensionStats(ctx, filter, "cache", analyticsCacheStatusSQL(), 10)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	byFailed, err := s.monitoringDimensionStats(ctx, filter, "failed", "case when failed != 0 then 'true' else 'false' end", 2)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	byAccount, err := s.monitoringIdentityStats(ctx, filter, "account", analyticsAccountSQL(), 100)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	byKey, err := s.monitoringIdentityStats(ctx, filter, "key", analyticsKeySQL(), 100)
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	errorsPage, err := s.MonitoringEventsPage(ctx, AnalyticsEventPageQuery{
		Filter: AnalyticsFilter{
			FromMS:      filter.FromMS,
			ToMS:        filter.ToMS,
			Provider:    filter.Provider,
			Account:     filter.Account,
			RequestType: filter.RequestType,
			Search:      filter.Search,
			CacheStatus: filter.CacheStatus,
			Failed:      boolPtr(true),
		},
		Limit: 10,
	})
	if err != nil {
		return MonitoringAnalytics{}, err
	}
	return MonitoringAnalytics{
		Totals:       totals,
		Timeline:     timeline,
		ByModel:      byModel,
		ByProvider:   byProvider,
		ByAccount:    byAccount,
		ByKey:        byKey,
		ByRequest:    byRequest,
		ByCache:      byCache,
		ByFailed:     byFailed,
		RecentErrors: errorsPage.Events,
	}, nil
}

func (s *Store) monitoringTimeline(ctx context.Context, filter AnalyticsFilter, granularity string) ([]MonitoringTimelinePoint, error) {
	where := buildAnalyticsWhere(filter)
	bucketMS := int64(60 * 60 * 1000)
	if granularity == "day" {
		bucketMS = 24 * bucketMS
	}
	args := []any{bucketMS, bucketMS}
	args = append(args, where.args...)
	rows, err := s.db.QueryContext(ctx, `select (timestamp_ms / ?) * ? as bucket_ms,
		count(*),
		coalesce(sum(case when failed != 0 then 1 else 0 end),0),
		coalesce(sum(input_tokens),0),
		coalesce(sum(output_tokens),0),
		coalesce(sum(reasoning_tokens),0),
		coalesce(sum(cached_tokens),0),
		coalesce(sum(cache_tokens),0),
		coalesce(sum(cache_read_tokens),0),
		coalesce(sum(cache_creation_tokens),0),
		coalesce(sum(total_tokens),0),
		coalesce(sum(case when latency_ms is not null then latency_ms else 0 end),0),
		coalesce(sum(case when latency_ms is not null then 1 else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then ttft_ms else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then 1 else 0 end),0),
		coalesce(min(timestamp_ms),0),
		coalesce(max(timestamp_ms),0)
		from usage_events`+where.sql+`
		group by bucket_ms
		order by bucket_ms asc`, args...)
	if err != nil {
		return nil, fmt.Errorf("monitoring timeline: query: %w", err)
	}
	defer rows.Close()
	out := []MonitoringTimelinePoint{}
	for rows.Next() {
		var point MonitoringTimelinePoint
		if err := rows.Scan(
			&point.BucketMS,
			&point.Metrics.EventCount,
			&point.Metrics.FailedCount,
			&point.Metrics.InputTokens,
			&point.Metrics.OutputTokens,
			&point.Metrics.ReasoningTokens,
			&point.Metrics.CachedTokens,
			&point.Metrics.CacheTokens,
			&point.Metrics.CacheReadTokens,
			&point.Metrics.CacheCreationTokens,
			&point.Metrics.TotalTokens,
			&point.Metrics.LatencySumMS,
			&point.Metrics.LatencyCount,
			&point.Metrics.TTFTSumMS,
			&point.Metrics.TTFTCount,
			&point.Metrics.FirstEventMS,
			&point.Metrics.LastEventMS,
		); err != nil {
			return nil, fmt.Errorf("monitoring timeline: scan: %w", err)
		}
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("monitoring timeline: read rows: %w", err)
	}
	return out, nil
}

func (s *Store) MonitoringEventsPage(ctx context.Context, query AnalyticsEventPageQuery) (MonitoringEventPage, error) {
	if s == nil || s.db == nil {
		return MonitoringEventPage{}, fmt.Errorf("monitoring events page: store is nil")
	}
	limit := NormalizeMonitoringLimit(query.Limit)
	where := addAnalyticsCursor(buildAnalyticsWhere(query.Filter), query.Cursor)
	args := append([]any{}, where.args...)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, selectMonitoringEventColumnsSQL()+where.sql+` order by timestamp_ms desc, id desc limit ?`, args...)
	if err != nil {
		return MonitoringEventPage{}, fmt.Errorf("monitoring events page: query: %w", err)
	}
	defer rows.Close()
	events, err := scanMonitoringEvents(rows, "monitoring events page")
	if err != nil {
		return MonitoringEventPage{}, err
	}
	var next *AnalyticsEventCursor
	if len(events) > limit {
		events = events[:limit]
		last := events[len(events)-1]
		next = &AnalyticsEventCursor{TimestampMS: last.Event.TimestampMS, ID: last.ID}
	}
	return MonitoringEventPage{Events: events, NextCursor: next, HasMore: next != nil}, nil
}

func (s *Store) monitoringTotals(ctx context.Context, filter AnalyticsFilter) (AggregateMetrics, error) {
	where := buildAnalyticsWhere(filter)
	row := s.db.QueryRowContext(ctx, `select
		count(*),
		coalesce(sum(case when failed != 0 then 1 else 0 end),0),
		coalesce(sum(input_tokens),0),
		coalesce(sum(output_tokens),0),
		coalesce(sum(reasoning_tokens),0),
		coalesce(sum(cached_tokens),0),
		coalesce(sum(cache_tokens),0),
		coalesce(sum(cache_read_tokens),0),
		coalesce(sum(cache_creation_tokens),0),
		coalesce(sum(total_tokens),0),
		coalesce(sum(case when latency_ms is not null then latency_ms else 0 end),0),
		coalesce(sum(case when latency_ms is not null then 1 else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then ttft_ms else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then 1 else 0 end),0),
		coalesce(min(timestamp_ms),0),
		coalesce(max(timestamp_ms),0)
		from usage_events`+where.sql, where.args...)
	metrics, err := scanMonitoringMetrics(row)
	if err != nil {
		return AggregateMetrics{}, fmt.Errorf("monitoring totals: %w", err)
	}
	return metrics, nil
}

func (s *Store) monitoringDimensionStats(ctx context.Context, filter AnalyticsFilter, label, keySQL string, limit int) ([]MonitoringDimensionStat, error) {
	where := buildAnalyticsWhere(filter)
	args := append([]any{}, where.args...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `select `+keySQL+` as dimension_key,
		count(*),
		coalesce(sum(case when failed != 0 then 1 else 0 end),0),
		coalesce(sum(input_tokens),0),
		coalesce(sum(output_tokens),0),
		coalesce(sum(reasoning_tokens),0),
		coalesce(sum(cached_tokens),0),
		coalesce(sum(cache_tokens),0),
		coalesce(sum(cache_read_tokens),0),
		coalesce(sum(cache_creation_tokens),0),
		coalesce(sum(total_tokens),0),
		coalesce(sum(case when latency_ms is not null then latency_ms else 0 end),0),
		coalesce(sum(case when latency_ms is not null then 1 else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then ttft_ms else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then 1 else 0 end),0),
		coalesce(min(timestamp_ms),0),
		coalesce(max(timestamp_ms),0)
		from usage_events`+where.sql+`
		group by dimension_key
		order by count(*) desc, dimension_key asc
		limit ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("monitoring %s stats: query: %w", label, err)
	}
	defer rows.Close()
	out := []MonitoringDimensionStat{}
	for rows.Next() {
		var key string
		var metrics AggregateMetrics
		if err := scanDimensionMetric(rows, &key, &metrics); err != nil {
			return nil, fmt.Errorf("monitoring %s stats: scan: %w", label, err)
		}
		out = append(out, MonitoringDimensionStat{Key: firstNonEmptyString(key, "-"), Metrics: metrics})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("monitoring %s stats: read rows: %w", label, err)
	}
	return out, nil
}

func (s *Store) monitoringIdentityStats(ctx context.Context, filter AnalyticsFilter, label, keySQL string, limit int) ([]MonitoringIdentityStat, error) {
	where := buildAnalyticsWhere(filter)
	args := append([]any{}, where.args...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `select `+keySQL+` as identity_key,
		coalesce(max(provider),''),
		coalesce(max(auth_index),''),
		coalesce(max(source),''),
		coalesce(max(source_hash),''),
		coalesce(max(api_key_hash),''),
		coalesce(max(account_snapshot),''),
		coalesce(max(auth_label_snapshot),''),
		coalesce(max(auth_file_snapshot),''),
		coalesce(max(auth_provider_snapshot),''),
		coalesce(max(auth_project_id_snapshot),''),
		count(*),
		coalesce(sum(case when failed != 0 then 1 else 0 end),0),
		coalesce(sum(input_tokens),0),
		coalesce(sum(output_tokens),0),
		coalesce(sum(reasoning_tokens),0),
		coalesce(sum(cached_tokens),0),
		coalesce(sum(cache_tokens),0),
		coalesce(sum(cache_read_tokens),0),
		coalesce(sum(cache_creation_tokens),0),
		coalesce(sum(total_tokens),0),
		coalesce(sum(case when latency_ms is not null then latency_ms else 0 end),0),
		coalesce(sum(case when latency_ms is not null then 1 else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then ttft_ms else 0 end),0),
		coalesce(sum(case when ttft_ms is not null then 1 else 0 end),0),
		coalesce(min(timestamp_ms),0),
		coalesce(max(timestamp_ms),0)
		from usage_events`+where.sql+`
		group by identity_key
		order by count(*) desc, identity_key asc
		limit ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("monitoring %s stats: query: %w", label, err)
	}
	defer rows.Close()
	out := []MonitoringIdentityStat{}
	for rows.Next() {
		var stat MonitoringIdentityStat
		if err := rows.Scan(
			&stat.Key,
			&stat.Provider,
			&stat.AuthIndex,
			&stat.Source,
			&stat.SourceHash,
			&stat.APIKeyHash,
			&stat.AccountSnapshot,
			&stat.AuthLabelSnapshot,
			&stat.AuthFileSnapshot,
			&stat.AuthProviderSnapshot,
			&stat.AuthProjectIDSnapshot,
			&stat.Metrics.EventCount,
			&stat.Metrics.FailedCount,
			&stat.Metrics.InputTokens,
			&stat.Metrics.OutputTokens,
			&stat.Metrics.ReasoningTokens,
			&stat.Metrics.CachedTokens,
			&stat.Metrics.CacheTokens,
			&stat.Metrics.CacheReadTokens,
			&stat.Metrics.CacheCreationTokens,
			&stat.Metrics.TotalTokens,
			&stat.Metrics.LatencySumMS,
			&stat.Metrics.LatencyCount,
			&stat.Metrics.TTFTSumMS,
			&stat.Metrics.TTFTCount,
			&stat.Metrics.FirstEventMS,
			&stat.Metrics.LastEventMS,
		); err != nil {
			return nil, fmt.Errorf("monitoring %s stats: scan: %w", label, err)
		}
		stat.Key = firstNonEmptyString(stat.Key, "-")
		stat.LastSeenMS = stat.Metrics.LastEventMS
		out = append(out, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("monitoring %s stats: read rows: %w", label, err)
	}
	return out, nil
}

func scanDimensionMetric(row rowScanner, key *string, metrics *AggregateMetrics) error {
	return row.Scan(
		key,
		&metrics.EventCount,
		&metrics.FailedCount,
		&metrics.InputTokens,
		&metrics.OutputTokens,
		&metrics.ReasoningTokens,
		&metrics.CachedTokens,
		&metrics.CacheTokens,
		&metrics.CacheReadTokens,
		&metrics.CacheCreationTokens,
		&metrics.TotalTokens,
		&metrics.LatencySumMS,
		&metrics.LatencyCount,
		&metrics.TTFTSumMS,
		&metrics.TTFTCount,
		&metrics.FirstEventMS,
		&metrics.LastEventMS,
	)
}

func scanMonitoringMetrics(row rowScanner) (AggregateMetrics, error) {
	var metrics AggregateMetrics
	err := row.Scan(
		&metrics.EventCount,
		&metrics.FailedCount,
		&metrics.InputTokens,
		&metrics.OutputTokens,
		&metrics.ReasoningTokens,
		&metrics.CachedTokens,
		&metrics.CacheTokens,
		&metrics.CacheReadTokens,
		&metrics.CacheCreationTokens,
		&metrics.TotalTokens,
		&metrics.LatencySumMS,
		&metrics.LatencyCount,
		&metrics.TTFTSumMS,
		&metrics.TTFTCount,
		&metrics.FirstEventMS,
		&metrics.LastEventMS,
	)
	return metrics, err
}

func selectMonitoringEventColumnsSQL() string {
	return `select id,
		request_id,event_hash,timestamp_ms,timestamp,provider,executor_type,model,endpoint,method,path,
		auth_type,auth_index,source,source_hash,api_key_hash,account_snapshot,auth_label_snapshot,
		auth_file_snapshot,auth_provider_snapshot,auth_project_id_snapshot,auth_snapshot_at_ms,
		requested_model,resolved_model,reasoning_effort,service_tier,response_service_tier,input_tokens,output_tokens,
		reasoning_tokens,cached_tokens,cache_tokens,cache_read_tokens,cache_creation_tokens,total_tokens,
		latency_ms,ttft_ms,failed,fail_status_code,fail_summary,coalesce(response_metadata_json,''),
		header_quota_recover_at_ms,header_quota_used_percent,coalesce(header_quota_plan_type,''),
		coalesce(header_error_kind,''),coalesce(header_error_code,''),coalesce(header_trace_id,''),created_at_ms,
		` + analyticsCacheStatusSQL() + ` as cache_state
		from usage_events`
}

func scanMonitoringEvents(rows *sql.Rows, op string) ([]MonitoringEvent, error) {
	out := []MonitoringEvent{}
	for rows.Next() {
		event, err := scanMonitoringEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: read rows: %w", op, err)
	}
	return out, nil
}

func scanMonitoringEvent(row rowScanner) (MonitoringEvent, error) {
	var item MonitoringEvent
	var event Event
	var requestID, provider, executorType, endpoint, method, path, authType, authIndex, source, sourceHash, apiKeyHash sql.NullString
	var accountSnapshot, authLabelSnapshot, authFileSnapshot, authProviderSnapshot, authProjectIDSnapshot sql.NullString
	var requestedModel, resolvedModel, reasoningEffort, serviceTier, responseServiceTier, failSummary sql.NullString
	var responseMetadataJSON, quotaPlanType, errorKind, errorCode, traceID string
	var authSnapshotAt, latency, ttft, failStatusCode, quotaRecoverAt sql.NullInt64
	var quotaUsed sql.NullFloat64
	var failed int
	err := row.Scan(
		&item.ID,
		&requestID,
		&event.EventHash,
		&event.TimestampMS,
		&event.Timestamp,
		&provider,
		&executorType,
		&event.Model,
		&endpoint,
		&method,
		&path,
		&authType,
		&authIndex,
		&source,
		&sourceHash,
		&apiKeyHash,
		&accountSnapshot,
		&authLabelSnapshot,
		&authFileSnapshot,
		&authProviderSnapshot,
		&authProjectIDSnapshot,
		&authSnapshotAt,
		&requestedModel,
		&resolvedModel,
		&reasoningEffort,
		&serviceTier,
		&responseServiceTier,
		&event.InputTokens,
		&event.OutputTokens,
		&event.ReasoningTokens,
		&event.CachedTokens,
		&event.CacheTokens,
		&event.CacheReadTokens,
		&event.CacheCreationTokens,
		&event.TotalTokens,
		&latency,
		&ttft,
		&failed,
		&failStatusCode,
		&failSummary,
		&responseMetadataJSON,
		&quotaRecoverAt,
		&quotaUsed,
		&quotaPlanType,
		&errorKind,
		&errorCode,
		&traceID,
		&event.CreatedAtMS,
		&item.CacheState,
	)
	if err != nil {
		return MonitoringEvent{}, err
	}
	event.RequestID = requestID.String
	event.Provider = provider.String
	event.ExecutorType = executorType.String
	event.Endpoint = endpoint.String
	event.Method = method.String
	event.Path = path.String
	event.AuthType = authType.String
	event.AuthIndex = authIndex.String
	event.Source = source.String
	event.SourceHash = sourceHash.String
	event.APIKeyHash = apiKeyHash.String
	event.AccountSnapshot = accountSnapshot.String
	event.AuthLabelSnapshot = authLabelSnapshot.String
	event.AuthFileSnapshot = authFileSnapshot.String
	event.AuthProviderSnapshot = authProviderSnapshot.String
	event.AuthProjectIDSnapshot = authProjectIDSnapshot.String
	event.AuthSnapshotAtMS = authSnapshotAt.Int64
	event.RequestedModel = requestedModel.String
	event.ResolvedModel = resolvedModel.String
	event.ReasoningEffort = reasoningEffort.String
	event.ServiceTier = serviceTier.String
	event.ResponseServiceTier = responseServiceTier.String
	if latency.Valid {
		event.LatencyMS = &latency.Int64
	}
	if ttft.Valid {
		event.TTFTMS = &ttft.Int64
	}
	event.Failed = failed != 0
	if failStatusCode.Valid {
		event.FailStatusCode = int(failStatusCode.Int64)
	}
	event.FailSummary = failSummary.String
	event.ResponseMetadataJSON = responseMetadataJSON
	event.ResponseMetadata = ResponseHeaderMetadataFromJSON(responseMetadataJSON)
	event.HeaderQuotaRecoverAtMS = quotaRecoverAt.Int64
	if quotaUsed.Valid {
		event.HeaderQuotaUsedPercent = &quotaUsed.Float64
	}
	event.HeaderQuotaPlanType = quotaPlanType
	event.HeaderErrorKind = errorKind
	event.HeaderErrorCode = errorCode
	event.HeaderTraceID = traceID
	item.Event = event
	return item, nil
}

func analyticsProviderSQL() string {
	return "coalesce(nullif(provider,''), nullif(auth_provider_snapshot,''), '-')"
}

func analyticsAccountSQL() string {
	return "coalesce(nullif(auth_index,''), nullif(account_snapshot,''), nullif(auth_label_snapshot,''), nullif(auth_project_id_snapshot,''), nullif(api_key_hash,''), nullif(source_hash,''), nullif(source,''), '-')"
}

func analyticsKeySQL() string {
	return "coalesce(nullif(api_key_hash,''), nullif(source_hash,''), nullif(source,''), '-')"
}

func analyticsRequestTypeSQL() string {
	return "coalesce(nullif(endpoint,''), nullif(path,''), nullif(method,''), '-')"
}

func boolPtr(value bool) *bool {
	return &value
}
