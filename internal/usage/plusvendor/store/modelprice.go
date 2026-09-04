package plusstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type ModelPriceRepository interface {
	LoadAll(context.Context) (map[string]ModelPrice, error)
	ReplaceAll(context.Context, map[string]ModelPrice) error
	UpsertSynced(context.Context, map[string]ModelPrice) (ModelPriceSyncResult, error)
	Delete(context.Context, string) error
}

type modelPriceRepository struct {
	db *sql.DB
}

func NewModelPriceRepository(db *sql.DB) ModelPriceRepository {
	return &modelPriceRepository{db: db}
}

func (r *modelPriceRepository) LoadAll(ctx context.Context) (map[string]ModelPrice, error) {
	rows, err := r.db.QueryContext(ctx, `select model,prompt_per_1m,completion_per_1m,cache_per_1m,cache_read_per_1m,cache_creation_per_1m,source,source_model_id,raw_json,updated_at_ms,synced_at_ms from model_prices order by model`)
	if err != nil {
		return nil, fmt.Errorf("load model prices: query: %w", err)
	}
	defer rows.Close()
	out := map[string]ModelPrice{}
	for rows.Next() {
		var id string
		var price ModelPrice
		var source, sourceID, raw sql.NullString
		var synced sql.NullInt64
		if err := rows.Scan(&id, &price.Prompt, &price.Completion, &price.Cache, &price.CacheRead, &price.CacheCreation, &source, &sourceID, &raw, &price.UpdatedAtMS, &synced); err != nil {
			return nil, fmt.Errorf("load model prices: scan: %w", err)
		}
		price.Source = source.String
		price.SourceModelID = sourceID.String
		price.RawJSON = raw.String
		if synced.Valid {
			value := synced.Int64
			price.SyncedAtMS = &value
		}
		out[id] = price
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load model prices: read rows: %w", err)
	}
	return out, nil
}

func (r *modelPriceRepository) ReplaceAll(ctx context.Context, prices map[string]ModelPrice) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace model prices: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `delete from model_prices`); err != nil {
		return fmt.Errorf("replace model prices: delete existing: %w", err)
	}
	if err := insertModelPrices(ctx, tx, prices); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace model prices: commit: %w", err)
	}
	return nil
}

func (r *modelPriceRepository) UpsertSynced(ctx context.Context, prices map[string]ModelPrice) (ModelPriceSyncResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelPriceSyncResult{}, fmt.Errorf("upsert synced model prices: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `insert into model_prices (
		model,prompt_per_1m,completion_per_1m,cache_per_1m,cache_read_per_1m,cache_creation_per_1m,
		source,source_model_id,raw_json,updated_at_ms,synced_at_ms
	) values (?,?,?,?,?,?,?,?,?,?,?)
	on conflict(model) do update set
		prompt_per_1m=excluded.prompt_per_1m,
		completion_per_1m=excluded.completion_per_1m,
		cache_per_1m=excluded.cache_per_1m,
		cache_read_per_1m=excluded.cache_read_per_1m,
		cache_creation_per_1m=excluded.cache_creation_per_1m,
		source=excluded.source,
		source_model_id=excluded.source_model_id,
		raw_json=excluded.raw_json,
		updated_at_ms=excluded.updated_at_ms,
		synced_at_ms=excluded.synced_at_ms`)
	if err != nil {
		return ModelPriceSyncResult{}, fmt.Errorf("upsert synced model prices: prepare upsert: %w", err)
	}
	defer stmt.Close()
	now := time.Now().UnixMilli()
	result := ModelPriceSyncResult{}
	for id, price := range prices {
		if err := validateModelPrice(id, price); err != nil {
			result.Skipped++
			continue
		}
		if price.Source == "" {
			price.Source = "sync"
		}
		if price.SourceModelID == "" {
			price.SourceModelID = id
		}
		if _, err := stmt.ExecContext(ctx, id, price.Prompt, price.Completion, price.Cache, price.CacheRead, price.CacheCreation, nullString(price.Source), nullString(price.SourceModelID), nullString(price.RawJSON), now, now); err != nil {
			return ModelPriceSyncResult{}, fmt.Errorf("upsert synced model prices: upsert %s: %w", id, err)
		}
		result.Imported++
	}
	if err := tx.Commit(); err != nil {
		return ModelPriceSyncResult{}, fmt.Errorf("upsert synced model prices: commit: %w", err)
	}
	return result, nil
}

func (r *modelPriceRepository) Delete(ctx context.Context, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("model is required")
	}
	if _, err := r.db.ExecContext(ctx, `delete from model_prices where model = ?`, model); err != nil {
		return fmt.Errorf("delete model price %s: %w", model, err)
	}
	return nil
}

func insertModelPrices(ctx context.Context, tx *sql.Tx, prices map[string]ModelPrice) error {
	if len(prices) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `insert into model_prices (
		model,prompt_per_1m,completion_per_1m,cache_per_1m,cache_read_per_1m,cache_creation_per_1m,
		source,source_model_id,raw_json,updated_at_ms,synced_at_ms
	) values (?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("replace model prices: prepare insert: %w", err)
	}
	defer stmt.Close()
	now := time.Now().UnixMilli()
	for id, price := range prices {
		if err := validateModelPrice(id, price); err != nil {
			return fmt.Errorf("replace model prices: validate %s: %w", id, err)
		}
		if _, err := stmt.ExecContext(ctx, id, price.Prompt, price.Completion, price.Cache, price.CacheRead, price.CacheCreation, nullString(price.Source), nullString(price.SourceModelID), nullString(price.RawJSON), now, nullInt64Ptr(price.SyncedAtMS)); err != nil {
			return fmt.Errorf("replace model prices: insert %s: %w", id, err)
		}
	}
	return nil
}

func validateModelPrice(id string, price ModelPrice) error {
	if id == "" {
		return errors.New("model is required")
	}
	if !validPriceValue(price.Prompt) ||
		!validPriceValue(price.Completion) ||
		!validPriceValue(price.Cache) ||
		!validPriceValue(price.CacheRead) ||
		!validPriceValue(price.CacheCreation) {
		return fmt.Errorf("invalid model price for %s", id)
	}
	return nil
}

func validPriceValue(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nullInt64Ptr(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
