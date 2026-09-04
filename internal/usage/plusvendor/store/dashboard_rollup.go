package plusstore

import (
	"context"
	"sort"
)

type DashboardRollup struct {
	FromMS     int64                  `json:"from_ms"`
	ToMS       int64                  `json:"to_ms"`
	Totals     AggregateMetrics       `json:"totals"`
	ByProvider []DimensionUsageRollup `json:"by_provider"`
	ByAccount  []DimensionUsageRollup `json:"by_account"`
	ByModel    []DimensionUsageRollup `json:"by_model"`
	ByCache    []DimensionUsageRollup `json:"by_cache"`
	ByFailed   []DimensionUsageRollup `json:"by_failed"`
	Hourly     []HourlyAggregate      `json:"hourly"`
}

type DimensionUsageRollup struct {
	Key     string           `json:"key"`
	Metrics AggregateMetrics `json:"metrics"`
}

type AccountHistoryRollup struct {
	AccountID string           `json:"account_id"`
	HourMS    int64            `json:"hour_ms"`
	Metrics   AggregateMetrics `json:"metrics"`
}

func (s *Store) DashboardRollup(ctx context.Context, query AggregateQuery) (DashboardRollup, error) {
	rows, err := s.MergedHourlyAggregates(ctx, query)
	if err != nil {
		return DashboardRollup{}, err
	}
	out := DashboardRollup{FromMS: query.FromMS, ToMS: query.ToMS, Hourly: rows}
	byProvider := map[string]AggregateMetrics{}
	byAccount := map[string]AggregateMetrics{}
	byModel := map[string]AggregateMetrics{}
	byCache := map[string]AggregateMetrics{}
	byFailed := map[string]AggregateMetrics{}
	for _, row := range rows {
		out.Totals.addMetrics(row.Metrics)
		addDimensionMetric(byProvider, firstNonEmptyString(row.Dimension.Provider, "-"), row.Metrics)
		addDimensionMetric(byAccount, firstNonEmptyString(row.Dimension.AccountID, "-"), row.Metrics)
		addDimensionMetric(byModel, firstNonEmptyString(row.Dimension.Model, "-"), row.Metrics)
		addDimensionMetric(byCache, firstNonEmptyString(row.Dimension.CacheStatus, "none"), row.Metrics)
		addDimensionMetric(byFailed, boolDimension(row.Dimension.Failed), row.Metrics)
	}
	out.ByProvider = sortedDimensionRollups(byProvider)
	out.ByAccount = sortedDimensionRollups(byAccount)
	out.ByModel = sortedDimensionRollups(byModel)
	out.ByCache = sortedDimensionRollups(byCache)
	out.ByFailed = sortedDimensionRollups(byFailed)
	return out, nil
}

func (s *Store) AccountHistoryRollup(ctx context.Context, query AggregateQuery) ([]AccountHistoryRollup, error) {
	rows, err := s.MergedHourlyAggregates(ctx, query)
	if err != nil {
		return nil, err
	}
	merged := map[string]AccountHistoryRollup{}
	for _, row := range rows {
		accountID := firstNonEmptyString(row.Dimension.AccountID, "-")
		key := intDimension64(row.HourMS) + "\x00" + accountID
		current := merged[key]
		if current.AccountID == "" {
			current.AccountID = accountID
			current.HourMS = row.HourMS
		}
		current.Metrics.addMetrics(row.Metrics)
		merged[key] = current
	}
	out := make([]AccountHistoryRollup, 0, len(merged))
	for _, row := range merged {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HourMS != out[j].HourMS {
			return out[i].HourMS < out[j].HourMS
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out, nil
}

func addDimensionMetric(target map[string]AggregateMetrics, key string, metrics AggregateMetrics) {
	current := target[key]
	current.addMetrics(metrics)
	target[key] = current
}

func sortedDimensionRollups(values map[string]AggregateMetrics) []DimensionUsageRollup {
	out := make([]DimensionUsageRollup, 0, len(values))
	for key, metrics := range values {
		out = append(out, DimensionUsageRollup{Key: key, Metrics: metrics})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
