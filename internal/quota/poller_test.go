package quota

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollSingleDeduplicatesAliasesAndSnapshots(t *testing.T) {
	var calls atomic.Int64
	poller := NewPoller(PollerConfig{
		Entries: []PollerEntry{
			{Name: "primary", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"},
			{Name: "alias", WorkspaceID: "workspace-1", AuthCookie: "cookie-b"},
		},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			calls.Add(1)
			if input.WorkspaceID != "workspace-1" {
				t.Fatalf("WorkspaceID = %q, want workspace-1", input.WorkspaceID)
			}
			if input.AuthCookie != "cookie-b" {
				t.Fatalf("AuthCookie = %q, want requested entry cookie", input.AuthCookie)
			}
			return validSSRDashboard(), nil
		},
	})

	result := poller.PollSingle(context.Background(), "alias")
	if result == nil || result.Error != nil {
		t.Fatalf("PollSingle() = %+v", result)
	}
	if result.EntryName != "alias" {
		t.Fatalf("EntryName = %q, want alias", result.EntryName)
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls.Load())
	}

	snapshot := poller.Results()
	if len(snapshot) != 2 || snapshot["primary"] == nil || snapshot["alias"] == nil {
		t.Fatalf("snapshot keys = %+v, want primary and alias", snapshot)
	}
	snapshot["alias"].Quota.Rolling.PercentRemaining = 1
	fresh := poller.Results()
	if fresh["alias"].Quota.Rolling.PercentRemaining != 90 {
		t.Fatalf("snapshot mutation changed stored result: %+v", fresh["alias"].Quota.Rolling)
	}
}

func TestScheduledPollingDeduplicatesWorkspaces(t *testing.T) {
	var calls atomic.Int64
	resultCh := make(chan string, 4)
	poller := NewPoller(PollerConfig{
		Interval: time.Hour,
		Entries: []PollerEntry{
			{Name: "a", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"},
			{Name: "b", WorkspaceID: "workspace-1", AuthCookie: "cookie-b"},
		},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			calls.Add(1)
			return validSSRDashboard(), nil
		},
		OnResult: func(entry PollerEntry, result *PollResult) {
			resultCh <- entry.Name
		},
	})

	poller.Start(context.Background())
	defer poller.Stop()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-resultCh:
			seen[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for aliases, seen = %+v", seen)
		}
	}
	poller.Stop()

	if calls.Load() != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls.Load())
	}
	if len(poller.Results()) != 2 {
		t.Fatalf("results = %+v, want two aliased snapshots", poller.Results())
	}
}

func TestScheduledPollerPreparesAliasesBeforeFetchAndCompletesPublishedResults(t *testing.T) {
	var events []string
	poller := NewPoller(PollerConfig{
		Entries: []PollerEntry{
			{Name: "primary", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"},
			{Name: "alias", WorkspaceID: "workspace-1", AuthCookie: "cookie-b"},
		},
		PreparePollAttempt: func(source PollAttemptSource, aliases []PollerEntry) PollAttemptCompletion {
			if source != PollAttemptSourceScheduled {
				t.Fatalf("prepared source = %v, want scheduled", source)
			}
			if len(aliases) != 2 || aliases[0].Name != "primary" || aliases[1].Name != "alias" {
				t.Fatalf("prepared aliases = %+v, want primary and alias", aliases)
			}
			events = append(events, "prepare")
			return func(entry PollerEntry, result *PollResult) {
				if result == nil || result.EntryName != entry.Name || result.Error != nil || result.Quota == nil {
					t.Fatalf("completion(%q) result = %+v", entry.Name, result)
				}
				events = append(events, "complete:"+entry.Name)
			}
		},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			events = append(events, "fetch")
			return validSSRDashboard(), nil
		},
	})

	poller.pollAll(context.Background())

	want := []string{"prepare", "fetch", "complete:primary", "complete:alias"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
	if snapshot := poller.Results(); len(snapshot) != 2 || snapshot["primary"] == nil || snapshot["alias"] == nil {
		t.Fatalf("snapshot = %+v, want both aliases", snapshot)
	}
}

func TestManualPollerAttemptCompletionPublishesSharedError(t *testing.T) {
	fetchErr := errors.New("dashboard unavailable")
	var prepared int
	var events []string
	completed := make(map[string]error)
	poller := NewPoller(PollerConfig{
		Entries: []PollerEntry{
			{Name: "primary", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"},
			{Name: "alias", WorkspaceID: "workspace-1", AuthCookie: "cookie-b"},
		},
		PreparePollAttempt: func(source PollAttemptSource, aliases []PollerEntry) PollAttemptCompletion {
			if source != PollAttemptSourceManual {
				t.Fatalf("prepared source = %v, want manual", source)
			}
			if len(aliases) != 2 || aliases[0].Name != "primary" || aliases[1].Name != "alias" {
				t.Fatalf("prepared aliases = %+v, want primary and alias", aliases)
			}
			prepared++
			events = append(events, "prepare")
			return func(entry PollerEntry, result *PollResult) {
				completed[entry.Name] = result.Error
			}
		},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			events = append(events, "fetch")
			return "", fetchErr
		},
	})

	result := poller.PollSingle(context.Background(), "alias")
	if result == nil || result.EntryName != "alias" || !errors.Is(result.Error, fetchErr) {
		t.Fatalf("PollSingle(alias) = %+v, want shared fetch error", result)
	}
	if prepared != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepared)
	}
	if len(events) != 2 || events[0] != "prepare" || events[1] != "fetch" {
		t.Fatalf("events = %v, want prepare before fetch", events)
	}
	if len(completed) != 2 || !errors.Is(completed["primary"], fetchErr) || !errors.Is(completed["alias"], fetchErr) {
		t.Fatalf("completed errors = %+v, want error for both aliases", completed)
	}
}

