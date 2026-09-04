package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestBeginManualQueryRejectsInactiveInputsWithoutIssuingTicket(t *testing.T) {
	t.Run("nil manager", func(t *testing.T) {
		_, resolved := newManualHubTestManager(t, "manual-nil-manager")
		if completion := beginManualQueryWithTable(context.Background(), nil, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(nil)); completion != nil {
			t.Fatal("nil manager returned a completion")
		}
	})

	tests := []struct {
		name   string
		mutate func(*auth.Auth) *auth.Auth
		method string
		url    func(*testing.T) *url.URL
		match  bool
	}{
		{name: "nil auth", mutate: func(*auth.Auth) *auth.Auth { return nil }, method: "GET", url: mustManualTestURL, match: true},
		{name: "empty ID", mutate: func(resolved *auth.Auth) *auth.Auth { resolved.ID = ""; return resolved }, method: "GET", url: mustManualTestURL, match: true},
		{name: "empty provider", mutate: func(resolved *auth.Auth) *auth.Auth { resolved.Provider = ""; return resolved }, method: "GET", url: mustManualTestURL, match: true},
		{name: "unmatched", mutate: func(resolved *auth.Auth) *auth.Auth { return resolved }, method: "GET", url: mustManualTestURL},
		{name: "invalid method", mutate: func(resolved *auth.Auth) *auth.Auth { return resolved }, method: "GET\n", url: mustManualTestURL, match: true},
		{name: "invalid URL", mutate: func(resolved *auth.Auth) *auth.Auth { return resolved }, method: "GET", url: func(*testing.T) *url.URL { return &url.URL{Path: "/quota"} }, match: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, current := newManualHubTestManager(t, "manual-rejected-"+tt.name)
			candidate := tt.mutate(current.Clone())
			table := newManualAdapterTable(manualHubTestAdapter(tt.match, nil))
			if completion := beginManualQueryWithTable(context.Background(), manager, candidate, tt.method, tt.url(t), table); completion != nil {
				t.Fatal("rejected input returned a completion")
			}
			assertNextBoundTicketOrder(t, manager, current, 1)
		})
	}

	for _, disabled := range []*auth.Auth{
		{ID: "manual-disabled-flag", Provider: "test-provider", Status: auth.StatusActive, Disabled: true},
		{ID: "manual-disabled-status", Provider: "test-provider", Status: auth.StatusDisabled},
	} {
		t.Run(disabled.ID, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			resolved := registerManualTestAuth(t, manager, disabled)
			if completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(nil)); completion != nil {
				t.Fatal("disabled auth returned a completion")
			}
			enabled, err := manager.Update(context.Background(), &auth.Auth{ID: resolved.ID, Provider: resolved.Provider, Status: auth.StatusActive})
			if err != nil || enabled == nil {
				t.Fatalf("Update() = %v, %v", enabled, err)
			}
			assertNextBoundTicketOrder(t, manager, enabled, 1)
		})
	}
}

func TestBeginManualQueryCopiesImmutableURLScalars(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-immutable-url")
	queryURL, err := url.Parse("https://quota.example.test:8443/quota%2Fdaily?workspace=a%2Fb")
	if err != nil {
		t.Fatal(err)
	}
	original := *queryURL
	var captured manualQueryMetadata
	table := newManualAdapterTable(manualQueryAdapter{
		provider: resolved.Provider,
		match: func(query manualQueryMetadata) bool {
			captured = query
			query.Scheme = "mutated"
			query.Host = "mutated.invalid"
			query.Path = "/mutated"
			query.RawQuery = "mutated=true"
			return true
		},
		observe: scoreOnlyManualObservation(10),
	})
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "get", queryURL, table)
	if completion == nil {
		t.Fatal("matched immutable query returned nil completion")
	}
	if !reflect.DeepEqual(*queryURL, original) {
		t.Fatalf("Begin mutated caller URL: got %+v, want %+v", *queryURL, original)
	}
	if captured.Provider != resolved.Provider || captured.Method != "GET" || captured.Scheme != "https" ||
		captured.Host != "quota.example.test:8443" || captured.Hostname != "quota.example.test" || captured.Port != "8443" ||
		captured.Path != "/quota/daily" || captured.EscapedPath != "/quota%2Fdaily" || captured.RawQuery != "workspace=a%2Fb" {
		t.Fatalf("captured metadata = %+v", captured)
	}
}

