package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCandidateExhausted429StopsOuterRetryAndReturnsUpstream(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 100*time.Millisecond, 0)

	model := "claude-fable-5-terminal-single"
	auth := &Auth{
		ID:       "terminal-429-single-auth",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	registerTestAuthModel(t, manager, auth, model)

	retryAfter := 5 * time.Millisecond
	execCalls := 0
	executor := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCalls++
			return cliproxyexecutor.Response{}, customStatusError{
				code:       http.StatusTooManyRequests,
				msg:        `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for this model.","details":{"error_code":"credits_required","disabled_reason":"org_level_disabled"}}}`,
				retryAfter: &retryAfter,
			}
		},
	}
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if got := statusCodeFromError(errExecute); got != http.StatusTooManyRequests {
		t.Fatalf("Execute() status = %d, want 429; err=%v", got, errExecute)
	}
	if !strings.Contains(errExecute.Error(), "credits_required") {
		t.Fatalf("Execute() error = %q, want upstream body", errExecute.Error())
	}
	if execCalls != 1 {
		t.Fatalf("executor calls = %d, want 1", execCalls)
	}
}

func TestCandidateExhaustedAll429StopsAfterOneCredentialSweep(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 100*time.Millisecond, 0)

	model := "claude-fable-5-terminal-pool"
	authIDs := []string{"terminal-429-auth-a", "terminal-429-auth-b", "terminal-429-auth-c"}
	for _, authID := range authIDs {
		registerTestAuthModel(t, manager, &Auth{ID: authID, Provider: "claude"}, model)
	}

	retryAfter := 5 * time.Millisecond
	executor := &authFallbackExecutor{
		id:            "claude",
		executeErrors: make(map[string]error, len(authIDs)),
	}
	for _, authID := range authIDs {
		executor.executeErrors[authID] = &retryAfterStatusError{
			status:     http.StatusTooManyRequests,
			message:    `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for this model.","details":{"error_code":"credits_required","auth":"` + authID + `"}}}`,
			retryAfter: retryAfter,
		}
	}
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if got := statusCodeFromError(errExecute); got != http.StatusTooManyRequests {
		t.Fatalf("Execute() status = %d, want 429; err=%v", got, errExecute)
	}
	if calls := executor.ExecuteCalls(); len(calls) != len(authIDs) {
		t.Fatalf("executor calls = %v, want one sweep across %d auths", calls, len(authIDs))
	}
}

func TestCandidateExhaustedCountTokens429StopsOuterRetry(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 100*time.Millisecond, 0)

	model := "claude-fable-5-terminal-count"
	auth := &Auth{ID: "terminal-429-count-auth", Provider: "claude"}
	registerTestAuthModel(t, manager, auth, model)

	retryAfter := 5 * time.Millisecond
	countCalls := 0
	executor := &mockCustomErrorExecutor{
		identifier: "claude",
		countFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			countCalls++
			return cliproxyexecutor.Response{}, customStatusError{
				code:       http.StatusTooManyRequests,
				msg:        `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for this model."}}`,
				retryAfter: &retryAfter,
			}
		},
	}
	manager.RegisterExecutor(executor)

	_, errCount := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errCount == nil {
		t.Fatal("ExecuteCount() error = nil, want upstream 429")
	}
	if got := statusCodeFromError(errCount); got != http.StatusTooManyRequests {
		t.Fatalf("ExecuteCount() status = %d, want 429; err=%v", got, errCount)
	}
	if countCalls != 1 {
		t.Fatalf("count calls = %d, want 1", countCalls)
	}
}

func TestCandidateExhaustedStream429ReturnsBootstrapErrorWithoutRepeat(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 100*time.Millisecond, 0)

	model := "claude-fable-5-terminal-stream"
	authIDs := []string{"terminal-stream-auth-a", "terminal-stream-auth-b"}
	for _, authID := range authIDs {
		registerTestAuthModel(t, manager, &Auth{ID: authID, Provider: "claude"}, model)
	}

	executor := &authFallbackExecutor{
		id:                "claude",
		streamFirstErrors: make(map[string]error, len(authIDs)),
	}
	for _, authID := range authIDs {
		executor.streamFirstErrors[authID] = &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for this model.","details":{"error_code":"credits_required","auth":"` + authID + `"}}}`,
		}
	}
	manager.RegisterExecutor(executor)

	result, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() returned immediate error = %v, want stream bootstrap result", errStream)
	}
	if result == nil || result.Chunks == nil {
		t.Fatal("ExecuteStream() result is nil")
	}
	var chunkErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
		}
	}
	if chunkErr == nil {
		t.Fatal("stream chunk error = nil, want upstream 429")
	}
	if got := statusCodeFromError(chunkErr); got != http.StatusTooManyRequests {
		t.Fatalf("stream chunk status = %d, want 429; err=%v", got, chunkErr)
	}
	if calls := executor.StreamCalls(); len(calls) != len(authIDs) {
		t.Fatalf("stream calls = %v, want one sweep across %d auths", calls, len(authIDs))
	}
}

func registerTestAuthModel(t *testing.T, manager *Manager, auth *Auth, model string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(%s): %v", auth.ID, errRegister)
	}
}
