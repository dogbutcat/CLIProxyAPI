package plusstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

const latestSchemaVersion = 6

type migration struct {
	version int
	name    string
	sql     []string
}

var migrations = []migration{
	{version: 1, name: "base usage store schema", sql: []string{
		`create table if not exists usage_events (
			id integer primary key autoincrement,
			request_id text,
			event_hash text not null unique,
			timestamp_ms integer not null,
			timestamp text not null,
			provider text,
			executor_type text,
			model text not null,
			endpoint text,
			method text,
			path text,
			auth_type text,
			auth_index text,
			source text,
			source_hash text,
			api_key_hash text,
			account_snapshot text,
			auth_label_snapshot text,
			auth_file_snapshot text,
			auth_provider_snapshot text,
			auth_project_id_snapshot text,
			auth_snapshot_at_ms integer,
				requested_model text,
				resolved_model text,
				reasoning_effort text,
				service_tier text,
				response_service_tier text,
				input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_ms integer,
			ttft_ms integer,
			failed integer not null default 0,
			fail_status_code integer,
			fail_summary text,
			response_metadata_json text,
			header_quota_recover_at_ms integer,
			header_quota_used_percent real,
			header_quota_plan_type text,
			header_error_kind text,
			header_error_code text,
			header_trace_id text,
			fail_body text,
			raw_json text,
			created_at_ms integer not null
		)`,
		`create index if not exists idx_usage_events_timestamp on usage_events(timestamp_ms)`,
		`create index if not exists idx_usage_events_request_id on usage_events(request_id)`,
		`create index if not exists idx_usage_events_model on usage_events(model)`,
		`create index if not exists idx_usage_events_auth_index on usage_events(auth_index)`,
		`create index if not exists idx_usage_events_endpoint on usage_events(endpoint)`,
		`create index if not exists idx_usage_events_header_quota_recover on usage_events(header_quota_recover_at_ms)`,
		`create index if not exists idx_usage_events_header_error_kind on usage_events(header_error_kind)`,
		`create index if not exists idx_usage_events_header_trace_id on usage_events(header_trace_id)`,
		`create table if not exists dead_letter_events (
			id integer primary key autoincrement,
			payload text not null,
			error text not null,
			created_at_ms integer not null
		)`,
		`create table if not exists settings (
			key text primary key,
			value text not null,
			updated_at_ms integer not null
		)`,
	}},
	{version: 2, name: "model prices", sql: []string{
		`create table if not exists model_prices (
			model text primary key,
			prompt_per_1m real not null,
			completion_per_1m real not null,
			cache_per_1m real not null,
			cache_read_per_1m real not null default 0,
			cache_creation_per_1m real not null default 0,
			source text,
			source_model_id text,
			raw_json text,
			updated_at_ms integer not null,
			synced_at_ms integer
		)`,
	}},
	{version: 3, name: "usage event identity ledger", sql: []string{
		`create table if not exists usage_event_identity_ledger (
			event_hash text primary key,
			raw_event_id integer,
			timestamp_ms integer not null,
			bucket_ms integer not null,
			aggregate_schema_version integer not null default 0,
			first_seen_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create index if not exists idx_ledger_raw_event_id on usage_event_identity_ledger(raw_event_id)`,
		`create index if not exists idx_ledger_bucket on usage_event_identity_ledger(bucket_ms)`,
	}},
	{version: 4, name: "usage event compatibility columns"},
	{version: 5, name: "hourly usage rollups", sql: []string{
		`create table if not exists usage_hourly_rollups (
			hour_ms integer not null,
			aggregate_schema_version integer not null,
			dimension_key text not null,
			provider text not null default '',
			executor_type text not null default '',
			model text not null default '',
			requested_model text not null default '',
			resolved_model text not null default '',
			endpoint text not null default '',
			auth_type text not null default '',
			auth_index text not null default '',
			account_id text not null default '',
			source text not null default '',
			source_hash text not null default '',
			api_key_hash text not null default '',
			account_snapshot text not null default '',
			auth_label_snapshot text not null default '',
			auth_file_snapshot text not null default '',
			auth_provider_snapshot text not null default '',
			auth_project_id_snapshot text not null default '',
			failed integer not null default 0,
			fail_status_code integer not null default 0,
			cache_status text not null default '',
			event_count integer not null default 0,
			failed_count integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_count integer not null default 0,
			ttft_sum_ms integer not null default 0,
			ttft_count integer not null default 0,
			first_event_ms integer not null default 0,
			last_event_ms integer not null default 0,
			updated_at_ms integer not null,
			primary key (hour_ms, aggregate_schema_version, dimension_key)
		)`,
		`create index if not exists idx_usage_hourly_rollups_window on usage_hourly_rollups(aggregate_schema_version, hour_ms)`,
		`create index if not exists idx_usage_hourly_rollups_provider on usage_hourly_rollups(provider, hour_ms)`,
		`create index if not exists idx_usage_hourly_rollups_account on usage_hourly_rollups(account_id, hour_ms)`,
		`create table if not exists usage_rollup_checkpoints (
			name text primary key,
			checkpoint_ms integer not null default 0,
			aggregate_schema_version integer not null default 0,
			owner text not null default '',
			lease_until_ms integer not null default 0,
			generation integer not null default 0,
			updated_at_ms integer not null
		)`,
		`create table if not exists usage_account_model_rollups (
			account_key text not null,
			account_snapshot text,
			auth_label_snapshot text,
			auth_provider_snapshot text,
			auth_index text,
			source text,
			source_hash text,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			calls integer not null default 0,
			success_calls integer not null default 0,
			failure_calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			first_seen_ms integer not null,
			last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (account_key, billing_model, service_tier)
		)`,
		`create index if not exists idx_usage_account_model_rollups_last_seen on usage_account_model_rollups(last_seen_ms)`,
		`create index if not exists idx_usage_account_model_rollups_auth_index on usage_account_model_rollups(auth_index)`,
		`create table if not exists usage_dashboard_hourly_rollups (
			bucket_ms integer not null,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			calls integer not null default 0,
			success_calls integer not null default 0,
			failure_calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			updated_at_ms integer not null,
			primary key (bucket_ms, model, billing_model, service_tier)
		)`,
		`create table if not exists api_key_aliases (
			api_key_hash text primary key,
			alias text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists usage_hourly_aggregate_v1 (
			bucket_ms integer not null,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			first_seen_at_ms integer not null,
			last_seen_at_ms integer not null,
			updated_at_ms integer not null,
			primary key (bucket_ms, model, billing_model, service_tier, failed)
		)`,
		`create table if not exists usage_hourly_aggregate_state (
			aggregate_name text primary key,
			schema_version integer not null,
			status text not null,
			backfill_last_event_id integer not null default 0,
			coverage_event_id integer not null default 0,
			target_event_id integer not null default 0,
			processed_events integer not null default 0,
			min_bucket_ms integer,
			max_bucket_ms integer,
			last_run_started_at_ms integer,
			updated_at_ms integer not null,
			finished_at_ms integer,
			last_error text
		)`,
		`insert or ignore into usage_hourly_aggregate_state (
			aggregate_name, schema_version, status, backfill_last_event_id, coverage_event_id,
			target_event_id, processed_events, min_bucket_ms, max_bucket_ms, last_run_started_at_ms,
			updated_at_ms, finished_at_ms, last_error
		)
		select
			'hourly_core', 1,
			case when exists(select 1 from usage_events limit 1) then 'pending' else 'ready' end,
			0, 0, coalesce(max(id), 0), 0, null, null, null, unixepoch('subsec') * 1000, null, null
			from usage_events`,
	}},
	{version: 6, name: "account action candidates", sql: accountActionCandidateSchemaSQL},
}

