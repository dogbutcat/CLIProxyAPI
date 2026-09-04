package worker

import (
	"context"
	"time"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type RollupLoop struct {
	worker  *HourlyAggregateWorker
	period  time.Duration
	results chan plusstore.RollupResult
}

func NewRollupLoop(worker *HourlyAggregateWorker, period time.Duration) *RollupLoop {
	if period <= 0 {
		period = time.Hour
	}
	return &RollupLoop{worker: worker, period: period, results: make(chan plusstore.RollupResult, 1)}
}

func (l *RollupLoop) Run(ctx context.Context) error {
	if l == nil || l.worker == nil {
		return nil
	}
	ticker := time.NewTicker(l.period)
	defer ticker.Stop()
	for {
		result, err := l.worker.RunOnce(ctx)
		if err != nil {
			return err
		}
		l.publish(result)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *RollupLoop) Results() <-chan plusstore.RollupResult {
	if l == nil {
		return nil
	}
	return l.results
}

func (l *RollupLoop) publish(result plusstore.RollupResult) {
	select {
	case l.results <- result:
	default:
		select {
		case <-l.results:
		default:
		}
		l.results <- result
	}
}
