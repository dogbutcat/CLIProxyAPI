package plusstore

import (
	"fmt"
	"strings"
)

const DefaultMonitoringLimit = 100
const MaxMonitoringLimit = 500

type AnalyticsFilter struct {
	FromMS           int64
	ToMS             int64
	Model            string
	Models           []string
	Provider         string
	Providers        []string
	Account          string
	Accounts         []string
	CredentialIDs    []string
	AuthFiles        []string
	AuthIndices      []string
	APIKeyHash       string
	APIKeyHashes     []string
	SourceHashes     []string
	ProjectIDs       []string
	RequestType      string
	RequestTypes     []string
	HeaderErrorKinds []string
	HeaderErrorCodes []string
	HeaderQuotaPlans []string
	HeaderTraceIDs   []string
	Search           string
	CacheStatus      string
	MinLatencyMS     int64
	Failed           *bool
}

type AnalyticsEventCursor struct {
	TimestampMS int64
	ID          int64
}

type AnalyticsEventPageQuery struct {
	Filter AnalyticsFilter
	Limit  int
	Cursor *AnalyticsEventCursor
}

type analyticsFilterDimension string

const (
	analyticsFilterModel       analyticsFilterDimension = "model"
	analyticsFilterProvider    analyticsFilterDimension = "provider"
	analyticsFilterAccount     analyticsFilterDimension = "account"
	analyticsFilterRequestType analyticsFilterDimension = "request_type"
	analyticsFilterCacheStatus analyticsFilterDimension = "cache_status"
	analyticsFilterFailed      analyticsFilterDimension = "failed"
)

func NormalizeAnalyticsFilter(filter AnalyticsFilter) AnalyticsFilter {
	filter.Model = normalizeAnalyticsText(filter.Model)
	filter.Models = normalizeAnalyticsList(filter.Models, false)
	filter.Provider = normalizeAnalyticsText(filter.Provider)
	filter.Providers = normalizeAnalyticsList(filter.Providers, false)
	filter.Account = normalizeAnalyticsText(filter.Account)
	filter.Accounts = normalizeAnalyticsList(filter.Accounts, false)
	filter.CredentialIDs = normalizeAnalyticsList(filter.CredentialIDs, false)
	filter.AuthFiles = normalizeAnalyticsList(filter.AuthFiles, false)
	filter.AuthIndices = normalizeAnalyticsList(filter.AuthIndices, false)
	filter.APIKeyHash = normalizeAnalyticsText(filter.APIKeyHash)
	filter.APIKeyHashes = normalizeAnalyticsList(filter.APIKeyHashes, true)
	filter.SourceHashes = normalizeAnalyticsList(filter.SourceHashes, true)
	filter.ProjectIDs = normalizeAnalyticsList(filter.ProjectIDs, false)
	filter.RequestType = normalizeAnalyticsText(filter.RequestType)
	filter.RequestTypes = normalizeAnalyticsList(filter.RequestTypes, false)
	filter.HeaderErrorKinds = normalizeAnalyticsList(filter.HeaderErrorKinds, true)
	filter.HeaderErrorCodes = normalizeAnalyticsList(filter.HeaderErrorCodes, true)
	filter.HeaderQuotaPlans = normalizeAnalyticsList(filter.HeaderQuotaPlans, true)
	filter.HeaderTraceIDs = normalizeAnalyticsList(filter.HeaderTraceIDs, true)
	filter.Search = normalizeAnalyticsText(filter.Search)
	filter.CacheStatus = normalizeAnalyticsCacheStatus(filter.CacheStatus)
	if filter.FromMS > 0 && filter.ToMS > 0 && filter.FromMS > filter.ToMS {
		filter.FromMS, filter.ToMS = filter.ToMS, filter.FromMS
	}
	return filter
}

func NormalizeMonitoringLimit(limit int) int {
	if limit <= 0 {
		return DefaultMonitoringLimit
	}
	if limit > MaxMonitoringLimit {
		return MaxMonitoringLimit
	}
	return limit
}

func (filter AnalyticsFilter) withoutDimension(dimension analyticsFilterDimension) AnalyticsFilter {
	switch dimension {
	case analyticsFilterModel:
		filter.Model = ""
		filter.Models = nil
	case analyticsFilterProvider:
		filter.Provider = ""
		filter.Providers = nil
	case analyticsFilterAccount:
		filter.Account = ""
		filter.Accounts = nil
		filter.CredentialIDs = nil
		filter.AuthFiles = nil
		filter.AuthIndices = nil
		filter.APIKeyHash = ""
		filter.APIKeyHashes = nil
		filter.SourceHashes = nil
		filter.ProjectIDs = nil
	case analyticsFilterRequestType:
		filter.RequestType = ""
		filter.RequestTypes = nil
	case analyticsFilterCacheStatus:
		filter.CacheStatus = ""
	case analyticsFilterFailed:
		filter.Failed = nil
	}
	return filter
}

