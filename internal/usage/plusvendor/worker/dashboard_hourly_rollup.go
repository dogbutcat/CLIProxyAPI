package worker

import (
	"context"
	"fmt"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type DashboardHourlyRollup struct {
	store *plusstore.Store
}

func NewDashboardHourlyRollup(store *plusstore.Store) *DashboardHourlyRollup {
	return &DashboardHourlyRollup{store: store}
}

func (r *DashboardHourlyRollup) Query(ctx context.Context, query plusstore.AggregateQuery) (plusstore.DashboardRollup, error) {
	if r == nil || r.store == nil {
		return plusstore.DashboardRollup{}, fmt.Errorf("dashboard hourly rollup: store is nil")
	}
	return r.store.DashboardRollup(ctx, query)
}
