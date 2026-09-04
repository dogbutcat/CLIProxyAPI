package worker

import (
	"context"
	"fmt"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type AccountHistoryRollup struct {
	store *plusstore.Store
}

func NewAccountHistoryRollup(store *plusstore.Store) *AccountHistoryRollup {
	return &AccountHistoryRollup{store: store}
}

func (r *AccountHistoryRollup) Query(ctx context.Context, query plusstore.AggregateQuery) ([]plusstore.AccountHistoryRollup, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("account history rollup: store is nil")
	}
	return r.store.AccountHistoryRollup(ctx, query)
}