func Migrate(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("migrate usage sqlite: db is nil")
	}
	ctx := context.Background()
	for _, pragma := range []string{
		`pragma journal_mode = WAL`,
		`pragma synchronous = FULL`,
		`pragma busy_timeout = 5000`,
		`pragma foreign_keys = ON`,
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("migrate usage sqlite: apply pragma %q: %w", pragma, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate usage sqlite: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `create table if not exists schema_migrations (
		version integer primary key,
		name text not null,
		applied_at_ms integer not null default (unixepoch('subsec') * 1000)
	)`); err != nil {
		return fmt.Errorf("migrate usage sqlite: create schema_migrations: %w", err)
	}
	applied, err := appliedMigrationVersions(ctx, tx)
	if err != nil {
		return err
	}
	ordered := append([]migration(nil), migrations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].version < ordered[j].version })
	for _, m := range ordered {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := applyMigration(ctx, tx, m); err != nil {
			return err
		}
	}
	if err := ensureCompatibilitySchema(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate usage sqlite: commit: %w", err)
	}
	return nil
}

func appliedMigrationVersions(ctx context.Context, tx *sql.Tx) (map[int]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `select version from schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate usage sqlite: list applied migrations: %w", err)
	}
	defer rows.Close()
	out := map[int]struct{}{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("migrate usage sqlite: scan applied migration: %w", err)
		}
		out[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate usage sqlite: read applied migrations: %w", err)
	}
	return out, nil
}

func applyMigration(ctx context.Context, tx *sql.Tx, m migration) error {
	if m.version == 6 {
		if err := ensureAccountActionCandidateSchema(ctx, tx); err != nil {
			return fmt.Errorf("migrate usage sqlite: apply migration %d %q: %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into schema_migrations(version, name) values(?, ?)`, m.version, m.name); err != nil {
			return fmt.Errorf("migrate usage sqlite: record migration %d %q: %w", m.version, m.name, err)
		}
		return nil
	}
	for _, stmt := range m.sql {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate usage sqlite: apply migration %d %q: %w", m.version, m.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `insert or ignore into schema_migrations(version, name) values(?, ?)`, m.version, m.name); err != nil {
		return fmt.Errorf("migrate usage sqlite: record migration %d %q: %w", m.version, m.name, err)
	}
	return nil
}

