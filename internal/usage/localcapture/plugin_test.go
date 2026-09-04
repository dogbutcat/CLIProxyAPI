package localcapture

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	usagebridge "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type mockRecorder struct {
	mu     sync.Mutex
	events []usagebridge.Event
}

func (m *mockRecorder) RecordEvent(event usagebridge.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *mockRecorder) last() usagebridge.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return usagebridge.Event{}
	}
	return m.events[len(m.events)-1]
}

func TestHandleUsageBasicMapping(t *testing.T) {
	recorder := &mockRecorder{}
	plugin := New(recorder)
	requestedAt := time.Unix(1700000000, 123000000)
	ctx := internallogging.WithEndpoint(internallogging.WithRequestID(context.Background(), "req-1"), "POST /v1/chat/completions")

	plugin.HandleUsage(ctx, coreusage.Record{
		Provider:     "gemini",
		ExecutorType: "gemini-direct",
		Model:        "gemini-2.5-flash",
		Alias:        "flash",
		APIKey:       "sk-secret-value",
		AuthID:       "auth-1",
		AuthIndex:    "0",
		AuthType:     "api-key",
		Source:       "user@example.com",
		RequestedAt:  requestedAt,
		Latency:      1500 * time.Millisecond,
		TTFT:         200 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})

	event := recorder.last()
	if event.RequestID != "req-1" || event.Provider != "gemini" || event.ExecutorType != "gemini-direct" {
		t.Fatalf("basic metadata = %#v", event)
	}
	if event.Model != "flash" || event.RequestedModel != "flash" || event.ResolvedModel != "gemini-2.5-flash" {
		t.Fatalf("model mapping = model %q requested %q resolved %q", event.Model, event.RequestedModel, event.ResolvedModel)
	}
	if event.Method != "POST" || event.Path != "/v1/chat/completions" {
		t.Fatalf("endpoint mapping = %q %q", event.Method, event.Path)
	}
	if event.Source == "user@example.com" || event.APIKeyHash == "" || event.APIKeyHash == "sk-secret-value" {
		t.Fatalf("secret fields were not masked/hashed: source=%q api_key_hash=%q", event.Source, event.APIKeyHash)
	}
	if event.LatencyMS == nil || *event.LatencyMS != 1500 || event.TTFTMS == nil || *event.TTFTMS != 200 {
		t.Fatalf("latency = %v ttft = %v", event.LatencyMS, event.TTFTMS)
	}
	if event.TotalTokens != 150 || event.EventHash == "" || event.TimestampMS != requestedAt.UnixMilli() {
		t.Fatalf("usage fields = %#v", event)
	}
}

func TestHandleUsageFailedRecordSanitizesSecrets(t *testing.T) {
	recorder := &mockRecorder{}
	plugin := New(recorder)
	ctx := internallogging.WithResponseStatusHolder(context.Background())
	internallogging.SetResponseStatus(ctx, http.StatusTooManyRequests)

	plugin.HandleUsage(ctx, coreusage.Record{
		Provider: "openai",
		Model:    "gpt-5",
		Failed:   true,
		Fail: coreusage.Failure{
			Body: "authorization: Bearer sk-secret-token user@example.com",
		},
	})

	event := recorder.last()
	if !event.Failed || event.FailStatusCode != http.StatusTooManyRequests {
		t.Fatalf("failure mapping = failed %v status %d", event.Failed, event.FailStatusCode)
	}
	if event.FailSummary == "" || strings.Contains(event.FailSummary, "sk-secret-token") || strings.Contains(event.FailBody, "sk-secret-token") {
		t.Fatalf("failure body was not sanitized: summary=%q body=%q", event.FailSummary, event.FailBody)
	}
}

func TestHandleUsageZeroTokenRecordIsCaptured(t *testing.T) {
	recorder := &mockRecorder{}
	New(recorder).HandleUsage(context.Background(), coreusage.Record{Provider: "codex", Model: "gpt-5"})
	event := recorder.last()
	if event.Model != "gpt-5" || event.TotalTokens != 0 || event.InputTokens != 0 || event.OutputTokens != 0 {
		t.Fatalf("zero-token event = %#v", event)
	}
}

func TestHandleUsageResponseHeadersSanitized(t *testing.T) {
	recorder := &mockRecorder{}
	New(recorder).HandleUsage(context.Background(), coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5",
		RequestedAt: time.Unix(1700000000, 0),
		ResponseHeaders: http.Header{
			"X-Oai-Request-Id":             {"trace-1"},
			"X-Codex-Plan-Type":            {"plus"},
			"X-Codex-Primary-Used-Percent": {"42.5"},
			"Authorization":                {"Bearer should-not-be-captured"},
			"Set-Cookie":                   {"session=should-not-be-captured"},
			"Content-Type":                 {"application/json"},
		},
	})
	event := recorder.last()
	if event.HeaderTraceID != "trace-1" || event.ResponseHeaders.Get("X-Codex-Plan-Type") != "plus" {
		t.Fatalf("header metadata = %#v", event)
	}
	if event.ResponseHeaders.Get("Authorization") != "" || event.ResponseHeaders.Get("Set-Cookie") != "" ||
		strings.Contains(event.ResponseMetadataJSON, "should-not-be-captured") {
		t.Fatalf("unsafe response metadata: headers=%#v json=%q", event.ResponseHeaders, event.ResponseMetadataJSON)
	}
}

func TestHandleUsageModelAliasFromContext(t *testing.T) {
	recorder := &mockRecorder{}
	ctx := coreusage.WithRequestedModelAlias(context.Background(), "client-alias")
	New(recorder).HandleUsage(ctx, coreusage.Record{Provider: "openai", Model: "upstream-model"})
	event := recorder.last()
	if event.Model != "client-alias" || event.RequestedModel != "client-alias" || event.ResolvedModel != "upstream-model" {
		t.Fatalf("alias mapping = %#v", event)
	}
}