func TestBeginManualQueryMatchesBeforeCapturingBoundTicket(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-match-order")
	matchCalled := false
	table := newManualAdapterTable(manualQueryAdapter{
		provider: resolved.Provider,
		match: func(manualQueryMetadata) bool {
			matchCalled = true
			assertNextBoundTicketOrder(t, manager, resolved, 1)
			return true
		},
		observe: scoreOnlyManualObservation(25),
	})
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), table)
	if completion == nil || !matchCalled {
		t.Fatal("matched query did not return a completion")
	}
	assertNextBoundTicketOrder(t, manager, resolved, 3)
}

func TestBeginManualQueryLifecycleChangeDuringMatchRejectsWithoutConsumingOrder(t *testing.T) {
	tests := []struct {
		name     string
		disabled bool
	}{
		{name: "replacement"},
		{name: "disable", disabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, stale := newManualHubTestManager(t, "manual-match-"+tt.name)
			var current *auth.Auth
			table := newManualAdapterTable(manualQueryAdapter{
				provider: stale.Provider,
				match: func(manualQueryMetadata) bool {
					status := auth.StatusActive
					if tt.disabled {
						status = auth.StatusDisabled
					}
					updated, err := manager.Update(context.Background(), &auth.Auth{ID: stale.ID, Provider: stale.Provider, Status: status})
					if err != nil || updated == nil {
						t.Fatalf("Update() = %v, %v", updated, err)
					}
					current = updated
					return true
				},
				observe: scoreOnlyManualObservation(30),
			})
			if completion := beginManualQueryWithTable(context.Background(), manager, stale, "GET", mustManualTestURL(t), table); completion != nil {
				t.Fatal("stale snapshot returned a completion")
			}
			if tt.disabled {
				enabled, err := manager.Update(context.Background(), &auth.Auth{ID: current.ID, Provider: current.Provider, Status: auth.StatusActive})
				if err != nil || enabled == nil {
					t.Fatalf("re-enable Update() = %v, %v", enabled, err)
				}
				current = enabled
			}
			assertNextBoundTicketOrder(t, manager, current, 1)
		})
	}
}

func TestBeginManualQueryCurrentExactIDSnapshotApplies(t *testing.T) {
	authID := "  manual-exact-id  "
	manager, resolved := newManualHubTestManager(t, authID)
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(scoreOnlyManualObservation(37)))
	if completion == nil {
		t.Fatal("current exact-ID snapshot returned nil completion")
	}
	completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: []byte("quota")})
	if score, ok := manager.QuotaScore(authID); !ok || score != 37 {
		t.Fatalf("QuotaScore(raw ID) = %v, %v; want 37, true", score, ok)
	}
	if _, ok := manager.QuotaScore("manual-exact-id"); ok {
		t.Fatal("completion rewrote the exact auth ID to its trimmed form")
	}
	assertNextBoundTicketOrder(t, manager, resolved, 2)
}