func ensureCompatibilitySchema(ctx context.Context, tx *sql.Tx) error {
	if err := ensureUsageEventColumns(ctx, tx); err != nil {
		return err
	}
	if err := ensureRollupCheckpointColumns(ctx, tx); err != nil {
		return err
	}
	for _, stmt := range []string{
		`create index if not exists idx_usage_events_timestamp on usage_events(timestamp_ms)`,
		`create index if not exists idx_usage_events_request_id on usage_events(request_id)`,
		`create index if not exists idx_usage_events_model on usage_events(model)`,
		`create index if not exists idx_usage_events_auth_index on usage_events(auth_index)`,
		`create index if not exists idx_usage_events_endpoint on usage_events(endpoint)`,
		`create index if not exists idx_usage_events_header_quota_recover on usage_events(header_quota_recover_at_ms)`,
		`create index if not exists idx_usage_events_header_error_kind on usage_events(header_error_kind)`,
		`create index if not exists idx_usage_events_header_trace_id on usage_events(header_trace_id)`,
		`create table if not exists dead_letter_events (
			id integer primary key autoincrement,
			payload text not null,
			error text not null,
			created_at_ms integer not null
		)`,
		`create table if not exists settings (
			key text primary key,
			value text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists model_prices (
			model text primary key,
			prompt_per_1m real not null,
			completion_per_1m real not null,
			cache_per_1m real not null,
			cache_read_per_1m real not null default 0,
			cache_creation_per_1m real not null default 0,
			source text,
			source_model_id text,
			raw_json text,
			updated_at_ms integer not null,
			synced_at_ms integer
		)`,
		`create table if not exists usage_event_identity_ledger (
			event_hash text primary key,
			raw_event_id integer,
			timestamp_ms integer not null,
			bucket_ms integer not null,
			aggregate_schema_version integer not null default 0,
			first_seen_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create index if not exists idx_ledger_raw_event_id on usage_event_identity_ledger(raw_event_id)`,
		`create index if not exists idx_ledger_bucket on usage_event_identity_ledger(bucket_ms)`,
		`create table if not exists usage_hourly_rollups (
			hour_ms integer not null,
			aggregate_schema_version integer not null,
			dimension_key text not null,
			provider text not null default '',
			executor_type text not null default '',
			model text not null default '',
			requested_model text not null default '',
			resolved_model text not null default '',
			endpoint text not null default '',
			auth_type text not null default '',
			auth_index text not null default '',
			account_id text not null default '',
			source text not null default '',
			source_hash text not null default '',
			api_key_hash text not null default '',
			account_snapshot text not null default '',
			auth_label_snapshot text not null default '',
			auth_file_snapshot text not null default '',
			auth_provider_snapshot text not null default '',
			auth_project_id_snapshot text not null default '',
			failed integer not null default 0,
			fail_status_code integer not null default 0,
			cache_status text not null default '',
			event_count integer not null default 0,
			failed_count integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_count integer not null default 0,
			ttft_sum_ms integer not null default 0,
			ttft_count integer not null default 0,
			first_event_ms integer not null default 0,
			last_event_ms integer not null default 0,
			updated_at_ms integer not null,
			primary key (hour_ms, aggregate_schema_version, dimension_key)
		)`,
		`create index if not exists idx_usage_hourly_rollups_window on usage_hourly_rollups(aggregate_schema_version, hour_ms)`,
		`create index if not exists idx_usage_hourly_rollups_provider on usage_hourly_rollups(provider, hour_ms)`,
		`create index if not exists idx_usage_hourly_rollups_account on usage_hourly_rollups(account_id, hour_ms)`,
		`create table if not exists usage_rollup_checkpoints (
			name text primary key,
			checkpoint_ms integer not null default 0,
			aggregate_schema_version integer not null default 0,
			owner text not null default '',
			lease_until_ms integer not null default 0,
			generation integer not null default 0,
			updated_at_ms integer not null
		)`,
		`create table if not exists usage_account_model_rollups (
			account_key text not null,
			account_snapshot text,
			auth_label_snapshot text,
			auth_provider_snapshot text,
			auth_index text,
			source text,
			source_hash text,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			calls integer not null default 0,
			success_calls integer not null default 0,
			failure_calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			first_seen_ms integer not null,
			last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (account_key, billing_model, service_tier)
		)`,
		`create index if not exists idx_usage_account_model_rollups_last_seen on usage_account_model_rollups(last_seen_ms)`,
		`create index if not exists idx_usage_account_model_rollups_auth_index on usage_account_model_rollups(auth_index)`,
		`create table if not exists usage_dashboard_hourly_rollups (
			bucket_ms integer not null,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			calls integer not null default 0,
			success_calls integer not null default 0,
			failure_calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			updated_at_ms integer not null,
			primary key (bucket_ms, model, billing_model, service_tier)
		)`,
		`create table if not exists api_key_aliases (
			api_key_hash text primary key,
			alias text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists usage_hourly_aggregate_v1 (
			bucket_ms integer not null,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			first_seen_at_ms integer not null,
			last_seen_at_ms integer not null,
			updated_at_ms integer not null,
			primary key (bucket_ms, model, billing_model, service_tier, failed)
		)`,
		`create table if not exists usage_hourly_aggregate_state (
			aggregate_name text primary key,
			schema_version integer not null,
			status text not null,
			backfill_last_event_id integer not null default 0,
			coverage_event_id integer not null default 0,
			target_event_id integer not null default 0,
			processed_events integer not null default 0,
			min_bucket_ms integer,
			max_bucket_ms integer,
			last_run_started_at_ms integer,
			updated_at_ms integer not null,
			finished_at_ms integer,
			last_error text
		)`,
		`insert or ignore into usage_hourly_aggregate_state (
			aggregate_name, schema_version, status, backfill_last_event_id, coverage_event_id,
			target_event_id, processed_events, min_bucket_ms, max_bucket_ms, last_run_started_at_ms,
			updated_at_ms, finished_at_ms, last_error
		)
		select
			'hourly_core', 1,
			case when exists(select 1 from usage_events limit 1) then 'pending' else 'ready' end,
			0, 0, coalesce(max(id), 0), 0, null, null, null, unixepoch('subsec') * 1000, null, null
		from usage_events`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate usage sqlite: ensure compatibility schema: %w", err)
		}
	}
	if err := ensureAccountActionCandidateSchema(ctx, tx); err != nil {
		return err
	}
	return nil
}

