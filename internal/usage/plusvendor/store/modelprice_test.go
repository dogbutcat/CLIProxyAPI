package plusstore

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func TestModelPriceLoadSaveAndUpsert(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	synced := int64(1234)
	if err := store.SaveModelPrices(ctx, map[string]ModelPrice{
		"gpt-5": {Prompt: 1.25, Completion: 10, Cache: 0.2, CacheRead: 0.1, CacheCreation: 0.3, Source: "manual", SyncedAtMS: &synced},
	}); err != nil {
		t.Fatalf("SaveModelPrices() error = %v", err)
	}
	prices, err := store.LoadModelPrices(ctx)
	if err != nil {
		t.Fatalf("LoadModelPrices() error = %v", err)
	}
	if prices["gpt-5"].Prompt != 1.25 || prices["gpt-5"].SyncedAtMS == nil || *prices["gpt-5"].SyncedAtMS != synced {
		t.Fatalf("loaded price = %+v", prices["gpt-5"])
	}
	result, err := store.UpsertSyncedModelPrices(ctx, map[string]ModelPrice{
		"gpt-5": {Prompt: 2, Completion: 11, Cache: 0.4},
		"bad":   {Prompt: math.NaN()},
	})
	if err != nil {
		t.Fatalf("UpsertSyncedModelPrices() error = %v", err)
	}
	if result.Imported != 1 || result.Skipped != 1 {
		t.Fatalf("sync result = %+v, want imported=1 skipped=1", result)
	}
	prices, err = store.LoadModelPrices(ctx)
	if err != nil {
		t.Fatalf("LoadModelPrices() after sync error = %v", err)
	}
	if prices["gpt-5"].Prompt != 2 || prices["gpt-5"].Source != "sync" || prices["gpt-5"].SourceModelID != "gpt-5" {
		t.Fatalf("synced price = %+v", prices["gpt-5"])
	}
}
