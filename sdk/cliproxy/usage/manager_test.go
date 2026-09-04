package usage

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStreamFromContextDefaultsMissingToFalse(t *testing.T) {
	if StreamFromContext(context.Background()) {
		t.Fatalf("StreamFromContext(background) = true, want false")
	}
}

func TestStreamFromContextHonorsExplicitTrue(t *testing.T) {
	ctx := WithStream(context.Background(), true)
	if !StreamFromContext(ctx) {
		t.Fatalf("StreamFromContext(true) = false, want true")
	}
}

func TestRecordStreamField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		Stream:   true,
	}
	if !record.Stream {
		t.Fatalf("Record.Stream = false, want true")
	}
}

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}

func TestManagerUnregisterNamedRemovesPluginAndKeepsIndexes(t *testing.T) {
	manager := NewManager(4)
	var mu sync.Mutex
	calls := []string{}
	recordCall := func(name string) Plugin {
		return usagePluginFunc(func(context.Context, Record) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name)
		})
	}
	manager.RegisterNamed("a", recordCall("a"))
	manager.RegisterNamed("b", recordCall("b"))
	manager.RegisterNamed("c", recordCall("c"))
	manager.UnregisterNamed("b")
	manager.RegisterNamed("c", recordCall("c2"))
	manager.Start(context.Background())
	manager.Publish(context.Background(), Record{Provider: "test"})
	manager.Stop()

	mu.Lock()
	defer mu.Unlock()
	want := []string{"a", "c2"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestManagerRestartAfterStop(t *testing.T) {
	manager := NewManager(4)
	var count atomic.Int64
	manager.RegisterNamed("count", usagePluginFunc(func(context.Context, Record) { count.Add(1) }))
	manager.Start(context.Background())
	manager.Publish(context.Background(), Record{Provider: "first"})
	manager.Stop()
	manager.Start(context.Background())
	manager.Publish(context.Background(), Record{Provider: "second"})
	manager.Stop()
	if got := count.Load(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestManagerSanitizesSecretResponseHeaders(t *testing.T) {
	manager := NewManager(4)
	defer manager.Stop()
	got := make(chan http.Header, 1)
	manager.RegisterNamed("capture", usagePluginFunc(func(_ context.Context, record Record) {
		got <- record.ResponseHeaders
	}))
	manager.Publish(context.Background(), Record{
		ResponseHeaders: http.Header{
			"Authorization":     {"Bearer secret"},
			"Cookie":            {"session=secret"},
			"X-Api-Key":         {"secret"},
			"X-Request-Id":      {"safe"},
			"X-Codex-Plan-Type": {"plus"},
		},
	})
	manager.Stop()
	headers := <-got
	if headers.Get("Authorization") != "" || headers.Get("Cookie") != "" || headers.Get("X-Api-Key") != "" {
		t.Fatalf("secret headers survived sanitation: %#v", headers)
	}
	if headers.Get("X-Request-Id") != "safe" || headers.Get("X-Codex-Plan-Type") != "plus" {
		t.Fatalf("safe headers missing after sanitation: %#v", headers)
	}
}

type usagePluginFunc func(context.Context, Record)

func (f usagePluginFunc) HandleUsage(ctx context.Context, record Record) {
	f(ctx, record)
}
