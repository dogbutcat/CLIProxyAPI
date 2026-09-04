package plusstore

import (
	"context"
	"fmt"
)

type RebuildOptions struct {
	FromMS        int64
	ToMS          int64
	Owner         string
	LeaseDuration int64
	BatchHours    int
}

func (s *Store) RebuildHourlyRollups(ctx context.Context, fromMS, toMS int64) (RollupResult, error) {
	if s == nil || s.db == nil {
		return RollupResult{}, fmt.Errorf("rebuild hourly rollups: store is nil")
	}
	fromMS = floorHour(fromMS)
	toMS = ceilHour(toMS)
	if fromMS >= toMS {
		return RollupResult{}, fmt.Errorf("rebuild hourly rollups: from_ms must be before to_ms")
	}
	opts := normalizeRollupOptions(RollupOptions{Name: hourlyRollupName, Owner: "rebuild"})
	lease, err := s.acquireRollupLease(ctx, opts)
	if err != nil {
		return RollupResult{}, err
	}
	if !lease.acquired {
		return RollupResult{Name: opts.Name, FromMS: fromMS, ToMS: toMS, CheckpointMS: lease.checkpointMS, Contended: true, Rebuilt: true}, nil
	}
	defer s.releaseRollupLease(context.Background(), opts.Name, lease.owner)
	result := RollupResult{Name: opts.Name, FromMS: fromMS, ToMS: toMS, CheckpointMS: lease.checkpointMS, Rebuilt: true}
	batchHours := opts.BatchHours
	for startMS := fromMS; startMS < toMS; startMS += int64(batchHours) * HourMS {
		endMS := startMS + int64(batchHours)*HourMS
		if endMS > toMS {
			endMS = toMS
		}
		batch, err := s.materializeHourlyRange(ctx, startMS, endMS, true)
		if err != nil {
			return result, err
		}
		result.Batches++
		result.Hours += int((endMS - startMS) / HourMS)
		result.RawEvents += batch.RawEvents
		result.RowsWritten += batch.RowsWritten
	}
	return result, nil
}

func (s *Store) BackfillHourlyRollups(ctx context.Context, fromMS, toMS int64) (RollupResult, error) {
	return s.RebuildHourlyRollups(ctx, fromMS, toMS)
}
