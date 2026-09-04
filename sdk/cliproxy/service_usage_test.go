package cliproxy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceUsageRuntimeStartStopIdempotentCaptures(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	if err := s.startUsageRuntime(ctx); err != nil {
		t.Fatalf("first startUsageRuntime: %v", err)
	}
	bridge := s.UsageBridge()
	if bridge == nil {
		t.Fatal("UsageBridge is nil after start")
	}
	if err := s.startUsageRuntime(ctx); err != nil {
		t.Fatalf("second startUsageRuntime: %v", err)
	}
	if got := s.UsageBridge(); got != bridge {
		t.Fatal("second start replaced usage bridge")
	}

	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:    "openai",
		Model:       "gpt-5",
		Alias:       "alias-gpt-5",
		RequestedAt: time.Now(),
		Detail: coreusage.Detail{
			InputTokens:  4,
			OutputTokens: 5,
			TotalTokens:  9,
		},
	})

	if err := s.stopUsageRuntime(ctx); err != nil {
		t.Fatalf("first stopUsageRuntime: %v", err)
	}
	if err := s.stopUsageRuntime(ctx); err != nil {
		t.Fatalf("second stopUsageRuntime: %v", err)
	}
	if s.UsageBridge() != nil {
		t.Fatal("UsageBridge not cleared after stop")
	}

	events := bridge.Collector().Events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Model != "alias-gpt-5" || events[0].TotalTokens != 9 {
		t.Fatalf("captured event = %#v", events[0])
	}
}

func TestServiceUsageRuntimeCanRestartWithoutDuplicateCapture(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	if err := s.startUsageRuntime(ctx); err != nil {
		t.Fatalf("start 1: %v", err)
	}
	firstBridge := s.UsageBridge()
	if err := s.stopUsageRuntime(ctx); err != nil {
		t.Fatalf("stop 1: %v", err)
	}
	if err := s.startUsageRuntime(ctx); err != nil {
		t.Fatalf("start 2: %v", err)
	}
	secondBridge := s.UsageBridge()
	if secondBridge == nil || secondBridge == firstBridge {
		t.Fatal("restart did not create a fresh usage bridge")
	}
	coreusage.PublishRecord(ctx, coreusage.Record{Provider: "openai", Model: "gpt-5"})
	if err := s.stopUsageRuntime(ctx); err != nil {
		t.Fatalf("stop 2: %v", err)
	}

	if got := len(firstBridge.Collector().Events()); got != 0 {
		t.Fatalf("first bridge events = %d, want 0", got)
	}
	if got := len(secondBridge.Collector().Events()); got != 1 {
		t.Fatalf("second bridge events = %d, want 1", got)
	}
}

func TestUsageLifecycleStopRuntimeReturnsBeforeDeadline(t *testing.T) {
	s := &Service{}
	if err := s.startUsageRuntime(context.Background()); err != nil {
		t.Fatalf("startUsageRuntime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.stopUsageRuntime(ctx); err != nil {
		t.Fatalf("stopUsageRuntime: %v", err)
	}
	if s.UsageBridge() != nil {
		t.Fatal("UsageBridge not cleared after lifecycle stop")
	}
}

func TestServiceUsageRuntimeCancelledStartDoesNotReplaceLastGoodBridge(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	if err := s.startUsageRuntime(ctx); err != nil {
		t.Fatalf("startUsageRuntime: %v", err)
	}
	t.Cleanup(func() {
		if err := s.stopUsageRuntime(context.Background()); err != nil {
			t.Fatalf("stopUsageRuntime: %v", err)
		}
	})
	bridge := s.UsageBridge()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.startUsageRuntime(cancelled); err != nil {
		t.Fatalf("cancelled start while running error = %v", err)
	}
	if got := s.UsageBridge(); got != bridge {
		t.Fatal("cancelled start changed last-good usage bridge")
	}
}

func TestServerOptionsWithRuntimeUsageBridgeAppendsRuntimeBridge(t *testing.T) {
	s := &Service{
		cfg:           &config.Config{},
		serverOptions: []api.ServerOption{api.WithLocalManagementPassword("local-management-key")},
	}
	if err := s.startUsageRuntime(context.Background()); err != nil {
		t.Fatalf("startUsageRuntime: %v", err)
	}
	t.Cleanup(func() {
		if err := s.stopUsageRuntime(context.Background()); err != nil {
			t.Fatalf("stopUsageRuntime: %v", err)
		}
	})

	opts := s.serverOptionsWithRuntimeUsageBridge()
	if got, want := len(opts), len(s.serverOptions)+1; got != want {
		t.Fatalf("server options length = %d, want %d", got, want)
	}
	server := api.NewServer(&config.Config{}, nil, nil, "", opts...)
	if server.UsageBridge() != s.UsageBridge() {
		t.Fatal("api server did not receive runtime usage bridge")
	}
}

func TestServiceUsageRuntimeUsesAuthDirUsageDB(t *testing.T) {
	authDir := t.TempDir()
	s := &Service{cfg: &config.Config{AuthDir: authDir}}
	if err := s.startUsageRuntime(context.Background()); err != nil {
		t.Fatalf("startUsageRuntime: %v", err)
	}
	t.Cleanup(func() {
		if err := s.stopUsageRuntime(context.Background()); err != nil {
			t.Fatalf("stopUsageRuntime: %v", err)
		}
	})

	bridge := s.UsageBridge()
	if bridge == nil {
		t.Fatal("UsageBridge is nil after start")
	}
	if got, want := bridge.DBPath(), filepath.Join(authDir, "data", "usage.db"); got != want {
		t.Fatalf("UsageBridge DBPath = %q, want %q", got, want)
	}
}

func TestServerOptionsWithRuntimeUsageBridgeSkipsMissingBridge(t *testing.T) {
	s := &Service{
		cfg:           &config.Config{},
		serverOptions: []api.ServerOption{api.WithLocalManagementPassword("local-management-key")},
	}
	opts := s.serverOptionsWithRuntimeUsageBridge()
	if got, want := len(opts), len(s.serverOptions); got != want {
		t.Fatalf("server options length = %d, want %d", got, want)
	}
}

func TestUsageLifecycleRunResetsShutdownForRetryableRestart(t *testing.T) {
	s := &Service{}
	s.shutdownOnce.Do(func() {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx); err == nil {
		t.Fatal("Run() error = nil, want cancelled startup")
	}
	called := false
	s.shutdownOnce.Do(func() { called = true })
	if !called {
		t.Fatal("Run() did not reset shutdownOnce for retryable restart")
	}
}
