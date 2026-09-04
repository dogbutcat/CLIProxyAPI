package plusstore

import (
	"context"
	"fmt"
)

type MonitoringSelectors struct {
	Models        []MonitoringSelectorOption
	Providers     []MonitoringSelectorOption
	Accounts      []MonitoringSelectorOption
	RequestTypes  []MonitoringSelectorOption
	CacheStatuses []MonitoringSelectorOption
	Failed        []MonitoringSelectorOption
}

type MonitoringSelectorOption struct {
	Value string
	Label string
	Count int64
}

func (s *Store) MonitoringSelectors(ctx context.Context, filter AnalyticsFilter) (MonitoringSelectors, error) {
	if s == nil || s.db == nil {
		return MonitoringSelectors{}, fmt.Errorf("monitoring selectors: store is nil")
	}
	models, err := s.monitoringSelectorOptions(ctx, filter.withoutDimension(analyticsFilterModel), "models", analyticsModelSQL(), analyticsModelSQL(), 500)
	if err != nil {
		return MonitoringSelectors{}, err
	}
	providers, err := s.monitoringSelectorOptions(ctx, filter.withoutDimension(analyticsFilterProvider), "providers", analyticsProviderSQL(), analyticsProviderSQL(), 200)
	if err != nil {
		return MonitoringSelectors{}, err
	}
	accounts, err := s.monitoringSelectorOptions(ctx, filter.withoutDimension(analyticsFilterAccount), "accounts", analyticsAccountSQL(), analyticsAccountSQL(), 500)
	if err != nil {
		return MonitoringSelectors{}, err
	}
	requestTypes, err := s.monitoringSelectorOptions(ctx, filter.withoutDimension(analyticsFilterRequestType), "request types", analyticsRequestTypeSQL(), analyticsRequestTypeSQL(), 200)
	if err != nil {
		return MonitoringSelectors{}, err
	}
	cacheStatuses, err := s.monitoringSelectorOptions(ctx, filter.withoutDimension(analyticsFilterCacheStatus), "cache statuses", analyticsCacheStatusSQL(), analyticsCacheStatusSQL(), 10)
	if err != nil {
		return MonitoringSelectors{}, err
	}
	failed, err := s.monitoringSelectorOptions(ctx, filter.withoutDimension(analyticsFilterFailed), "failed", "case when failed != 0 then 'true' else 'false' end", "case when failed != 0 then 'failed' else 'succeeded' end", 2)
	if err != nil {
		return MonitoringSelectors{}, err
	}
	return MonitoringSelectors{
		Models:        models,
		Providers:     providers,
		Accounts:      accounts,
		RequestTypes:  requestTypes,
		CacheStatuses: cacheStatuses,
		Failed:        failed,
	}, nil
}

func (s *Store) monitoringSelectorOptions(ctx context.Context, filter AnalyticsFilter, label, valueSQL, labelSQL string, limit int) ([]MonitoringSelectorOption, error) {
	where := buildAnalyticsWhere(filter)
	args := append([]any{}, where.args...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `select `+valueSQL+` as option_value, `+labelSQL+` as option_label, count(*)
		from usage_events`+where.sql+`
		group by option_value, option_label
		order by count(*) desc, option_label asc
		limit ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("monitoring %s selectors: query: %w", label, err)
	}
	defer rows.Close()
	out := []MonitoringSelectorOption{}
	for rows.Next() {
		var option MonitoringSelectorOption
		if err := rows.Scan(&option.Value, &option.Label, &option.Count); err != nil {
			return nil, fmt.Errorf("monitoring %s selectors: scan: %w", label, err)
		}
		option.Value = firstNonEmptyString(option.Value, "-")
		option.Label = firstNonEmptyString(option.Label, option.Value)
		out = append(out, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("monitoring %s selectors: read rows: %w", label, err)
	}
	return out, nil
}