func ensureAccountActionCandidateSchema(ctx context.Context, tx *sql.Tx) error {
	if len(accountActionCandidateSchemaSQL) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, accountActionCandidateSchemaSQL[0]); err != nil {
		return fmt.Errorf("migrate usage sqlite: ensure account action table: %w", err)
	}
	if err := ensureAccountActionCandidateColumns(ctx, tx); err != nil {
		return err
	}
	for _, stmt := range accountActionCandidateSchemaSQL[1:] {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate usage sqlite: ensure account action schema: %w", err)
		}
	}
	return nil
}

func ensureAccountActionCandidateColumns(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns(ctx, tx, "account_action_candidates")
	if err != nil {
		return err
	}
	definitions := []struct {
		name string
		sql  string
	}{
		{"action_type", "action_type text not null default 'review'"},
		{"status", "status text not null default 'pending'"},
		{"provider", "provider text"},
		{"auth_file_name", "auth_file_name text not null default ''"},
		{"auth_index", "auth_index text"},
		{"account_snapshot", "account_snapshot text"},
		{"account_id_snapshot", "account_id_snapshot text"},
		{"auth_label", "auth_label text"},
		{"reason_code", "reason_code text"},
		{"reason", "reason text not null default ''"},
		{"auto_disable_eligible", "auto_disable_eligible integer not null default 0"},
		{"auto_disabled_at_ms", "auto_disabled_at_ms integer"},
		{"evidence_json", "evidence_json text"},
		{"last_error", "last_error text"},
		{"first_seen_at_ms", "first_seen_at_ms integer not null default 0"},
		{"last_seen_at_ms", "last_seen_at_ms integer not null default 0"},
		{"hit_count", "hit_count integer not null default 1"},
		{"created_at_ms", "created_at_ms integer not null default 0"},
		{"updated_at_ms", "updated_at_ms integer not null default 0"},
	}
	for _, def := range definitions {
		if _, ok := columns[def.name]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `alter table account_action_candidates add column `+def.sql); err != nil {
			return fmt.Errorf("migrate usage sqlite: add account_action_candidates.%s: %w", def.name, err)
		}
	}
	return nil
}

