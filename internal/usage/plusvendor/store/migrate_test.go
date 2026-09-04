package plusstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateCreatesLatestSchema(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema version: %v", err)
	}
	if version != latestSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, latestSchemaVersion)
	}
	for _, table := range []string{
		"usage_events",
		"dead_letter_events",
		"model_prices",
		"settings",
		"usage_event_identity_ledger",
		"usage_rollup_checkpoints",
		"usage_hourly_rollups",
		"usage_account_model_rollups",
		"usage_dashboard_hourly_rollups",
		"api_key_aliases",
		"usage_hourly_aggregate_v1",
		"usage_hourly_aggregate_state",
		"account_action_candidates",
		"account_action_event_ledger",
	} {
		if !tableExists(t, store.db, table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}
	for _, index := range []string{"idx_ledger_raw_event_id", "idx_ledger_bucket"} {
		if !indexExists(t, store.db, index) {
			t.Fatalf("expected index %s to exist", index)
		}
	}
}

func TestMigrateFromLegacySchemaPreservesUsageEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	applyMigrationsThrough(t, db, 2)
	if _, err := db.Exec(`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms) values ('legacy-hash', 1000, '1970-01-01T00:00:01Z', 'gpt-5', 1234)`); err != nil {
		t.Fatalf("insert legacy usage event: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	var count int
	if err := db.QueryRow(`select count(*) from usage_events where event_hash = 'legacy-hash' and created_at_ms = 1234`).Scan(&count); err != nil {
		t.Fatalf("count preserved raw events: %v", err)
	}
	if count != 1 {
		t.Fatalf("preserved usage event count = %d, want 1", count)
	}
}

func TestMigrateFromMinimalLegacySchemaAddsUsageEventColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table schema_migrations (version integer primary key, name text not null, applied_at_ms integer not null default 0)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.Exec(`insert into schema_migrations(version, name) values(1, 'legacy minimal')`); err != nil {
		t.Fatalf("mark legacy migration: %v", err)
	}
	if _, err := db.Exec(`create table usage_events (
		id integer primary key autoincrement,
		event_hash text not null unique,
		timestamp_ms integer not null,
		timestamp text not null,
		model text not null,
		created_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create minimal usage_events: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms) values ('legacy-minimal-hash', 1000, '1970-01-01T00:00:01Z', 'gpt-5', 1234)`); err != nil {
		t.Fatalf("insert minimal legacy usage event: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	store := NewStore(db)
	ctx := context.Background()
	timestampMS := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC).UnixMilli()
	result, err := store.InsertEvents(ctx, []Event{{
		RequestID:            "post-migrate",
		EventHash:            "post-migrate-hash",
		TimestampMS:          timestampMS,
		Timestamp:            time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:                "gpt-5",
		ResponseServiceTier:  "default",
		InputTokens:          1,
		OutputTokens:         2,
		TotalTokens:          3,
		ResponseMetadataJSON: `{"X-Oai-Request-Id":["trace-after-migrate"]}`,
		CreatedAtMS:          timestampMS,
	}})
	if err != nil {
		t.Fatalf("InsertEvents() after migration error = %v", err)
	}
	if result.Inserted != 1 {
		t.Fatalf("InsertEvents() after migration = %+v, want one insert", result)
	}
	events, err := store.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents() after migration error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("RecentEvents() len = %d, want legacy and new rows", len(events))
	}
}