func TestManualPollerNilAttemptSeamPreservesPublication(t *testing.T) {
	resultCh := make(chan string, 2)
	poller := NewPoller(PollerConfig{
		Entries: []PollerEntry{
			{Name: "primary", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"},
			{Name: "alias", WorkspaceID: "workspace-1", AuthCookie: "cookie-b"},
		},
		PreparePollAttempt: func(source PollAttemptSource, aliases []PollerEntry) PollAttemptCompletion {
			if source != PollAttemptSourceManual {
				t.Fatalf("prepared source = %v, want manual", source)
			}
			if len(aliases) != 2 || aliases[0].Name != "primary" || aliases[1].Name != "alias" {
				t.Fatalf("prepared aliases = %+v, want primary and alias", aliases)
			}
			return nil
		},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			return validSSRDashboard(), nil
		},
		OnResult: func(entry PollerEntry, result *PollResult) {
			resultCh <- entry.Name
		},
	})

	result := poller.PollSingle(context.Background(), "alias")
	if result == nil || result.Error != nil || result.EntryName != "alias" {
		t.Fatalf("PollSingle(alias) = %+v, want successful alias result", result)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		seen[<-resultCh] = true
	}
	if !seen["primary"] || !seen["alias"] {
		t.Fatalf("OnResult aliases = %+v, want primary and alias", seen)
	}
	if snapshot := poller.Results(); len(snapshot) != 2 || snapshot["primary"] == nil || snapshot["alias"] == nil {
		t.Fatalf("snapshot = %+v, want both aliases", snapshot)
	}
}

func TestScheduledPollingSkipsDeduplicatedWorkspaceAndManualBypasses(t *testing.T) {
	var calls atomic.Int64
	var seenCookies []string
	poller := NewPoller(PollerConfig{
		Entries: []PollerEntry{
			{Name: "primary", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"},
			{Name: "alias", WorkspaceID: "workspace-1", AuthCookie: "cookie-b"},
		},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			calls.Add(1)
			seenCookies = append(seenCookies, input.AuthCookie)
			return validSSRDashboard(), nil
		},
	})

	if !poller.SkipEntry("alias") {
		t.Fatal("SkipEntry(alias) returned false, want true")
	}
	if !poller.IsSkipped("primary") || !poller.IsSkipped("workspace-1") {
		t.Fatal("deduplicated workspace was not skipped for primary/workspace aliases")
	}

	poller.pollAll(context.Background())
	if got := calls.Load(); got != 0 {
		t.Fatalf("scheduled fetch calls while skipped = %d, want 0", got)
	}

	result := poller.PollSingle(context.Background(), "alias")
	if result == nil || result.Error != nil || result.EntryName != "alias" {
		t.Fatalf("PollSingle(alias) = %+v, want successful alias result", result)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("manual fetch calls = %d, want 1", got)
	}
	if len(seenCookies) != 1 || seenCookies[0] != "cookie-b" {
		t.Fatalf("manual fetch cookies = %v, want requested alias cookie-b", seenCookies)
	}
	if snapshot := poller.Results(); len(snapshot) != 2 || snapshot["primary"] == nil || snapshot["alias"] == nil {
		t.Fatalf("manual snapshot = %+v, want both aliases", snapshot)
	}

	if !poller.UnskipEntry("workspace-1") {
		t.Fatal("UnskipEntry(workspace-1) returned false, want true")
	}
	poller.pollAll(context.Background())
	if got := calls.Load(); got != 2 {
		t.Fatalf("scheduled fetch calls after unskip = %d, want 2", got)
	}
}

func TestStopCancelsInFlightPoll(t *testing.T) {
	started := make(chan struct{})
	poller := NewPoller(PollerConfig{
		Interval: time.Hour,
		Entries:  []PollerEntry{{Name: "a", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"}},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
	})

	poller.Start(context.Background())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not start")
	}

	done := make(chan struct{})
	go func() {
		poller.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel in-flight poll")
	}

	results := poller.Results()
	if results["a"] == nil || !errors.Is(results["a"].Error, context.Canceled) {
		t.Fatalf("result error = %v, want context.Canceled", results["a"])
	}
}

func TestPollerConcurrentAccess(t *testing.T) {
	poller := NewPoller(PollerConfig{
		Interval: time.Millisecond,
		Entries:  []PollerEntry{{Name: "a", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"}},
		FetchDashboard: func(ctx context.Context, input DashboardFetchInput) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
				return validSSRDashboard(), nil
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	poller.Start(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = poller.Results()
				_ = poller.PollSingle(context.Background(), "a")
				_ = poller.SkipEntry("a")
				_ = poller.IsSkipped("a")
				_ = poller.UnskipEntry("a")
			}
		}()
	}
	wg.Wait()
	cancel()
	poller.Stop()
}

func TestPollSingleUnknownEntry(t *testing.T) {
	poller := NewPoller(PollerConfig{
		Entries: []PollerEntry{{Name: "a", WorkspaceID: "workspace-1", AuthCookie: "cookie-a"}},
	})
	if got := poller.PollSingle(context.Background(), "missing"); got != nil {
		t.Fatalf("PollSingle() = %+v, want nil", got)
	}
}