type analyticsWhere struct {
	sql  string
	args []any
}

func buildAnalyticsWhere(filter AnalyticsFilter) analyticsWhere {
	filter = NormalizeAnalyticsFilter(filter)
	clauses := []string{"1=1"}
	args := []any{}
	if filter.FromMS > 0 {
		clauses = append(clauses, "timestamp_ms >= ?")
		args = append(args, filter.FromMS)
	}
	if filter.ToMS > 0 {
		clauses = append(clauses, "timestamp_ms < ?")
		args = append(args, filter.ToMS)
	}
	if values := analyticsValues(filter.Model, filter.Models, true); len(values) > 0 {
		columns := []string{"model", "requested_model", "resolved_model"}
		clauses = append(clauses, analyticsAnyLowerClause(columns, values))
		args = append(args, anySliceRepeat(values, len(columns))...)
	}
	if values := analyticsValues(filter.Provider, filter.Providers, true); len(values) > 0 {
		columns := []string{"provider", "auth_provider_snapshot"}
		clauses = append(clauses, analyticsAnyLowerClause(columns, values))
		args = append(args, anySliceRepeat(values, len(columns))...)
	}
	if values := analyticsValues(filter.Account, filter.Accounts, true); len(values) > 0 {
		columns := []string{
			"auth_index", "account_snapshot", "auth_label_snapshot", "auth_file_snapshot",
			"auth_project_id_snapshot", "api_key_hash", "source_hash", "source",
		}
		clauses = append(clauses, analyticsAnyLowerClause(columns, values))
		args = append(args, anySliceRepeat(values, len(columns))...)
	}
	if len(filter.CredentialIDs) > 0 {
		values := lowerValues(filter.CredentialIDs)
		columns := []string{"auth_index", "auth_file_snapshot", "source", "source_hash"}
		clauses = append(clauses, analyticsAnyLowerClause(columns, values))
		args = append(args, anySliceRepeat(values, len(columns))...)
	}
	if len(filter.AuthFiles) > 0 {
		values := lowerValues(filter.AuthFiles)
		columns := []string{"auth_file_snapshot", "source"}
		clauses = append(clauses, analyticsAnyLowerClause(columns, values))
		args = append(args, anySliceRepeat(values, len(columns))...)
	}
	if len(filter.AuthIndices) > 0 {
		values := lowerValues(filter.AuthIndices)
		clauses = append(clauses, inLowerClause("auth_index", len(values)))
		args = append(args, anySlice(values)...)
	}
	if values := analyticsValues(filter.APIKeyHash, filter.APIKeyHashes, true); len(values) > 0 {
		columns := []string{"api_key_hash", "source_hash", "source"}
		clauses = append(clauses, analyticsAnyLowerClause(columns, values))
		args = append(args, anySliceRepeat(values, len(columns))...)
	}
	if len(filter.SourceHashes) > 0 {
		values := lowerValues(filter.SourceHashes)
		clauses = append(clauses, inLowerClause("source_hash", len(values)))
		args = append(args, anySlice(values)...)
	}
	if len(filter.ProjectIDs) > 0 {
		values := lowerValues(filter.ProjectIDs)
		clauses = append(clauses, inLowerClause("auth_project_id_snapshot", len(values)))
		args = append(args, anySlice(values)...)
	}
	if values := analyticsValues(filter.RequestType, filter.RequestTypes, true); len(values) > 0 {
		columns := []string{"endpoint", "path", "method"}
		clauses = append(clauses, analyticsAnyLowerClause(columns, values))
		args = append(args, anySliceRepeat(values, len(columns))...)
	}
	if filter.Search != "" {
		clauses = append(clauses, `(
			lower(coalesce(request_id,'')) like ? or
			lower(coalesce(model,'')) like ? or
			lower(coalesce(requested_model,'')) like ? or
			lower(coalesce(resolved_model,'')) like ? or
			lower(coalesce(endpoint,'')) like ? or
			lower(coalesce(path,'')) like ? or
			lower(coalesce(auth_index,'')) like ? or
			lower(coalesce(source,'')) like ? or
			lower(coalesce(source_hash,'')) like ? or
			lower(coalesce(api_key_hash,'')) like ? or
			lower(coalesce(account_snapshot,'')) like ? or
			lower(coalesce(auth_label_snapshot,'')) like ? or
			lower(coalesce(auth_project_id_snapshot,'')) like ? or
			lower(coalesce(fail_summary,'')) like ? or
			lower(coalesce(header_error_kind,'')) like ? or
			lower(coalesce(header_error_code,'')) like ? or
			lower(coalesce(header_trace_id,'')) like ?
		)`)
		pattern := "%" + strings.ToLower(filter.Search) + "%"
		for i := 0; i < 17; i++ {
			args = append(args, pattern)
		}
	}
	if len(filter.HeaderErrorKinds) > 0 {
		values := lowerValues(filter.HeaderErrorKinds)
		clauses = append(clauses, inLowerClause("header_error_kind", len(values)))
		args = append(args, anySlice(values)...)
	}
	if len(filter.HeaderErrorCodes) > 0 {
		values := lowerValues(filter.HeaderErrorCodes)
		clauses = append(clauses, inLowerClause("header_error_code", len(values)))
		args = append(args, anySlice(values)...)
	}
	if len(filter.HeaderQuotaPlans) > 0 {
		values := lowerValues(filter.HeaderQuotaPlans)
		clauses = append(clauses, inLowerClause("header_quota_plan_type", len(values)))
		args = append(args, anySlice(values)...)
	}
	if len(filter.HeaderTraceIDs) > 0 {
		values := lowerValues(filter.HeaderTraceIDs)
		clauses = append(clauses, inLowerClause("header_trace_id", len(values)))
		args = append(args, anySlice(values)...)
	}
	if filter.CacheStatus != "" {
		clauses = append(clauses, analyticsCacheStatusSQL()+" = ?")
		args = append(args, filter.CacheStatus)
	}
	if filter.MinLatencyMS > 0 {
		clauses = append(clauses, "latency_ms >= ?")
		args = append(args, filter.MinLatencyMS)
	}
	if filter.Failed != nil {
		clauses = append(clauses, "failed = ?")
		if *filter.Failed {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	return analyticsWhere{sql: " where " + strings.Join(clauses, " and "), args: args}
}

func addAnalyticsCursor(where analyticsWhere, cursor *AnalyticsEventCursor) analyticsWhere {
	if cursor == nil || cursor.TimestampMS <= 0 || cursor.ID <= 0 {
		return where
	}
	where.sql += " and (timestamp_ms < ? or (timestamp_ms = ? and id < ?))"
	where.args = append(where.args, cursor.TimestampMS, cursor.TimestampMS, cursor.ID)
	return where
}

func analyticsCacheStatusSQL() string {
	return `case
		when coalesce(cache_creation_tokens,0) > 0 and (coalesce(cache_read_tokens,0) > 0 or coalesce(cache_tokens,0) > 0 or coalesce(cached_tokens,0) > 0) then 'read_write'
		when coalesce(cache_creation_tokens,0) > 0 then 'write'
		when coalesce(cache_read_tokens,0) > 0 or coalesce(cache_tokens,0) > 0 or coalesce(cached_tokens,0) > 0 then 'read'
		else 'none'
	end`
}

func analyticsModelSQL() string {
	return analyticsGroupValueSQL("resolved_model", "model", "requested_model")
}

func normalizeAnalyticsText(value string) string {
	return strings.TrimSpace(value)
}

func normalizeAnalyticsList(values []string, lower bool) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || strings.EqualFold(trimmed, "all") {
			continue
		}
		if lower {
			trimmed = strings.ToLower(trimmed)
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

func normalizeAnalyticsCacheStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return ""
	case "hit", "cache_hit", "cache-hit", "read":
		return "read"
	case "miss", "cache_miss", "cache-miss", "none":
		return "none"
	case "write", "creation", "cache_write", "cache-write":
		return "write"
	case "read_write", "read-write", "both":
		return "read_write"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func analyticsValues(single string, many []string, lower bool) []string {
	values := append([]string{}, many...)
	if strings.TrimSpace(single) != "" {
		values = append(values, single)
	}
	return normalizeAnalyticsList(values, lower)
}

func lowerValues(values []string) []string {
	return normalizeAnalyticsList(values, true)
}

func anySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func anySliceRepeat(values []string, times int) []any {
	out := make([]any, 0, len(values)*times)
	for i := 0; i < times; i++ {
		out = append(out, anySlice(values)...)
	}
	return out
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func inLowerClause(column string, count int) string {
	return "lower(coalesce(" + column + ",'')) in (" + placeholders(count) + ")"
}

func analyticsAnyLowerClause(columns []string, values []string) string {
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		parts = append(parts, inLowerClause(column, len(values)))
	}
	return "(" + strings.Join(parts, " or ") + ")"
}

func analyticsGroupValueSQL(values ...string) string {
	parts := make([]string, 0, len(values)+1)
	for _, value := range values {
		parts = append(parts, "nullif("+value+",'')")
	}
	parts = append(parts, "'-'")
	return "coalesce(" + strings.Join(parts, ", ") + ")"
}

func analyticsScanContext(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
