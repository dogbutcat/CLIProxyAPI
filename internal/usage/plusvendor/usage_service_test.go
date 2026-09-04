package plusvendor

import (
	"context"
	"errors"
	"testing"
	"time"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

func TestDashboardSummaryPartialWhenRawEventsUnavailablePreservesRollup(t *testing.T) {
	todayMS := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	nowMS := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	store := &fakeDashboardStore{
		rollup: plusstore.DashboardRollup{
			Totals: plusstore.AggregateMetrics{
				EventCount:   2,
				FailedCount:  1,
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
			},
			ByModel: []plusstore.DimensionUsageRollup{{
				Key: "gpt-5",
				Metrics: plusstore.AggregateMetrics{
					EventCount:  2,
					FailedCount: 1,
					TotalTokens: 30,
				},
			}},
			Hourly: []plusstore.HourlyAggregate{{
				HourMS: todayMS,
				Dimension: plusstore.AggregateDimensions{
					Model:     "gpt-5",
					AuthIndex: "auth-1",
					Source:    "codex",
				},
				Metrics: plusstore.AggregateMetrics{
					EventCount:  2,
					FailedCount: 1,
					TotalTokens: 30,
					LastEventMS: nowMS,
				},
			}},
		},
		eventsErr: errors.New("raw events unavailable"),
	}

	summary, err := newDashboardUsageService(store).DashboardSummary(context.Background(), DashboardSummaryQuery{
		TodayStartMS:   todayMS,
		NowMS:          nowMS,
		TopModels:      5,
		RecentFailures: 5,
	})
	if err != nil {
		t.Fatalf("dashboard summary: %v", err)
	}
	if summary.Status.State != "partial" || !summary.Status.Partial || !summary.Status.HasData {
		t.Fatalf("status = %#v, want partial with data", summary.Status)
	}
	if len(summary.Status.Warnings) != 1 || summary.Status.Warnings[0].Kind != "query_error" {
		t.Fatalf("warnings = %#v, want raw event query warning", summary.Status.Warnings)
	}
	if summary.Aggregate.TotalCalls != 2 || summary.Aggregate.FailureCalls != 1 {
		t.Fatalf("aggregate = %#v, want preserved rollup totals", summary.Aggregate)
	}
	if len(summary.Models) != 1 || summary.Models[0].Model != "gpt-5" {
		t.Fatalf("models = %#v, want rollup model stats", summary.Models)
	}
}

type fakeDashboardStore struct {
	rollup    plusstore.DashboardRollup
	events    []plusstore.Event
	eventsErr error
}

func (f *fakeDashboardStore) DashboardRollup(context.Context, plusstore.AggregateQuery) (plusstore.DashboardRollup, error) {
	return f.rollup, nil
}

func (f *fakeDashboardStore) HourlyRollupCheckpoint(context.Context) (int64, error) {
	return f.rollup.ToMS, nil
}

func (f *fakeDashboardStore) EventsBetween(context.Context, int64, int64, int) ([]plusstore.Event, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	return f.events, nil
}
