package plusstore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultMaxOpenConns    = 4
	defaultMaxIdleConns    = 2
	defaultConnMaxIdleTime = 5 * time.Minute
)

type Options struct {
	Path            string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
}

type Store struct {
	db          *sql.DB
	events      UsageEventRepository
	deadLetters DeadLetterRepository
	modelPrices ModelPriceRepository
}

func Open(path string) (*sql.DB, error) {
	return OpenWithOptions(Options{Path: path})
}

func OpenWithOptions(options Options) (*sql.DB, error) {
	if options.Path == "" {
		return nil, fmt.Errorf("open usage sqlite: path is required")
	}
	path, err := filepath.Abs(options.Path)
	if err != nil {
		return nil, fmt.Errorf("open usage sqlite: resolve path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("open usage sqlite: create parent directory: %w", err)
	}
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("open usage sqlite: open database: %w", err)
	}
	db.SetMaxOpenConns(options.maxOpenConns())
	db.SetMaxIdleConns(options.maxIdleConns())
	db.SetConnMaxIdleTime(options.connMaxIdleTime())
	if err := Migrate(db); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("open usage sqlite: migrate: %w; close after migrate failure: %v", err, closeErr)
		}
		return nil, fmt.Errorf("open usage sqlite: migrate: %w", err)
	}
	return db, nil
}

func OpenStore(path string) (*Store, error) {
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	return NewStore(db), nil
}

func NewStore(db *sql.DB) *Store {
	return &Store{
		db:          db,
		events:      NewUsageEventRepository(db),
		deadLetters: NewDeadLetterRepository(db),
		modelPrices: NewModelPriceRepository(db),
	}
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close usage sqlite: %w", err)
	}
	return nil
}

func (s *Store) InsertEvents(ctx context.Context, events []Event) (InsertResult, error) {
	return s.events.InsertBatch(ctx, events)
}

func (s *Store) RecentEvents(ctx context.Context, limit int) ([]Event, error) {
	return s.events.ListRecent(ctx, limit)
}

func (s *Store) EventsBetween(ctx context.Context, fromMS, toMS int64, limit int) ([]Event, error) {
	return s.events.EventsBetween(ctx, fromMS, toMS, limit)
}

func (s *Store) CountEvents(ctx context.Context) (int64, error) {
	return s.events.Count(ctx)
}

func (s *Store) AddDeadLetter(ctx context.Context, payload string, parseErr error) error {
	errText := ""
	if parseErr != nil {
		errText = FailSummaryFromBody(parseErr.Error())
	}
	return s.deadLetters.Insert(ctx, payload, errText)
}

func (s *Store) CountDeadLetters(ctx context.Context) (int64, error) {
	return s.deadLetters.Count(ctx)
}

func (s *Store) Counts(ctx context.Context) (int64, int64, error) {
	events, err := s.CountEvents(ctx)
	if err != nil {
		return 0, 0, err
	}
	dead, err := s.CountDeadLetters(ctx)
	if err != nil {
		return 0, 0, err
	}
	return events, dead, nil
}

func (s *Store) LoadModelPrices(ctx context.Context) (map[string]ModelPrice, error) {
	return s.modelPrices.LoadAll(ctx)
}

func (s *Store) SaveModelPrices(ctx context.Context, prices map[string]ModelPrice) error {
	return s.modelPrices.ReplaceAll(ctx, prices)
}

func (s *Store) UpsertSyncedModelPrices(ctx context.Context, prices map[string]ModelPrice) (ModelPriceSyncResult, error) {
	return s.modelPrices.UpsertSynced(ctx, prices)
}

func (s *Store) DeleteModelPrice(ctx context.Context, model string) error {
	return s.modelPrices.Delete(ctx, model)
}

func dataSourceName(path string) string {
	dsn := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := dsn.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_txlock", "immediate")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func (o Options) maxOpenConns() int {
	if o.MaxOpenConns > 0 {
		return o.MaxOpenConns
	}
	return defaultMaxOpenConns
}

func (o Options) maxIdleConns() int {
	if o.MaxIdleConns > 0 {
		return o.MaxIdleConns
	}
	return defaultMaxIdleConns
}

func (o Options) connMaxIdleTime() time.Duration {
	if o.ConnMaxIdleTime > 0 {
		return o.ConnMaxIdleTime
	}
	return defaultConnMaxIdleTime
}
