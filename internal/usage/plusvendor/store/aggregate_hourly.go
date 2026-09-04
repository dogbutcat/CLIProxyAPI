package plusstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

const hourlyRollupName = "usage_hourly"

type RollupOptions struct {
	Name          string
	ThroughMS     int64
	BatchHours    int
	MaxBatches    int
	Owner         string
	LeaseDuration time.Duration
}

type RollupResult struct {
	Name         string
	FromMS       int64
	ToMS         int64
	CheckpointMS int64
	Batches      int
	Hours        int
	RawEvents    int
	RowsWritten  int
	Contended    bool
	Rebuilt      bool
}

func (s *Store) CatchUpHourlyRollups(ctx context.Context, opts RollupOptions) (RollupResult, error) {
	if s == nil || s.db == nil {
		return RollupResult{}, fmt.Errorf("catch up hourly rollups: store is nil")
	}
	opts = normalizeRollupOptions(opts)
	lease, err := s.acquireRollupLease(ctx, opts)
	if err != nil {
		return RollupResult{}, err
	}
	if !lease.acquired {
		return RollupResult{Name: opts.Name, CheckpointMS: lease.checkpointMS, Contended: true}, nil
	}
	defer s.releaseRollupLease(context.Background(), opts.Name, lease.owner)

	upperMS := floorHour(opts.ThroughMS)
	result := RollupResult{Name: opts.Name, CheckpointMS: lease.checkpointMS}
	for result.Batches < opts.MaxBatches {
		startMS, err := s.nextRollupStart(ctx, lease.checkpointMS, upperMS)
		if err != nil {
			return result, err
		}
		if startMS < 0 || startMS >= upperMS {
			break
		}
		endMS := startMS + int64(opts.BatchHours)*HourMS
		if endMS > upperMS {
			endMS = upperMS
		}
		batch, err := s.materializeHourlyRange(ctx, startMS, endMS, false)
		if err != nil {
			return result, err
		}
		if result.FromMS == 0 || startMS < result.FromMS {
			result.FromMS = startMS
		}
		if endMS > result.ToMS {
			result.ToMS = endMS
		}
		result.Batches++
		result.Hours += int((endMS - startMS) / HourMS)
		result.RawEvents += batch.RawEvents
		result.RowsWritten += batch.RowsWritten
		checkpointMS, _, err := s.rollupCheckpoint(ctx, opts.Name)
		if err != nil {
			return result, err
		}
		if checkpointMS > lease.checkpointMS {
			lease.checkpointMS = checkpointMS
			result.CheckpointMS = checkpointMS
		}
	}
	return result, nil
}

func (s *Store) RollupHourly(ctx context.Context, opts RollupOptions) (RollupResult, error) {
	return s.CatchUpHourlyRollups(ctx, opts)
}

func (s *Store) HourlyRollupCheckpoint(ctx context.Context) (int64, error) {
	cp, _, err := s.rollupCheckpoint(ctx, hourlyRollupName)
	return cp, err
}

func (s *Store) MergedHourlyAggregates(ctx context.Context, query AggregateQuery) ([]HourlyAggregate, error) {
	if err := validateAggregateQuery(query); err != nil {
		return nil, err
	}
	fullStart := ceilHour(query.FromMS)
	fullEnd := floorHour(query.ToMS)
	merged := map[string]HourlyAggregate{}
	if fullStart < fullEnd {
		rows, err := s.queryMaterializedHourly(ctx, query, fullStart, fullEnd)
		if err != nil {
			return nil, err
		}
		mergeAggregates(merged, rows)
	}
	rawRows, err := s.queryRawHourlyForMergedRead(ctx, query, fullStart, fullEnd)
	if err != nil {
		return nil, err
	}
	mergeAggregates(merged, rawRows)
	return sortedAggregates(merged), nil
}

func (s *Store) HourlyAggregates(ctx context.Context, query AggregateQuery) ([]HourlyAggregate, error) {
	return s.MergedHourlyAggregates(ctx, query)
}

func (s *Store) RawHourlyAggregates(ctx context.Context, query AggregateQuery) ([]HourlyAggregate, error) {
	if err := validateAggregateQuery(query); err != nil {
		return nil, err
	}
	events, err := s.rawEventsForQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	return aggregateEvents(events, query), nil
}

type materializeResult struct {
	RawEvents   int
	RowsWritten int
}

