package worker

import (
	"context"
	"fmt"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type HourlyAggregateWorker struct {
	store *plusstore.Store
	opts  plusstore.RollupOptions
}

func NewHourlyAggregateWorker(store *plusstore.Store, opts plusstore.RollupOptions) *HourlyAggregateWorker {
	return &HourlyAggregateWorker{store: store, opts: opts}
}

func (w *HourlyAggregateWorker) RunOnce(ctx context.Context) (plusstore.RollupResult, error) {
	if w == nil || w.store == nil {
		return plusstore.RollupResult{}, fmt.Errorf("hourly aggregate worker: store is nil")
	}
	return w.store.CatchUpHourlyRollups(ctx, w.opts)
}