func TestBeginManualQueryCompletionFiltersStatusAndBodyCap(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		bodySize   int
		wantCalls  int32
	}{
		{name: "2xx", statusCode: 204, bodySize: 1, wantCalls: 1},
		{name: "below 2xx", statusCode: 199, bodySize: 1},
		{name: "above 2xx", statusCode: 300, bodySize: 1},
		{name: "body at cap", statusCode: 200, bodySize: manualQueryMaxBodyBytes, wantCalls: 1},
		{name: "body above cap", statusCode: 200, bodySize: manualQueryMaxBodyBytes + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, resolved := newManualHubTestManager(t, "manual-filter-"+tt.name)
			var calls atomic.Int32
			observe := func(manualResponseMetadata, io.Reader) (Observation, error) {
				calls.Add(1)
				return scoreOnlyObservation(41), nil
			}
			completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(observe))
			if completion == nil {
				t.Fatal("matched query returned nil completion")
			}
			completion(context.Background(), ManualQueryResponse{StatusCode: tt.statusCode, Body: make([]byte, tt.bodySize)})
			if got := calls.Load(); got != tt.wantCalls {
				t.Fatalf("adapter calls = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestBeginManualQueryCompletionBorrowsReadOnlyBodySynchronously(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-read-only-body")
	body := []byte("quota snapshot")
	original := append([]byte(nil), body...)
	before := time.Now().UTC()
	serverDate := "Mon, 02 Jan 2006 15:04:05 GMT"
	var gotMetadata manualResponseMetadata
	var retained io.Reader
	adapterReturned := false
	observe := func(metadata manualResponseMetadata, reader io.Reader) (Observation, error) {
		gotMetadata = metadata
		retained = reader
		gotBody, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(gotBody, original) {
			t.Fatalf("ReadAll() = %q, %v", gotBody, err)
		}
		adapterReturned = true
		return scoreOnlyObservation(52), nil
	}
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(observe))
	completion(context.Background(), ManualQueryResponse{StatusCode: 200, ServerDate: serverDate, Body: body})
	after := time.Now().UTC()
	if !adapterReturned || !bytes.Equal(body, original) {
		t.Fatal("adapter did not finish synchronously or caller body changed")
	}
	buffer := make([]byte, 1)
	if read, err := retained.Read(buffer); read != 0 || !errors.Is(err, errManualQueryBodyExpired) {
		t.Fatalf("retained reader Read() = %d, %v; want 0, expired", read, err)
	}
	wantDate := time.Date(2006, time.January, 2, 15, 4, 5, 0, time.UTC)
	if !gotMetadata.ServerDate.Equal(wantDate) {
		t.Fatalf("server date = %v, want %v", gotMetadata.ServerDate, wantDate)
	}
	if gotMetadata.CompletedAt.Before(before) || gotMetadata.CompletedAt.After(after) || gotMetadata.CompletedAt.Location() != time.UTC {
		t.Fatalf("completion time = %v, want capture in [%v, %v] UTC", gotMetadata.CompletedAt, before, after)
	}
}

func TestBeginManualQueryInvalidatesRetainedBodyOnAdapterExit(t *testing.T) {
	tests := []struct {
		name    string
		observe func(io.Reader) (Observation, error)
	}{
		{name: "success", observe: func(io.Reader) (Observation, error) {
			return scoreOnlyObservation(20), nil
		}},
		{name: "error", observe: func(io.Reader) (Observation, error) {
			return Observation{}, errors.New("adapter-secret")
		}},
		{name: "panic", observe: func(io.Reader) (Observation, error) {
			panic("panic-secret")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, resolved := newManualHubTestManager(t, "manual-expire-"+tt.name)
			body := []byte("borrowed-body-secret")
			original := append([]byte(nil), body...)
			var retained io.Reader
			observe := func(_ manualResponseMetadata, reader io.Reader) (Observation, error) {
				retained = reader
				return tt.observe(reader)
			}
			completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(observe))
			completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: body})
			if !bytes.Equal(body, original) {
				t.Fatal("adapter path changed caller body")
			}
			buffer := make([]byte, len(body))
			if read, err := retained.Read(buffer); read != 0 || !errors.Is(err, errManualQueryBodyExpired) {
				t.Fatalf("retained reader Read() = %d, %v; want 0, expired", read, err)
			}
			if bytes.Contains(buffer, []byte("borrowed-body-secret")) {
				t.Fatal("expired reader exposed original body")
			}
		})
	}
}

func TestBeginManualQueryBorrowedBodyConcurrentReadAndInvalidation(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-reader-race")
	body := bytes.Repeat([]byte("quota"), 1024)
	original := append([]byte(nil), body...)
	var retained io.Reader
	var readers sync.WaitGroup
	start := make(chan struct{})
	observe := func(_ manualResponseMetadata, reader io.Reader) (Observation, error) {
		retained = reader
		for range 32 {
			readers.Add(1)
			go func() {
				defer readers.Done()
				<-start
				_, _ = reader.Read(make([]byte, 17))
			}()
		}
		close(start)
		return scoreOnlyObservation(21), nil
	}
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(observe))
	completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: body})
	readers.Wait()
	if !bytes.Equal(body, original) {
		t.Fatal("concurrent reads changed caller body")
	}
	if read, err := retained.Read(make([]byte, 1)); read != 0 || !errors.Is(err, errManualQueryBodyExpired) {
		t.Fatalf("post-completion Read() = %d, %v; want 0, expired", read, err)
	}
}

