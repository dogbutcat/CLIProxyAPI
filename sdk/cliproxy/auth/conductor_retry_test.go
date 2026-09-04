package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type lexicographicSelector struct{}

func (lexicographicSelector) Pick(_ context.Context, _ string, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	sort.Slice(auths, func(i, j int) bool { return auths[i].ID < auths[j].ID })
	return auths[0], nil
}

type transientRetryTestExecutor struct {
	id string

	mu                   sync.Mutex
	executeCalls         []string
	countCalls           []string
	streamCalls          []string
	executeFailures      int
	countFailures        int
	streamBootstrapFails int
	streamPostBootstrap  bool
	failAuthID           string
	transientStatus      int
}

func (e *transientRetryTestExecutor) Identifier() string { return e.id }

func (e *transientRetryTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	if auth.ID == e.failAuthID || len(e.executeCalls) <= e.executeFailures {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: e.status(), Message: "transient"}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *transientRetryTestExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.countCalls = append(e.countCalls, auth.ID)
	if len(e.countCalls) <= e.countFailures {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: e.status(), Message: "transient"}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *transientRetryTestExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	call := len(e.streamCalls)
	bootstrapFails := e.streamBootstrapFails
	postBootstrap := e.streamPostBootstrap
	status := e.status()
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	if call <= bootstrapFails {
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: status, Message: "transient"}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Retry-Test": []string{auth.ID}}, Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	if postBootstrap {
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: status, Message: "transient"}}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Retry-Test": []string{auth.ID}}, Chunks: ch}, nil
}

func (e *transientRetryTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *transientRetryTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *transientRetryTestExecutor) status() int {
	if e.transientStatus != 0 {
		return e.transientStatus
	}
	return http.StatusBadGateway
}

func (e *transientRetryTestExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executeCalls...)
}

func (e *transientRetryTestExecutor) CountCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.countCalls...)
}

func (e *transientRetryTestExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.streamCalls...)
}

func withTransientRetryTestBackoff(t *testing.T) *[]time.Duration {
	t.Helper()
	oldSleep := sleepTransientRetryBackoff
	waits := []time.Duration{}
	sleepTransientRetryBackoff = func(ctx context.Context, wait time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		waits = append(waits, wait)
		return nil
	}
	t.Cleanup(func() { sleepTransientRetryBackoff = oldSleep })
	return &waits
}

func newTransientRetryTestManager(t *testing.T, executor *transientRetryTestExecutor, hook Hook) *Manager {
	t.Helper()
	m := NewManager(nil, lexicographicSelector{}, hook)
	m.RegisterExecutor(executor)
	model := "retry-model"
	for _, authID := range []string{"aa-auth", "bb-auth"} {
		registry.GetGlobalRegistry().RegisterClient(authID, executor.id, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func(authID string) func() {
			return func() { registry.GetGlobalRegistry().UnregisterClient(authID) }
		}(authID))
		if _, errRegister := m.Register(context.Background(), &Auth{ID: authID, Provider: executor.id}); errRegister != nil {
			t.Fatalf("register %s: %v", authID, errRegister)
		}
	}
	return m
}

func TestTransientStatusClassification(t *testing.T) {
	transient := []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	for _, status := range transient {
		if !isTransientStatus(status) {
			t.Fatalf("isTransientStatus(%d) = false, want true", status)
		}
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden, http.StatusUnauthorized, http.StatusBadRequest, http.StatusOK} {
		if isTransientStatus(status) {
			t.Fatalf("isTransientStatus(%d) = true, want false", status)
		}
	}
}