func (s *Store) materializeHourlyRange(ctx context.Context, fromMS, toMS int64, rebuild bool) (materializeResult, error) {
	if fromMS >= toMS {
		return materializeResult{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return materializeResult{}, fmt.Errorf("materialize hourly rollups: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	events, err := eventsBetweenTx(ctx, tx, fromMS, toMS)
	if err != nil {
		return materializeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `delete from usage_hourly_rollups where aggregate_schema_version = ? and hour_ms >= ? and hour_ms < ?`, AggregateSchemaVersion, fromMS, toMS); err != nil {
		return materializeResult{}, fmt.Errorf("materialize hourly rollups: clear range: %w", err)
	}
	aggregates := aggregateEvents(events, AggregateQuery{FromMS: fromMS, ToMS: toMS})
	rowsWritten, err := insertHourlyAggregatesTx(ctx, tx, aggregates)
	if err != nil {
		return materializeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `update usage_event_identity_ledger set aggregate_schema_version = ?, updated_at_ms = ? where raw_event_id is not null and timestamp_ms >= ? and timestamp_ms < ?`, AggregateSchemaVersion, nowMS(), fromMS, toMS); err != nil {
		return materializeResult{}, fmt.Errorf("materialize hourly rollups: mark ledger: %w", err)
	}
	if !rebuild {
		if err := advanceCheckpointTx(ctx, tx, hourlyRollupName, fromMS, toMS); err != nil {
			return materializeResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return materializeResult{}, fmt.Errorf("materialize hourly rollups: commit: %w", err)
	}
	return materializeResult{RawEvents: len(events), RowsWritten: rowsWritten}, nil
}

func (s *Store) nextRollupStart(ctx context.Context, checkpointMS, upperMS int64) (int64, error) {
	if upperMS <= 0 {
		return -1, nil
	}
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `select min(bucket_ms)
		from usage_event_identity_ledger
		where raw_event_id is not null
			and bucket_ms < ?
			and aggregate_schema_version < ?`, upperMS, AggregateSchemaVersion).Scan(&next)
	if err != nil {
		return -1, fmt.Errorf("find next rollup bucket: %w", err)
	}
	if next.Valid {
		return next.Int64, nil
	}
	if checkpointMS <= 0 {
		var first sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `select min(bucket_ms) from usage_event_identity_ledger where raw_event_id is not null and bucket_ms < ?`, upperMS).Scan(&first); err != nil {
			return -1, fmt.Errorf("find first rollup bucket: %w", err)
		}
		if first.Valid {
			checkpointMS = first.Int64
		} else {
			checkpointMS = upperMS
		}
	}
	if checkpointMS < upperMS {
		return checkpointMS, nil
	}
	return -1, nil
}

func aggregateEvents(events []Event, query AggregateQuery) []HourlyAggregate {
	byKey := map[string]HourlyAggregate{}
	for _, event := range events {
		dim := aggregateDimensionForEvent(event)
		if !matchesAggregateQuery(dim, query) {
			continue
		}
		hourMS := floorHour(event.TimestampMS)
		key := aggregateKey(hourMS, dim)
		agg := byKey[key]
		if agg.HourMS == 0 {
			agg.HourMS = hourMS
			agg.Dimension = dim
		}
		agg.Metrics.addEvent(event)
		byKey[key] = agg
	}
	return sortedAggregates(byKey)
}

func mergeAggregates(target map[string]HourlyAggregate, rows []HourlyAggregate) {
	for _, row := range rows {
		key := aggregateKey(row.HourMS, row.Dimension)
		current := target[key]
		if current.HourMS == 0 {
			current.HourMS = row.HourMS
			current.Dimension = row.Dimension
		}
		current.Metrics.addMetrics(row.Metrics)
		target[key] = current
	}
}

func sortedAggregates(rows map[string]HourlyAggregate) []HourlyAggregate {
	out := make([]HourlyAggregate, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HourMS != out[j].HourMS {
			return out[i].HourMS < out[j].HourMS
		}
		return dimensionKey(out[i].Dimension) < dimensionKey(out[j].Dimension)
	})
	return out
}

func eventsBetweenTx(ctx context.Context, tx *sql.Tx, fromMS, toMS int64) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, selectEventColumnsSQL()+` where timestamp_ms >= ? and timestamp_ms < ? order by timestamp_ms asc,id asc`, fromMS, toMS)
	if err != nil {
		return nil, fmt.Errorf("materialize hourly rollups: query raw events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows, "materialize hourly rollups")
}

func (s *Store) rawEventsForQuery(ctx context.Context, query AggregateQuery) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, selectEventColumnsSQL()+` where timestamp_ms >= ? and timestamp_ms < ? order by timestamp_ms asc,id asc`, query.FromMS, query.ToMS)
	if err != nil {
		return nil, fmt.Errorf("query raw hourly aggregates: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows, "query raw hourly aggregates")
}

func (s *Store) queryRawHourlyForMergedRead(ctx context.Context, query AggregateQuery, fullStart, fullEnd int64) ([]HourlyAggregate, error) {
	rows, err := s.db.QueryContext(ctx, selectEventColumnsSQL()+`
		where usage_events.timestamp_ms >= ? and usage_events.timestamp_ms < ?
			and (usage_events.timestamp_ms < ? or usage_events.timestamp_ms >= ? or coalesce((select l.aggregate_schema_version from usage_event_identity_ledger l where l.event_hash = usage_events.event_hash), 0) < ?)
		order by usage_events.timestamp_ms asc, usage_events.id asc`, query.FromMS, query.ToMS, fullStart, fullEnd, AggregateSchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("query merged raw hourly aggregates: %w", err)
	}
	defer rows.Close()
	events, err := scanEvents(rows, "query merged raw hourly aggregates")
	if err != nil {
		return nil, err
	}
	return aggregateEvents(events, query), nil
}

func (s *Store) queryMaterializedHourly(ctx context.Context, query AggregateQuery, fromMS, toMS int64) ([]HourlyAggregate, error) {
	rows, err := s.db.QueryContext(ctx, `select
		hour_ms,provider,executor_type,model,requested_model,resolved_model,endpoint,auth_type,auth_index,
		account_id,source,source_hash,api_key_hash,account_snapshot,auth_label_snapshot,auth_file_snapshot,
		auth_provider_snapshot,auth_project_id_snapshot,failed,fail_status_code,cache_status,
		event_count,failed_count,input_tokens,output_tokens,reasoning_tokens,cached_tokens,cache_tokens,
		cache_read_tokens,cache_creation_tokens,total_tokens,latency_sum_ms,latency_count,ttft_sum_ms,
		ttft_count,first_event_ms,last_event_ms
		from usage_hourly_rollups
		where aggregate_schema_version = ? and hour_ms >= ? and hour_ms < ?
		order by hour_ms asc, dimension_key asc`, AggregateSchemaVersion, fromMS, toMS)
	if err != nil {
		return nil, fmt.Errorf("query materialized hourly aggregates: %w", err)
	}
	defer rows.Close()
	out := []HourlyAggregate{}
	for rows.Next() {
		var row HourlyAggregate
		var failed int
		if err := rows.Scan(
			&row.HourMS,
			&row.Dimension.Provider,
			&row.Dimension.ExecutorType,
			&row.Dimension.Model,
			&row.Dimension.RequestedModel,
			&row.Dimension.ResolvedModel,
			&row.Dimension.Endpoint,
			&row.Dimension.AuthType,
			&row.Dimension.AuthIndex,
			&row.Dimension.AccountID,
			&row.Dimension.Source,
			&row.Dimension.SourceHash,
			&row.Dimension.APIKeyHash,
			&row.Dimension.AccountSnapshot,
			&row.Dimension.AuthLabelSnapshot,
			&row.Dimension.AuthFileSnapshot,
			&row.Dimension.AuthProviderSnapshot,
			&row.Dimension.AuthProjectIDSnapshot,
			&failed,
			&row.Dimension.FailStatusCode,
			&row.Dimension.CacheStatus,
			&row.Metrics.EventCount,
			&row.Metrics.FailedCount,
			&row.Metrics.InputTokens,
			&row.Metrics.OutputTokens,
			&row.Metrics.ReasoningTokens,
			&row.Metrics.CachedTokens,
			&row.Metrics.CacheTokens,
			&row.Metrics.CacheReadTokens,
			&row.Metrics.CacheCreationTokens,
			&row.Metrics.TotalTokens,
			&row.Metrics.LatencySumMS,
			&row.Metrics.LatencyCount,
			&row.Metrics.TTFTSumMS,
			&row.Metrics.TTFTCount,
			&row.Metrics.FirstEventMS,
			&row.Metrics.LastEventMS,
		); err != nil {
			return nil, fmt.Errorf("query materialized hourly aggregates: scan: %w", err)
		}
		row.Dimension.Failed = failed != 0
		if matchesAggregateQuery(row.Dimension, query) {
			out = append(out, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query materialized hourly aggregates: read rows: %w", err)
	}
	return out, nil
}

func insertHourlyAggregatesTx(ctx context.Context, tx *sql.Tx, aggregates []HourlyAggregate) (int, error) {
	stmt, err := tx.PrepareContext(ctx, `insert into usage_hourly_rollups (
		hour_ms,aggregate_schema_version,dimension_key,provider,executor_type,model,requested_model,
		resolved_model,endpoint,auth_type,auth_index,account_id,source,source_hash,api_key_hash,
		account_snapshot,auth_label_snapshot,auth_file_snapshot,auth_provider_snapshot,auth_project_id_snapshot,
		failed,fail_status_code,cache_status,event_count,failed_count,input_tokens,output_tokens,
		reasoning_tokens,cached_tokens,cache_tokens,cache_read_tokens,cache_creation_tokens,total_tokens,
		latency_sum_ms,latency_count,ttft_sum_ms,ttft_count,first_event_ms,last_event_ms,updated_at_ms
	) values (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, fmt.Errorf("insert hourly aggregates: prepare: %w", err)
	}
	defer stmt.Close()
	for _, row := range aggregates {
		if _, err := stmt.ExecContext(ctx,
			row.HourMS,
			AggregateSchemaVersion,
			dimensionKey(row.Dimension),
			row.Dimension.Provider,
			row.Dimension.ExecutorType,
			row.Dimension.Model,
			row.Dimension.RequestedModel,
			row.Dimension.ResolvedModel,
			row.Dimension.Endpoint,
			row.Dimension.AuthType,
			row.Dimension.AuthIndex,
			row.Dimension.AccountID,
			row.Dimension.Source,
			row.Dimension.SourceHash,
			row.Dimension.APIKeyHash,
			row.Dimension.AccountSnapshot,
			row.Dimension.AuthLabelSnapshot,
			row.Dimension.AuthFileSnapshot,
			row.Dimension.AuthProviderSnapshot,
			row.Dimension.AuthProjectIDSnapshot,
			boolInt(row.Dimension.Failed),
			row.Dimension.FailStatusCode,
			row.Dimension.CacheStatus,
			row.Metrics.EventCount,
			row.Metrics.FailedCount,
			row.Metrics.InputTokens,
			row.Metrics.OutputTokens,
			row.Metrics.ReasoningTokens,
			row.Metrics.CachedTokens,
			row.Metrics.CacheTokens,
			row.Metrics.CacheReadTokens,
			row.Metrics.CacheCreationTokens,
			row.Metrics.TotalTokens,
			row.Metrics.LatencySumMS,
			row.Metrics.LatencyCount,
			row.Metrics.TTFTSumMS,
			row.Metrics.TTFTCount,
			row.Metrics.FirstEventMS,
			row.Metrics.LastEventMS,
			nowMS(),
		); err != nil {
			return 0, fmt.Errorf("insert hourly aggregates: insert: %w", err)
		}
	}
	return len(aggregates), nil
}

func matchesAggregateQuery(d AggregateDimensions, query AggregateQuery) bool {
	if query.Provider != "" && d.Provider != query.Provider {
		return false
	}
	if query.Model != "" && d.Model != query.Model && d.ResolvedModel != query.Model {
		return false
	}
	if query.AccountID != "" && d.AccountID != query.AccountID {
		return false
	}
	if query.CacheStatus != "" && d.CacheStatus != query.CacheStatus {
		return false
	}
	if query.Failed != nil && d.Failed != *query.Failed {
		return false
	}
	return true
}

func validateAggregateQuery(query AggregateQuery) error {
	if query.FromMS >= query.ToMS {
		return fmt.Errorf("aggregate query: from_ms must be before to_ms")
	}
	return nil
}

func normalizeRollupOptions(opts RollupOptions) RollupOptions {
	if opts.Name == "" {
		opts.Name = hourlyRollupName
	}
	if opts.ThroughMS <= 0 {
		opts.ThroughMS = nowMS()
	}
	if opts.BatchHours <= 0 {
		opts.BatchHours = 24
	}
	if opts.MaxBatches <= 0 {
		opts.MaxBatches = 1024
	}
	if opts.Owner == "" {
		opts.Owner = fmt.Sprintf("rollup-%d", time.Now().UnixNano())
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = 5 * time.Minute
	}
	return opts
}

func floorHour(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms - ms%HourMS
}

func ceilHour(ms int64) int64 {
	floor := floorHour(ms)
	if floor == ms {
		return floor
	}
	return floor + HourMS
}