func TestBeginManualQueryCompletionInvalidDateUsesZeroTime(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-invalid-date")
	var gotDate time.Time
	observe := func(metadata manualResponseMetadata, _ io.Reader) (Observation, error) {
		gotDate = metadata.ServerDate
		return scoreOnlyObservation(33), nil
	}
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(observe))
	completion(context.Background(), ManualQueryResponse{StatusCode: 200, ServerDate: "secret-not-a-date"})
	if !gotDate.IsZero() {
		t.Fatalf("invalid server date = %v, want zero", gotDate)
	}
}

func TestBeginManualQueryCompletionIsOnceAndConcurrentSafe(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-once")
	var calls atomic.Int32
	observe := func(manualResponseMetadata, io.Reader) (Observation, error) {
		calls.Add(1)
		return scoreOnlyObservation(61), nil
	}
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(observe))
	var waitGroup sync.WaitGroup
	for range 32 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: []byte("same response")})
		}()
	}
	waitGroup.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("adapter calls = %d, want 1", got)
	}
}

func TestBeginManualQueryCompletionFailuresAreOnceNoOpsAndDoNotPanic(t *testing.T) {
	tests := []struct {
		name    string
		observe func(*atomic.Int32) manualQueryObserveFunc
	}{
		{name: "adapter error", observe: func(calls *atomic.Int32) manualQueryObserveFunc {
			return func(manualResponseMetadata, io.Reader) (Observation, error) {
				calls.Add(1)
				return Observation{}, errors.New("body-secret adapter failure")
			}
		}},
		{name: "invalid observation", observe: func(calls *atomic.Int32) manualQueryObserveFunc {
			return func(manualResponseMetadata, io.Reader) (Observation, error) {
				calls.Add(1)
				return Observation{}, nil
			}
		}},
		{name: "adapter panic", observe: func(calls *atomic.Int32) manualQueryObserveFunc {
			return func(manualResponseMetadata, io.Reader) (Observation, error) {
				calls.Add(1)
				panic("body-secret panic")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, resolved := newManualHubTestManager(t, "manual-failure-"+tt.name)
			var calls atomic.Int32
			completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(tt.observe(&calls)))
			completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: []byte("raw-body-secret")})
			completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: []byte("second-body")})
			if got := calls.Load(); got != 1 {
				t.Fatalf("adapter calls = %d, want 1", got)
			}
			if _, ok := manager.QuotaScore(resolved.ID); ok {
				t.Fatal("failed completion changed quota score")
			}
		})
	}
}