func ensureRollupCheckpointColumns(ctx context.Context, tx *sql.Tx) error {
	if !tableExistsTx(ctx, tx, "usage_rollup_checkpoints") {
		return nil
	}
	columns, err := tableColumns(ctx, tx, "usage_rollup_checkpoints")
	if err != nil {
		return err
	}
	definitions := []struct {
		name string
		sql  string
	}{
		{"checkpoint_ms", "checkpoint_ms integer not null default 0"},
		{"aggregate_schema_version", "aggregate_schema_version integer not null default 0"},
		{"owner", "owner text not null default ''"},
		{"lease_until_ms", "lease_until_ms integer not null default 0"},
		{"generation", "generation integer not null default 0"},
		{"updated_at_ms", "updated_at_ms integer not null default 0"},
	}
	for _, def := range definitions {
		if _, ok := columns[def.name]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `alter table usage_rollup_checkpoints add column `+def.sql); err != nil {
			return fmt.Errorf("migrate usage sqlite: add usage_rollup_checkpoints.%s: %w", def.name, err)
		}
	}
	return nil
}

func ensureUsageEventColumns(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns(ctx, tx, "usage_events")
	if err != nil {
		return err
	}
	definitions := []struct {
		name string
		sql  string
	}{
		{"request_id", "request_id text"},
		{"event_hash", "event_hash text"},
		{"timestamp_ms", "timestamp_ms integer not null default 0"},
		{"timestamp", "timestamp text not null default ''"},
		{"provider", "provider text"},
		{"executor_type", "executor_type text"},
		{"model", "model text not null default '-'"},
		{"endpoint", "endpoint text"},
		{"method", "method text"},
		{"path", "path text"},
		{"auth_type", "auth_type text"},
		{"auth_index", "auth_index text"},
		{"source", "source text"},
		{"source_hash", "source_hash text"},
		{"api_key_hash", "api_key_hash text"},
		{"account_snapshot", "account_snapshot text"},
		{"auth_label_snapshot", "auth_label_snapshot text"},
		{"auth_file_snapshot", "auth_file_snapshot text"},
		{"auth_provider_snapshot", "auth_provider_snapshot text"},
		{"auth_project_id_snapshot", "auth_project_id_snapshot text"},
		{"auth_snapshot_at_ms", "auth_snapshot_at_ms integer"},
		{"requested_model", "requested_model text"},
		{"resolved_model", "resolved_model text"},
		{"reasoning_effort", "reasoning_effort text"},
		{"service_tier", "service_tier text"},
		{"response_service_tier", "response_service_tier text"},
		{"input_tokens", "input_tokens integer not null default 0"},
		{"output_tokens", "output_tokens integer not null default 0"},
		{"reasoning_tokens", "reasoning_tokens integer not null default 0"},
		{"cached_tokens", "cached_tokens integer not null default 0"},
		{"cache_tokens", "cache_tokens integer not null default 0"},
		{"cache_read_tokens", "cache_read_tokens integer not null default 0"},
		{"cache_creation_tokens", "cache_creation_tokens integer not null default 0"},
		{"total_tokens", "total_tokens integer not null default 0"},
		{"latency_ms", "latency_ms integer"},
		{"ttft_ms", "ttft_ms integer"},
		{"failed", "failed integer not null default 0"},
		{"fail_status_code", "fail_status_code integer"},
		{"fail_summary", "fail_summary text"},
		{"response_metadata_json", "response_metadata_json text"},
		{"header_quota_recover_at_ms", "header_quota_recover_at_ms integer"},
		{"header_quota_used_percent", "header_quota_used_percent real"},
		{"header_quota_plan_type", "header_quota_plan_type text"},
		{"header_error_kind", "header_error_kind text"},
		{"header_error_code", "header_error_code text"},
		{"header_trace_id", "header_trace_id text"},
		{"fail_body", "fail_body text"},
		{"raw_json", "raw_json text"},
		{"created_at_ms", "created_at_ms integer not null default 0"},
	}
	for _, def := range definitions {
		if _, ok := columns[def.name]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `alter table usage_events add column `+def.sql); err != nil {
			return fmt.Errorf("migrate usage sqlite: add usage_events.%s: %w", def.name, err)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `pragma table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("migrate usage sqlite: inspect table %s: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("migrate usage sqlite: scan table %s column: %w", table, err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate usage sqlite: read table %s columns: %w", table, err)
	}
	return columns, nil
}

func tableExistsTx(ctx context.Context, tx *sql.Tx, table string) bool {
	var name string
	err := tx.QueryRowContext(ctx, `select name from sqlite_master where type = 'table' and name = ?`, table).Scan(&name)
	return err == nil && name == table
}
