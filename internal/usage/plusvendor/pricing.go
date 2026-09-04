package plusvendor

import (
	"context"
	"fmt"
	"io"
	"strings"

	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

type ModelPriceSyncResponse struct {
	Prices        map[string]plusstore.ModelPrice `json:"prices"`
	Imported      int                             `json:"imported"`
	Skipped       int                             `json:"skipped"`
	Source        string                          `json:"source,omitempty"`
	Sources       []string                        `json:"sources,omitempty"`
	SourceResults []ModelPriceSyncSourceResult    `json:"sourceResults,omitempty"`
}

type ModelPriceSyncSourceResult struct {
	Source  string `json:"source"`
	Models  int    `json:"models"`
	Skipped int    `json:"skipped"`
	Error   string `json:"error,omitempty"`
}

func (s *UsageService) ModelPrices(ctx context.Context) (map[string]plusstore.ModelPrice, error) {
	if s == nil || s.auxiliary == nil {
		return nil, fmt.Errorf("model prices: store is nil")
	}
	return s.auxiliary.LoadModelPrices(ctx)
}

func (s *UsageService) SaveModelPrices(ctx context.Context, prices map[string]plusstore.ModelPrice) (map[string]plusstore.ModelPrice, error) {
	if s == nil || s.auxiliary == nil {
		return nil, fmt.Errorf("save model prices: store is nil")
	}
	if err := s.auxiliary.SaveModelPrices(ctx, prices); err != nil {
		return nil, err
	}
	return s.auxiliary.LoadModelPrices(ctx)
}

func (s *UsageService) DeleteModelPrice(ctx context.Context, model string) (map[string]plusstore.ModelPrice, error) {
	if s == nil || s.auxiliary == nil {
		return nil, fmt.Errorf("delete model price: store is nil")
	}
	if err := s.auxiliary.DeleteModelPrice(ctx, model); err != nil {
		return nil, err
	}
	return s.auxiliary.LoadModelPrices(ctx)
}

func (s *UsageService) SyncModelPrices(ctx context.Context, models []string) (ModelPriceSyncResponse, error) {
	if s == nil || s.auxiliary == nil {
		return ModelPriceSyncResponse{}, fmt.Errorf("sync model prices: store is nil")
	}
	prices, err := s.auxiliary.LoadModelPrices(ctx)
	if err != nil {
		return ModelPriceSyncResponse{}, err
	}
	requested := normalizePriceModelList(models)
	if len(requested) == 0 {
		return ModelPriceSyncResponse{
			Prices:  prices,
			Source:  "integrated",
			Sources: []string{"integrated"},
			SourceResults: []ModelPriceSyncSourceResult{{
				Source: "integrated",
				Models: len(prices),
				Error:  "unsupported",
			}},
		}, nil
	}
	matched := map[string]plusstore.ModelPrice{}
	for _, model := range requested {
		if price, ok := prices[model]; ok {
			matched[model] = price
		}
	}
	result, err := s.auxiliary.UpsertSyncedModelPrices(ctx, matched)
	if err != nil {
		return ModelPriceSyncResponse{}, err
	}
	prices, err = s.auxiliary.LoadModelPrices(ctx)
	if err != nil {
		return ModelPriceSyncResponse{}, err
	}
	return ModelPriceSyncResponse{
		Prices:   prices,
		Imported: result.Imported,
		Skipped:  result.Skipped + len(requested) - len(matched),
		Source:   "integrated",
		Sources:  []string{"integrated"},
		SourceResults: []ModelPriceSyncSourceResult{{
			Source:  "integrated",
			Models:  result.Imported,
			Skipped: result.Skipped + len(requested) - len(matched),
		}},
	}, nil
}

func (s *UsageService) ModelPriceUsageSummary(ctx context.Context, limit int) (plusstore.ModelPriceUsageSummary, error) {
	if s == nil || s.auxiliary == nil {
		return plusstore.ModelPriceUsageSummary{}, fmt.Errorf("model price usage summary: store is nil")
	}
	return s.auxiliary.ModelPriceUsageSummary(ctx, limit)
}

func (s *UsageService) APIKeyAliases(ctx context.Context) ([]plusstore.APIKeyAlias, error) {
	if s == nil || s.auxiliary == nil {
		return nil, fmt.Errorf("api key aliases: store is nil")
	}
	return s.auxiliary.LoadAPIKeyAliases(ctx)
}

func (s *UsageService) SaveAPIKeyAliases(ctx context.Context, aliases []plusstore.APIKeyAlias, activeHashes []string, cleanupOrphans bool) ([]plusstore.APIKeyAlias, error) {
	if s == nil || s.auxiliary == nil {
		return nil, fmt.Errorf("save api key aliases: store is nil")
	}
	return s.auxiliary.SaveAPIKeyAliases(ctx, aliases, activeHashes, cleanupOrphans)
}

func (s *UsageService) DeleteAPIKeyAlias(ctx context.Context, apiKeyHash string) error {
	if s == nil || s.auxiliary == nil {
		return fmt.Errorf("delete api key alias: store is nil")
	}
	return s.auxiliary.DeleteAPIKeyAlias(ctx, apiKeyHash)
}

func (s *UsageService) ExportEventsJSONL(ctx context.Context, w io.Writer) (int, error) {
	if s == nil || s.auxiliary == nil {
		return 0, fmt.Errorf("export usage: store is nil")
	}
	return s.auxiliary.ExportEventsJSONL(ctx, w)
}

func (s *UsageService) ImportEventsJSONL(ctx context.Context, r io.Reader) (plusstore.UsageImportResult, error) {
	if s == nil || s.auxiliary == nil {
		return plusstore.UsageImportResult{}, fmt.Errorf("import usage: store is nil")
	}
	return s.auxiliary.ImportEventsJSONL(ctx, r)
}

func normalizePriceModelList(models []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}