func TestBeginManualQueryFailureLogsAreStructuredAndSanitized(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousOutput := logger.Out
	previousLevel := logger.GetLevel()
	logger.SetOutput(io.Discard)
	logger.SetLevel(log.WarnLevel)
	hook := logtest.NewLocal(logger)
	defer func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetOutput(previousOutput)
		logger.SetLevel(previousLevel)
	}()

	tests := []struct {
		name       string
		errorClass string
		match      manualQueryMatchFunc
		observe    manualQueryObserveFunc
	}{
		{
			name:       "adapter error",
			errorClass: manualQueryErrorAdapter,
			match:      func(manualQueryMetadata) bool { return true },
			observe: func(manualResponseMetadata, io.Reader) (Observation, error) {
				return Observation{}, errors.New("adapter-error-secret")
			},
		},
		{
			name:       "validation error",
			errorClass: manualQueryErrorValidation,
			match:      func(manualQueryMetadata) bool { return true },
			observe: func(manualResponseMetadata, io.Reader) (Observation, error) {
				return Observation{}, nil
			},
		},
		{
			name:       "adapter panic",
			errorClass: manualQueryErrorAdapterPanic,
			match:      func(manualQueryMetadata) bool { return true },
			observe: func(manualResponseMetadata, io.Reader) (Observation, error) {
				panic("adapter-panic-secret")
			},
		},
		{
			name:       "match panic",
			errorClass: manualQueryErrorMatchPanic,
			match: func(manualQueryMetadata) bool {
				panic("match-panic-secret")
			},
			observe: scoreOnlyManualObservation(1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook.Reset()
			manager, resolved := newManualHubTestManager(t, "manual-log-"+tt.name)
			queryURL, err := url.Parse("https://Quota.Example.Test/quota?query-secret=1#fragment-secret")
			if err != nil {
				t.Fatal(err)
			}
			table := newManualAdapterTable(manualQueryAdapter{
				provider: resolved.Provider,
				match:    tt.match,
				observe:  tt.observe,
			})
			completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", queryURL, table)
			if tt.errorClass == manualQueryErrorMatchPanic {
				if completion != nil {
					t.Fatal("match panic returned a completion")
				}
				assertNextBoundTicketOrder(t, manager, resolved, 1)
			} else {
				if completion == nil {
					t.Fatal("matched query returned nil completion")
				}
				completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: []byte("body-secret")})
			}

			entries := hook.AllEntries()
			if len(entries) != 1 {
				t.Fatalf("log entries = %d, want 1", len(entries))
			}
			entry := entries[0]
			if entry.Level != log.WarnLevel || entry.Message != "quota hub: manual query synchronization failed" ||
				entry.Data["provider"] != "test-provider" || entry.Data["endpoint"] != "https://quota.example.test/quota" ||
				entry.Data["error_class"] != tt.errorClass {
				t.Fatalf("sanitized log = level %s message %q data %+v", entry.Level, entry.Message, entry.Data)
			}
			serialized := fmt.Sprint(entry.Message, entry.Data)
			for _, secret := range []string{
				"adapter-error-secret", "adapter-panic-secret", "match-panic-secret",
				"body-secret", "query-secret", "fragment-secret",
			} {
				if strings.Contains(serialized, secret) {
					t.Fatalf("sanitized log leaked %q: %s", secret, serialized)
				}
			}
		})
	}
}

func TestBeginManualQueryMatchPanicReturnsNilWithoutTicket(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-match-panic")
	table := newManualAdapterTable(manualQueryAdapter{
		provider: resolved.Provider,
		match: func(manualQueryMetadata) bool {
			panic("match failure")
		},
		observe: scoreOnlyManualObservation(1),
	})
	if completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), table); completion != nil {
		t.Fatal("panicking match returned a completion")
	}
	assertNextBoundTicketOrder(t, manager, resolved, 1)
}

func TestBeginManualQueryStaleCompletionCannotOverrideRuntime429(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-stale-runtime")
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(scoreOnlyManualObservation(88)))
	manager.MarkResult(context.Background(), auth.Result{
		AuthID:   resolved.ID,
		Provider: resolved.Provider,
		Model:    "runtime-model",
		Error:    &auth.Error{HTTPStatus: 429, Message: "runtime quota"},
	})
	completion(context.Background(), ManualQueryResponse{StatusCode: 200, Body: []byte("healthy")})
	if _, ok := manager.QuotaScore(resolved.ID); ok {
		t.Fatal("stale manual completion changed quota score after runtime 429")
	}
	current, ok := manager.GetByID(resolved.ID)
	if !ok || !current.Unavailable {
		t.Fatal("stale manual completion cleared runtime 429 state")
	}
}

