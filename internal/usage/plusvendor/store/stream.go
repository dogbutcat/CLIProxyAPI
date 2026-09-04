package plusstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

func (s *Store) ExportEventsJSONL(ctx context.Context, w io.Writer) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("export usage events: store is nil")
	}
	rows, err := s.db.QueryContext(ctx, selectEventColumnsSQL()+` order by timestamp_ms asc, id asc`)
	if err != nil {
		return 0, fmt.Errorf("export usage events: query: %w", err)
	}
	defer rows.Close()
	encoder := json.NewEncoder(w)
	count := 0
	for rows.Next() {
		select {
		case <-ctx.Done():
			return count, ctx.Err()
		default:
		}
		event, err := scanEvent(rows)
		if err != nil {
			return count, fmt.Errorf("export usage events: scan: %w", err)
		}
		event.Source = MaskSource(event.Source)
		event.RawJSON = ""
		event.FailBody = ""
		if err := encoder.Encode(event); err != nil {
			return count, fmt.Errorf("export usage events: write jsonl: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("export usage events: read rows: %w", err)
	}
	return count, nil
}
