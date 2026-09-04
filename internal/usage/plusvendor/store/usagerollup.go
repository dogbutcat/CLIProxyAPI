package plusstore

import (
	"context"
	"database/sql"
	"fmt"
)

type rollupLease struct {
	acquired     bool
	owner        string
	checkpointMS int64
	generation   int64
}

func (s *Store) acquireRollupLease(ctx context.Context, opts RollupOptions) (rollupLease, error) {
	now := nowMS()
	owner := opts.Owner
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return rollupLease{}, fmt.Errorf("acquire rollup lease: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `insert or ignore into usage_rollup_checkpoints(
		name, checkpoint_ms, aggregate_schema_version, owner, lease_until_ms, generation, updated_at_ms
	) values(?, 0, ?, '', 0, 0, ?)`, opts.Name, AggregateSchemaVersion, now); err != nil {
		return rollupLease{}, fmt.Errorf("acquire rollup lease: ensure checkpoint: %w", err)
	}
	var checkpointMS, leaseUntilMS, generation int64
	var schemaVersion int
	var currentOwner string
	if err := tx.QueryRowContext(ctx, `select checkpoint_ms, aggregate_schema_version, owner, lease_until_ms, generation
		from usage_rollup_checkpoints where name = ?`, opts.Name).Scan(&checkpointMS, &schemaVersion, &currentOwner, &leaseUntilMS, &generation); err != nil {
		return rollupLease{}, fmt.Errorf("acquire rollup lease: load checkpoint: %w", err)
	}
	if schemaVersion != AggregateSchemaVersion {
		checkpointMS = 0
		schemaVersion = AggregateSchemaVersion
		if _, err := tx.ExecContext(ctx, `update usage_rollup_checkpoints set checkpoint_ms = 0, aggregate_schema_version = ?, owner = '', lease_until_ms = 0, generation = generation + 1, updated_at_ms = ? where name = ?`, AggregateSchemaVersion, now, opts.Name); err != nil {
			return rollupLease{}, fmt.Errorf("acquire rollup lease: reset version gate: %w", err)
		}
	}
	if leaseUntilMS > now && currentOwner != "" && currentOwner != owner {
		if err := tx.Commit(); err != nil {
			return rollupLease{}, fmt.Errorf("acquire rollup lease: commit contended read: %w", err)
		}
		return rollupLease{checkpointMS: checkpointMS}, nil
	}
	generation++
	if _, err := tx.ExecContext(ctx, `update usage_rollup_checkpoints set owner = ?, lease_until_ms = ?, generation = ?, updated_at_ms = ? where name = ?`, owner, now+opts.LeaseDuration.Milliseconds(), generation, now, opts.Name); err != nil {
		return rollupLease{}, fmt.Errorf("acquire rollup lease: claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return rollupLease{}, fmt.Errorf("acquire rollup lease: commit: %w", err)
	}
	return rollupLease{acquired: true, owner: owner, checkpointMS: checkpointMS, generation: generation}, nil
}

func (s *Store) releaseRollupLease(ctx context.Context, name, owner string) {
	if s == nil || s.db == nil || name == "" || owner == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `update usage_rollup_checkpoints set owner = '', lease_until_ms = 0, updated_at_ms = ? where name = ? and owner = ?`, nowMS(), name, owner)
}

func (s *Store) rollupCheckpoint(ctx context.Context, name string) (int64, int, error) {
	if name == "" {
		name = hourlyRollupName
	}
	var checkpointMS int64
	var schemaVersion int
	err := s.db.QueryRowContext(ctx, `select checkpoint_ms, aggregate_schema_version from usage_rollup_checkpoints where name = ?`, name).Scan(&checkpointMS, &schemaVersion)
	if err == sql.ErrNoRows {
		return 0, AggregateSchemaVersion, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("load rollup checkpoint: %w", err)
	}
	return checkpointMS, schemaVersion, nil
}

func advanceCheckpointTx(ctx context.Context, tx *sql.Tx, name string, fromMS, toMS int64) error {
	var checkpointMS int64
	err := tx.QueryRowContext(ctx, `select checkpoint_ms from usage_rollup_checkpoints where name = ?`, name).Scan(&checkpointMS)
	if err == sql.ErrNoRows {
		checkpointMS = 0
		if _, err := tx.ExecContext(ctx, `insert into usage_rollup_checkpoints(
			name, checkpoint_ms, aggregate_schema_version, owner, lease_until_ms, generation, updated_at_ms
		) values(?, 0, ?, '', 0, 0, ?)`, name, AggregateSchemaVersion, nowMS()); err != nil {
			return fmt.Errorf("advance rollup checkpoint: insert: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("advance rollup checkpoint: load: %w", err)
	}
	if checkpointMS == 0 || fromMS <= checkpointMS {
		if toMS > checkpointMS {
			checkpointMS = toMS
		}
		if _, err := tx.ExecContext(ctx, `update usage_rollup_checkpoints set checkpoint_ms = ?, aggregate_schema_version = ?, updated_at_ms = ? where name = ?`, checkpointMS, AggregateSchemaVersion, nowMS(), name); err != nil {
			return fmt.Errorf("advance rollup checkpoint: update: %w", err)
		}
	}
	return nil
}
