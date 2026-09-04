package plusstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const ledgerBucketMS = int64(60 * 60 * 1000)

type UsageEventRepository interface {
	InsertBatch(context.Context, []Event) (InsertResult, error)
	ListRecent(context.Context, int) ([]Event, error)
	EventsBetween(context.Context, int64, int64, int) ([]Event, error)
	Count(context.Context) (int64, error)
}

type usageEventRepository struct {
	db *sql.DB
}

type rowScanner interface {
	Scan(...any) error
}

func NewUsageEventRepository(db *sql.DB) UsageEventRepository {
	return &usageEventRepository{db: db}
}

func (r *usageEventRepository) InsertBatch(ctx context.Context, events []Event) (InsertResult, error) {
	if len(events) == 0 {
		return InsertResult{}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return InsertResult{}, fmt.Errorf("insert usage events: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	claimStmt, err := tx.PrepareContext(ctx, `insert or ignore into usage_event_identity_ledger(
		event_hash, timestamp_ms, bucket_ms, aggregate_schema_version, first_seen_at_ms, updated_at_ms
	) values(?,?,?,0,?,?)`)
	if err != nil {
		return InsertResult{}, fmt.Errorf("insert usage events: prepare identity ledger claim: %w", err)
	}
	defer claimStmt.Close()
	stmt, err := tx.PrepareContext(ctx, `insert or ignore into usage_events (
		request_id,event_hash,timestamp_ms,timestamp,provider,executor_type,model,endpoint,method,path,
		auth_type,auth_index,source,source_hash,api_key_hash,account_snapshot,auth_label_snapshot,
		auth_file_snapshot,auth_provider_snapshot,auth_project_id_snapshot,auth_snapshot_at_ms,
			requested_model,resolved_model,reasoning_effort,service_tier,response_service_tier,input_tokens,output_tokens,
		reasoning_tokens,cached_tokens,cache_tokens,cache_read_tokens,cache_creation_tokens,total_tokens,
		latency_ms,ttft_ms,failed,fail_status_code,fail_summary,response_metadata_json,
		header_quota_recover_at_ms,header_quota_used_percent,header_quota_plan_type,header_error_kind,
		header_error_code,header_trace_id,fail_body,raw_json,created_at_ms
		) values (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return InsertResult{}, fmt.Errorf("insert usage events: prepare insert: %w", err)
	}
	defer stmt.Close()
	out := InsertResult{}
	for _, event := range events {
		normalizeEventForInsert(&event)
		firstSeenAtMS := identityLedgerFirstSeenAtMS(event)
		claimRes, err := claimStmt.ExecContext(ctx, event.EventHash, event.TimestampMS, identityLedgerBucketMS(event.TimestampMS), firstSeenAtMS, firstSeenAtMS)
		if err != nil {
			return InsertResult{}, fmt.Errorf("insert usage events: claim identity ledger: %w", err)
		}
		claimed, err := claimRes.RowsAffected()
		if err != nil {
			return InsertResult{}, fmt.Errorf("insert usage events: read identity ledger claim rows affected: %w", err)
		}
		if claimed == 0 {
			if err := attachIdentityLedgerToExistingRaw(ctx, tx, event.EventHash, firstSeenAtMS); err != nil && err != sql.ErrNoRows {
				return InsertResult{}, fmt.Errorf("insert usage events: attach existing identity ledger: %w", err)
			}
			out.Skipped++
			continue
		}
		md, recoverAt, used, plan, kind, code, trace := responseHeaderDerivedForInsert(event)
		failSource := event.FailSummary
		if failSource == "" {
			failSource = event.FailBody
		}
		res, err := stmt.ExecContext(
			ctx,
			nullString(event.RequestID),
			event.EventHash,
			event.TimestampMS,
			event.Timestamp,
			nullString(event.Provider),
			nullString(event.ExecutorType),
			event.Model,
			nullString(event.Endpoint),
			nullString(event.Method),
			nullString(event.Path),
			nullString(event.AuthType),
			nullString(event.AuthIndex),
			nullString(event.Source),
			nullString(event.SourceHash),
			nullString(event.APIKeyHash),
			nullString(event.AccountSnapshot),
			nullString(event.AuthLabelSnapshot),
			nullString(event.AuthFileSnapshot),
			nullString(event.AuthProviderSnapshot),
			nullString(event.AuthProjectIDSnapshot),
			nullPositiveInt64(event.AuthSnapshotAtMS),
			nullString(event.RequestedModel),
			nullString(event.ResolvedModel),
			nullString(event.ReasoningEffort),
			nullString(event.ServiceTier),
			nullString(event.ResponseServiceTier),
			event.InputTokens,
			event.OutputTokens,
			event.ReasoningTokens,
			event.CachedTokens,
			event.CacheTokens,
			event.CacheReadTokens,
			event.CacheCreationTokens,
			event.TotalTokens,
			nullInt(event.LatencyMS),
			nullInt(event.TTFTMS),
			boolInt(event.Failed),
			nullPositiveInt64(int64(event.FailStatusCode)),
			nullString(FailSummaryFromBody(failSource)),
			nullString(md),
			nullPositiveInt64(recoverAt),
			nullFloat(used),
			nullString(plan),
			nullString(kind),
			nullString(code),
			nullString(trace),
			nullString(FailSummaryFromBody(event.FailBody)),
			nullString(SafeRawJSON(event.RawJSON)),
			event.CreatedAtMS,
		)
		if err != nil {
			return InsertResult{}, fmt.Errorf("insert usage events: insert event: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return InsertResult{}, fmt.Errorf("insert usage events: read rows affected: %w", err)
		}
		if err := attachIdentityLedgerToExistingRaw(ctx, tx, event.EventHash, firstSeenAtMS); err != nil {
			return InsertResult{}, fmt.Errorf("insert usage events: attach identity ledger: %w", err)
		}
		if rows > 0 {
			out.Inserted++
			out.InsertedEventHashes = append(out.InsertedEventHashes, event.EventHash)
		} else {
			out.Skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return InsertResult{}, fmt.Errorf("insert usage events: commit: %w", err)
	}
	return out, nil
}

func (r *usageEventRepository) ListRecent(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50000
	}
	rows, err := r.db.QueryContext(ctx, selectEventColumnsSQL()+` order by timestamp_ms desc,id desc limit ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent usage events: query: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows, "list recent usage events")
}

func (r *usageEventRepository) EventsBetween(ctx context.Context, fromMS, toMS int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 50000 {
		limit = 50000
	}
	rows, err := r.db.QueryContext(ctx, selectEventColumnsSQL()+` where timestamp_ms >= ? and timestamp_ms < ? order by timestamp_ms desc,id desc limit ?`, fromMS, toMS, limit)
	if err != nil {
		return nil, fmt.Errorf("list usage events between: query: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows, "list usage events between")
}

func (r *usageEventRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count usage events: %w", err)
	}
	return count, nil
}

func normalizeEventForInsert(event *Event) {
	if event.EventHash == "" {
		event.EventHash = BuildEventHash(*event)
	}
	if event.Model == "" {
		event.Model = "-"
	}
	if event.Timestamp == "" && event.TimestampMS > 0 {
		event.Timestamp = sqlTimestamp(event.TimestampMS)
	}
	if event.TimestampMS <= 0 {
		event.TimestampMS = nowMS()
	}
	if event.CreatedAtMS <= 0 {
		event.CreatedAtMS = event.TimestampMS
	}
}

func identityLedgerBucketMS(timestampMS int64) int64 {
	return timestampMS - timestampMS%ledgerBucketMS
}

func identityLedgerFirstSeenAtMS(event Event) int64 {
	if event.CreatedAtMS > 0 {
		return event.CreatedAtMS
	}
	if event.TimestampMS > 0 {
		return event.TimestampMS
	}
	return 0
}

func attachIdentityLedgerToExistingRaw(ctx context.Context, tx *sql.Tx, eventHash string, updatedAtMS int64) error {
	var rawEventID int64
	var timestampMS int64
	err := tx.QueryRowContext(ctx, `select id, timestamp_ms from usage_events where event_hash = ?`, eventHash).Scan(&rawEventID, &timestampMS)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `update usage_event_identity_ledger set
		raw_event_id = ?,
		timestamp_ms = ?,
		bucket_ms = ?,
		first_seen_at_ms = coalesce((select case when created_at_ms > 0 then created_at_ms end from usage_events where event_hash = ?), first_seen_at_ms),
		updated_at_ms = ?
	where event_hash = ?`, rawEventID, timestampMS, identityLedgerBucketMS(timestampMS), eventHash, updatedAtMS, eventHash)
	return err
}

func selectEventColumnsSQL() string {
	return `select
		request_id,event_hash,timestamp_ms,timestamp,provider,executor_type,model,endpoint,method,path,
		auth_type,auth_index,source,source_hash,api_key_hash,account_snapshot,auth_label_snapshot,
		auth_file_snapshot,auth_provider_snapshot,auth_project_id_snapshot,auth_snapshot_at_ms,
			requested_model,resolved_model,reasoning_effort,service_tier,response_service_tier,input_tokens,output_tokens,
		reasoning_tokens,cached_tokens,cache_tokens,cache_read_tokens,cache_creation_tokens,total_tokens,
		latency_ms,ttft_ms,failed,fail_status_code,fail_summary,coalesce(response_metadata_json,''),
		header_quota_recover_at_ms,header_quota_used_percent,coalesce(header_quota_plan_type,''),
		coalesce(header_error_kind,''),coalesce(header_error_code,''),coalesce(header_trace_id,''),created_at_ms
	from usage_events`
}

func scanEvents(rows *sql.Rows, op string) ([]Event, error) {
	out := []Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
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

func scanEvent(row rowScanner) (Event, error) {
	var event Event
	var requestID, provider, executorType, endpoint, method, path, authType, authIndex, source, sourceHash, apiKeyHash sql.NullString
	var accountSnapshot, authLabelSnapshot, authFileSnapshot, authProviderSnapshot, authProjectIDSnapshot sql.NullString
	var requestedModel, resolvedModel, reasoningEffort, serviceTier, responseServiceTier, failSummary sql.NullString
	var responseMetadataJSON, quotaPlanType, errorKind, errorCode, traceID string
	var authSnapshotAt, latency, ttft, failStatusCode, quotaRecoverAt sql.NullInt64
	var quotaUsed sql.NullFloat64
	var failed int
	err := row.Scan(
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
	)
	if err != nil {
		return Event{}, err
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
	event.FailStatusCode = int(failStatusCode.Int64)
	event.FailSummary = failSummary.String
	event.ResponseMetadataJSON = responseMetadataJSON
	event.ResponseMetadata = ResponseHeaderMetadataFromJSON(responseMetadataJSON)
	event.HeaderQuotaRecoverAtMS = quotaRecoverAt.Int64
	if quotaUsed.Valid {
		value := quotaUsed.Float64
		event.HeaderQuotaUsedPercent = &value
	}
	event.HeaderQuotaPlanType = quotaPlanType
	event.HeaderErrorKind = errorKind
	event.HeaderErrorCode = errorCode
	event.HeaderTraceID = traceID
	event.Failed = failed != 0
	if latency.Valid {
		value := latency.Int64
		event.LatencyMS = &value
	}
	if ttft.Valid {
		value := ttft.Int64
		event.TTFTMS = &value
	}
	return event, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sqlTimestamp(timestampMS int64) string {
	return time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano)
}

func nowMS() int64 {
	return time.Now().UnixMilli()
}

func responseHeaderDerivedForInsert(event Event) (string, int64, *float64, string, string, string, string) {
	md := event.ResponseMetadataJSON
	recoverAt := event.HeaderQuotaRecoverAtMS
	used := event.HeaderQuotaUsedPercent
	plan := event.HeaderQuotaPlanType
	kind := event.HeaderErrorKind
	code := event.HeaderErrorCode
	trace := event.HeaderTraceID
	if event.ResponseMetadata == nil && md != "" {
		event.ResponseMetadata = ResponseHeaderMetadataFromJSON(md)
	}
	derived := DeriveResponseHeaderMetadata(event.ResponseMetadata)
	if derived.MetadataJSON != "" {
		md = derived.MetadataJSON
	}
	if md == "" {
		md = derived.MetadataJSON
	}
	if recoverAt == 0 {
		recoverAt = derived.QuotaRecoverAtMS
	}
	if used == nil {
		used = derived.QuotaUsedPercent
	}
	if plan == "" {
		plan = derived.QuotaPlanType
	}
	if kind == "" {
		kind = derived.ErrorKind
	}
	if code == "" {
		code = derived.ErrorCode
	}
	if trace == "" {
		trace = derived.TraceID
	}
	return md, recoverAt, used, plan, kind, code, trace
}
