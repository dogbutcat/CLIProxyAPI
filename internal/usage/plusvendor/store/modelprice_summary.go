package plusstore

import (
	"context"
	"fmt"
)

func (s *Store) ModelPriceUsageSummary(ctx context.Context, limit int) (ModelPriceUsageSummary, error) {
	if s == nil || s.db == nil {
		return ModelPriceUsageSummary{}, fmt.Errorf("model price usage summary: store is nil")
	}
	if limit <= 0 {
		limit = 50000
	}
	total, err := s.CountEvents(ctx)
	if err != nil {
		return ModelPriceUsageSummary{}, fmt.Errorf("model price usage summary: count events: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `with sampled as (
		select model, requested_model, resolved_model
		from usage_events
		order by timestamp_ms desc, id desc
		limit ?
	)
	select model_key, sum(calls), sum(requested_calls), sum(resolved_calls)
	from (
		select coalesce(nullif(model,''), '-') as model_key, count(*) as calls, 0 as requested_calls, 0 as resolved_calls
		from sampled
		group by model_key
		union all
		select requested_model as model_key, 0 as calls, count(*) as requested_calls, 0 as resolved_calls
		from sampled
		where coalesce(requested_model,'') != ''
		group by requested_model
		union all
		select resolved_model as model_key, 0 as calls, 0 as requested_calls, count(*) as resolved_calls
		from sampled
		where coalesce(resolved_model,'') != ''
		group by resolved_model
	)
	group by model_key
	order by sum(calls) desc, sum(requested_calls) desc, sum(resolved_calls) desc, model_key asc`, limit)
	if err != nil {
		return ModelPriceUsageSummary{}, fmt.Errorf("model price usage summary: query: %w", err)
	}
	defer rows.Close()
	out := ModelPriceUsageSummary{TotalEvents: total, Truncated: total > int64(limit)}
	if out.Truncated {
		out.SampledEvents = int64(limit)
	} else {
		out.SampledEvents = total
	}
	for rows.Next() {
		var stat ModelPriceUsageStat
		if err := rows.Scan(&stat.Model, &stat.Calls, &stat.RequestedCalls, &stat.ResolvedCalls); err != nil {
			return ModelPriceUsageSummary{}, fmt.Errorf("model price usage summary: scan: %w", err)
		}
		out.Models = append(out.Models, stat)
	}
	if err := rows.Err(); err != nil {
		return ModelPriceUsageSummary{}, fmt.Errorf("model price usage summary: read rows: %w", err)
	}
	return out, nil
}