func TestBeginManualQueryCanceledCompletionStillPersistsWithContextValues(t *testing.T) {
	manager, resolved := newManualHubTestManager(t, "manual-canceled-save")
	store := &manualRecordingCooldownStore{}
	manager.SetCooldownStateStore(store)
	observe := func(metadata manualResponseMetadata, _ io.Reader) (Observation, error) {
		return Observation{
			Completeness: ExhaustionEvidence,
			Mutations: []Mutation{{
				Scope:   ScopeAuth,
				Outcome: Exhausted,
				ResetAt: metadata.CompletedAt.Add(time.Hour),
			}},
		}, nil
	}
	completion := beginManualQueryWithTable(context.Background(), manager, resolved, "GET", mustManualTestURL(t), matchedManualTestTable(observe))
	if completion == nil {
		t.Fatal("matched query returned nil completion")
	}
	type contextKey struct{}
	base := context.WithValue(context.Background(), contextKey{}, "preserved-value")
	canceled, cancel := context.WithTimeout(base, time.Hour)
	cancel()
	completion(canceled, ManualQueryResponse{StatusCode: 200, Body: []byte("exhausted")})

	saveCount, savedErr, savedValue, hadDeadline, records := store.snapshot(contextKey{})
	if saveCount != 1 || savedErr != nil || savedValue != "preserved-value" || hadDeadline {
		t.Fatalf("Save() = count %d err %v value %v deadline %v", saveCount, savedErr, savedValue, hadDeadline)
	}
	if len(records) != 1 || records[0].AuthID != resolved.ID || !records[0].Quota.Exceeded {
		t.Fatalf("saved records = %+v", records)
	}
}

type manualRecordingCooldownStore struct {
	mu          sync.Mutex
	saveCount   int
	contextErr  error
	contextData context.Context
	hadDeadline bool
	records     []auth.CooldownStateRecord
}

func (store *manualRecordingCooldownStore) Load(context.Context) ([]auth.CooldownStateRecord, error) {
	return nil, nil
}

func (store *manualRecordingCooldownStore) Save(ctx context.Context, records []auth.CooldownStateRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saveCount++
	store.contextErr = ctx.Err()
	store.contextData = ctx
	_, store.hadDeadline = ctx.Deadline()
	store.records = append([]auth.CooldownStateRecord(nil), records...)
	return nil
}

func (store *manualRecordingCooldownStore) snapshot(key any) (int, error, any, bool, []auth.CooldownStateRecord) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saveCount, store.contextErr, store.contextData.Value(key), store.hadDeadline,
		append([]auth.CooldownStateRecord(nil), store.records...)
}

func matchedManualTestTable(observe manualQueryObserveFunc) manualAdapterTable {
	return newManualAdapterTable(manualHubTestAdapter(true, observe))
}

func manualHubTestAdapter(matched bool, observe manualQueryObserveFunc) manualQueryAdapter {
	if observe == nil {
		observe = scoreOnlyManualObservation(10)
	}
	return manualQueryAdapter{
		provider: "test-provider",
		match: func(query manualQueryMetadata) bool {
			return matched && query.Provider == "test-provider" && query.Method == "GET" &&
				query.Scheme == "https" && query.Host == "quota.example.test" && query.Hostname == "quota.example.test" &&
				query.Port == "" && query.Path == "/quota" && query.EscapedPath == "/quota" && query.RawQuery == ""
		},
		observe: observe,
	}
}

func scoreOnlyManualObservation(score float64) manualQueryObserveFunc {
	return func(manualResponseMetadata, io.Reader) (Observation, error) {
		return scoreOnlyObservation(score), nil
	}
}

func scoreOnlyObservation(score float64) Observation {
	return Observation{Score: &score, Completeness: ScoreOnly}
}

func newManualHubTestManager(t *testing.T, authID string) (*auth.Manager, *auth.Auth) {
	t.Helper()
	manager := auth.NewManager(nil, nil, nil)
	resolved := registerManualTestAuth(t, manager, &auth.Auth{ID: authID, Provider: "test-provider", Status: auth.StatusActive})
	return manager, resolved
}

func registerManualTestAuth(t *testing.T, manager *auth.Manager, authRecord *auth.Auth) *auth.Auth {
	t.Helper()
	resolved, err := manager.Register(context.Background(), authRecord)
	if err != nil || resolved == nil {
		t.Fatalf("Register() = %v, %v", resolved, err)
	}
	return resolved
}

func assertNextBoundTicketOrder(t *testing.T, manager *auth.Manager, resolved *auth.Auth, want uint64) {
	t.Helper()
	ticket, ok := manager.IssueQuotaObservationTicketForAuth(resolved)
	if !ok || ticket.StartOrder != want {
		t.Fatalf("bound ticket = %+v, %v; want order %d", ticket, ok, want)
	}
}

func mustManualTestURL(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := url.Parse("https://quota.example.test/quota")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	return parsed
}
