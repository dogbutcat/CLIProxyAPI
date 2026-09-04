package plusstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type DeadLetterRepository interface {
	Insert(context.Context, string, string) error
	Count(context.Context) (int64, error)
}

type deadLetterRepository struct {
	db *sql.DB
}

func NewDeadLetterRepository(db *sql.DB) DeadLetterRepository {
	return &deadLetterRepository{db: db}
}

func (r *deadLetterRepository) Insert(ctx context.Context, payload string, errText string) error {
	_, err := r.db.ExecContext(ctx, `insert into dead_letter_events(payload,error,created_at_ms) values(?,?,?)`, SafeRawJSON(payload), FailSummaryFromBody(errText), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("insert dead-letter event: %w", err)
	}
	return nil
}

func (r *deadLetterRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from dead_letter_events`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count dead-letter events: %w", err)
	}
	return count, nil
}