func TestMigrateFromLegacyRollupSchemaAddsCheckpointColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table schema_migrations (version integer primary key, name text not null, applied_at_ms integer not null default 0)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.Exec(`insert into schema_migrations(version, name) values(1, 'legacy base'), (2, 'legacy prices'), (3, 'legacy rollup foundations'), (4, 'legacy aliases')`); err != nil {
		t.Fatalf("mark legacy migrations: %v", err)
	}
	if _, err := db.Exec(`create table usage_events (
		id integer primary key autoincrement,
		event_hash text not null unique,
		timestamp_ms integer not null,
		timestamp text not null,
		model text not null,
		created_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create legacy usage_events: %v", err)
	}
	if _, err := db.Exec(`create table usage_rollup_checkpoints (
		name text primary key,
		last_event_id integer not null default 0,
		updated_at_ms integer not null,
		last_error text,
		last_run_started_at_ms integer,
		last_run_finished_at_ms integer
	)`); err != nil {
		t.Fatalf("create legacy usage_rollup_checkpoints: %v", err)
	}
	if _, err := db.Exec(`insert into usage_rollup_checkpoints(name, last_event_id, updated_at_ms) values('usage_hourly', 0, 1)`); err != nil {
		t.Fatalf("insert legacy checkpoint: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	for _, column := range []string{"checkpoint_ms", "aggregate_schema_version", "owner", "lease_until_ms", "generation"} {
		if !columnExists(t, db, "usage_rollup_checkpoints", column) {
			t.Fatalf("expected usage_rollup_checkpoints.%s to exist", column)
		}
	}
	for _, table := range []string{"usage_event_identity_ledger", "usage_dashboard_hourly_rollups", "api_key_aliases", "usage_hourly_aggregate_state"} {
		if !tableExists(t, db, table) {
			t.Fatalf("expected compatibility table %s to exist", table)
		}
	}
	store := NewStore(db)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := store.InsertEvents(ctx, []Event{{
		RequestID:   "legacy-rollup",
		EventHash:   "legacy-rollup-hash",
		TimestampMS: base + 1,
		Timestamp:   time.UnixMilli(base + 1).UTC().Format(time.RFC3339Nano),
		Model:       "gpt-5",
		TotalTokens: 1,
		CreatedAtMS: base + 1,
	}}); err != nil {
		t.Fatalf("InsertEvents() after legacy rollup migration error = %v", err)
	}
	if _, err := store.CatchUpHourlyRollups(ctx, RollupOptions{ThroughMS: base + HourMS}); err != nil {
		t.Fatalf("CatchUpHourlyRollups() after legacy migration error = %v", err)
	}
}

func TestMigrateFromLegacyAccountActionSchemaAddsColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	applyMigrationsThrough(t, db, 5)
	if _, err := db.Exec(`create table account_action_candidates (
		id integer primary key autoincrement,
		action_type text not null,
		status text not null,
		provider text,
		auth_file_name text not null,
		auth_index text,
		account_snapshot text,
		auth_label text,
		reason text not null,
		auto_disable_eligible integer not null default 0,
		evidence_json text,
		first_seen_at_ms integer not null,
		last_seen_at_ms integer not null,
		hit_count integer not null default 1,
		created_at_ms integer not null,
		updated_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create legacy account_action_candidates: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	for _, column := range []string{"account_id_snapshot", "reason_code", "auto_disabled_at_ms", "last_error"} {
		if !columnExists(t, db, "account_action_candidates", column) {
			t.Fatalf("expected account_action_candidates.%s to exist", column)
		}
	}
	for _, index := range []string{"idx_account_action_candidates_pending_identity_action", "idx_account_action_candidates_status_seen"} {
		if !indexExists(t, db, index) {
			t.Fatalf("expected index %s to exist", index)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	for i := 0; i < 3; i++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("Migrate() round %d error = %v", i+1, err)
		}
	}
	var count int
	if err := db.QueryRow(`select count(*) from schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != latestSchemaVersion {
		t.Fatalf("migration count = %d, want %d", count, latestSchemaVersion)
	}
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`select name from sqlite_master where type = 'table' and name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query table %s: %v", table, err)
	}
	return name == table
}

func indexExists(t *testing.T, db *sql.DB, index string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`select name from sqlite_master where type = 'index' and name = ?`, index).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query index %s: %v", index, err)
	}
	return name == index
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("query table %s columns: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table %s column: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read table %s columns: %v", table, err)
	}
	return false
}

func applyMigrationsThrough(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin legacy migration transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `create table if not exists schema_migrations (version integer primary key, name text not null, applied_at_ms integer not null default (unixepoch('subsec') * 1000))`); err != nil {
		t.Fatalf("create legacy schema_migrations: %v", err)
	}
	for _, migration := range migrations {
		if migration.version > version {
			continue
		}
		if err := applyMigration(ctx, tx, migration); err != nil {
			t.Fatalf("apply legacy migration %d: %v", migration.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy migrations: %v", err)
	}
}