func TestRetrySameAuthCancellationInterruptsBackoff(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetTransientRetryCount(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	shouldRetry, errRetry := m.retrySameAuth(ctx, &Auth{ID: "auth"}, &Error{HTTPStatus: http.StatusBadGateway, Message: "transient"}, 0)
	if !errors.Is(errRetry, context.Canceled) {
		t.Fatalf("retrySameAuth error = %v, want context.Canceled", errRetry)
	}
	if shouldRetry {
		t.Fatal("retrySameAuth shouldRetry = true after canceled backoff")
	}
}

func TestExecuteRetriesSameAuthBeforeCooldownAndSwitch(t *testing.T) {
	waits := withTransientRetryTestBackoff(t)
	hook := &resultCaptureHook{}
	executor := &transientRetryTestExecutor{id: "claude", executeFailures: 2}
	m := newTransientRetryTestManager(t, executor, hook)
	m.SetTransientRetryCount(2)

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "retry-model"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v", errExecute)
	}
	if string(resp.Payload) != "aa-auth" {
		t.Fatalf("payload = %q, want aa-auth", string(resp.Payload))
	}
	if got, want := executor.ExecuteCalls(), []string{"aa-auth", "aa-auth", "aa-auth"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	if got, want := *waits, []time.Duration{time.Second, 2 * time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry waits = %v, want %v", got, want)
	}
	results := hook.Results()
	if len(results) != 1 || !results[0].Success || results[0].AuthID != "aa-auth" {
		t.Fatalf("recorded results = %+v, want one aa-auth success", results)
	}
}

func TestExecuteTransientRetryCountZeroSwitchesAuthImmediately(t *testing.T) {
	withTransientRetryTestBackoff(t)
	executor := &transientRetryTestExecutor{id: "claude", failAuthID: "aa-auth"}
	m := newTransientRetryTestManager(t, executor, nil)
	m.SetTransientRetryCount(0)

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "retry-model"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v", errExecute)
	}
	if string(resp.Payload) != "bb-auth" {
		t.Fatalf("payload = %q, want bb-auth", string(resp.Payload))
	}
	if got, want := executor.ExecuteCalls(), []string{"aa-auth", "bb-auth"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
}

func TestExecuteCountRetriesSameAuth(t *testing.T) {
	withTransientRetryTestBackoff(t)
	executor := &transientRetryTestExecutor{id: "claude", countFailures: 1}
	m := newTransientRetryTestManager(t, executor, nil)
	m.SetTransientRetryCount(1)

	resp, errExecute := m.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "retry-model"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute count error = %v", errExecute)
	}
	if string(resp.Payload) != "aa-auth" {
		t.Fatalf("payload = %q, want aa-auth", string(resp.Payload))
	}
	if got, want := executor.CountCalls(), []string{"aa-auth", "aa-auth"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("count calls = %v, want %v", got, want)
	}
}

func TestExecuteStreamRetriesBootstrapOnlyBeforeFirstPayload(t *testing.T) {
	withTransientRetryTestBackoff(t)
	hook := &resultCaptureHook{}
	executor := &transientRetryTestExecutor{id: "claude", streamBootstrapFails: 1}
	m := newTransientRetryTestManager(t, executor, hook)
	m.SetTransientRetryCount(1)

	streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "retry-model"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v", errExecute)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "aa-auth" {
		t.Fatalf("payload = %q, want aa-auth", string(payload))
	}
	if got, want := executor.StreamCalls(), []string{"aa-auth", "aa-auth"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
	results := hook.Results()
	if len(results) != 1 || !results[0].Success || results[0].AuthID != "aa-auth" {
		t.Fatalf("recorded results = %+v, want one aa-auth success", results)
	}
}

func TestExecuteStreamDoesNotRetryAfterFirstPayload(t *testing.T) {
	withTransientRetryTestBackoff(t)
	executor := &transientRetryTestExecutor{id: "claude", streamPostBootstrap: true}
	m := newTransientRetryTestManager(t, executor, nil)
	m.SetTransientRetryCount(3)

	streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "retry-model"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v", errExecute)
	}
	var gotErr error
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
	}
	if gotErr == nil {
		t.Fatal("stream error = nil, want post-bootstrap error")
	}
	if got, want := executor.StreamCalls(), []string{"aa-auth"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
}
