package plusstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const maxUsageImportLineBytes = 16 << 20

func IsUsageImportRetryable(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Store) ImportEventsJSONL(ctx context.Context, r io.Reader) (UsageImportResult, error) {
	if s == nil || s.db == nil {
		return UsageImportResult{}, fmt.Errorf("import usage events: store is nil")
	}
	result := UsageImportResult{Format: "jsonl"}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxUsageImportLineBytes)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		result.Total++
		event, err := decodeImportEvent(line)
		if err != nil {
			result.Failed++
			if len(result.Warnings) < 10 {
				result.Warnings = append(result.Warnings, err.Error())
			}
			continue
		}
		inserted, err := s.InsertEvents(ctx, []Event{event})
		if err != nil {
			result.Failed++
			if len(result.Warnings) < 10 {
				result.Warnings = append(result.Warnings, err.Error())
			}
			continue
		}
		result.Added += inserted.Inserted
		result.Skipped += inserted.Skipped
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("import usage events: read jsonl: %w", err)
	}
	return result, nil
}

func decodeImportEvent(line []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(line, &event); err == nil && (event.Model != "" || event.TimestampMS > 0 || event.EventHash != "") {
		normalizeImportedEvent(&event)
		return event, nil
	}
	event, err := NormalizeRaw(line)
	if err != nil {
		return Event{}, err
	}
	normalizeImportedEvent(&event)
	return event, nil
}

func normalizeImportedEvent(event *Event) {
	event.Source = MaskSource(event.Source)
	event.RawJSON = SafeRawJSON(event.RawJSON)
	event.FailBody = FailSummaryFromBody(event.FailBody)
	if event.TimestampMS <= 0 {
		event.TimestampMS = time.Now().UnixMilli()
	}
	if event.Timestamp == "" {
		event.Timestamp = time.UnixMilli(event.TimestampMS).UTC().Format(time.RFC3339Nano)
	}
	if strings.TrimSpace(event.Model) == "" {
		event.Model = "-"
	}
	if event.TotalTokens <= 0 {
		event.TotalTokens = event.InputTokens + event.OutputTokens
	}
	if event.EventHash == "" {
		event.EventHash = BuildEventHash(*event)
	}
}
