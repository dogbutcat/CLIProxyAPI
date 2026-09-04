package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

func TestDashboardSummaryEmptyDBStatusAndShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	bridge := newTestUsageBridge(t, dbPath)
	router := gin.New()
	router.GET("/summary", NewHandlers(bridge).DashboardSummary)

	nowMS := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	todayMS := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	body := performDashboardRequest(t, router, "/summary?today_start_ms=%d&now_ms=%d", todayMS, nowMS)

	status := body["status"].(map[string]any)
	if status["state"] != "empty" || status["has_data"] != false {
		t.Fatalf("status = %#v, want empty has_data=false", status)
	}
	today := body["today"].(map[string]any)
	if today["total_calls"] != float64(0) {
		t.Fatalf("today.total_calls = %#v, want 0", today["total_calls"])
	}
	if got := len(body["traffic_timeline"].([]any)); got != 24 {
		t.Fatalf("traffic_timeline length = %d, want 24", got)
	}
	health := body["today_request_health_timeline"].(map[string]any)
	if got := len(health["points"].([]any)); got != 144 {
		t.Fatalf("health points length = %d, want 144", got)
	}
}

func TestDashboardSummaryNormalDataOpenCodeIdentityAndFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	nowMS := time.Date(2026, 8, 2, 12, 30, 0, 0, time.UTC).UnixMilli()
	insertDashboardEvents(t, dbPath, []plusstore.Event{
		{
			RequestID:            "ok-1",
			TimestampMS:          nowMS - 20*60*1000,
			Provider:             "opencode-go",
			Model:                "claude-sonnet-4",
			ResolvedModel:        "claude-sonnet-4",
			Endpoint:             "/v1/messages",
			AuthIndex:            "auth-opencode",
			Source:               "opencode",
			SourceHash:           "source-hash",
			AccountSnapshot:      "opencode-go:anthropic:bad-token",
			AuthLabelSnapshot:    "opencode-go:anthropic:bad-token",
			AuthProviderSnapshot: "opencode-go:anthropic:bad-token",
			InputTokens:          10,
			OutputTokens:         20,
			TotalTokens:          30,
			LatencyMS:            int64Ptr(1000),
		},
		{
			RequestID:            "fail-1",
			TimestampMS:          nowMS - 10*60*1000,
			Provider:             "opencode-go",
			Model:                "claude-sonnet-4",
			ResolvedModel:        "claude-sonnet-4",
			Endpoint:             "/v1/messages",
			AuthIndex:            "auth-opencode",
			Source:               "opencode",
			SourceHash:           "source-hash",
			AccountSnapshot:      "opencode-go:anthropic:bad-token",
			AuthLabelSnapshot:    "opencode-go:anthropic:bad-token",
			AuthProviderSnapshot: "opencode-go:anthropic:bad-token",
			InputTokens:          4,
			OutputTokens:         0,
			TotalTokens:          4,
			Failed:               true,
			FailStatusCode:       http.StatusTooManyRequests,
			FailSummary:          "quota exceeded",
			HeaderErrorKind:      "quota",
			HeaderErrorCode:      "rate_limit",
			HeaderTraceID:        "trace-1",
			LatencyMS:            int64Ptr(2000),
		},
	})
	bridge := newTestUsageBridge(t, dbPath)
	router := gin.New()
	router.GET("/summary", NewHandlers(bridge, WithMonitoringAuthMetadataProvider(func(_ context.Context) map[string]MonitoringAuthMetadata {
		return map[string]MonitoringAuthMetadata{
			"auth-opencode": {
				AccountName:  "alice",
				AuthLabel:    "alice",
				Provider:     "claude",
				AuthProvider: "opencode-go",
				Protocol:     "anthropic",
				ProjectID:    "workspace-a",
			},
		}
	})).DashboardSummary)

	todayMS := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	body := performDashboardRequest(t, router, "/summary?today_start_ms=%d&now_ms=%d&top_models=1&recent_failures=1", todayMS, nowMS)
	status := body["status"].(map[string]any)
	if status["state"] != "ok" || status["has_data"] != true {
		t.Fatalf("status = %#v, want ok has_data=true", status)
	}
	today := body["today"].(map[string]any)
	if today["total_calls"] != float64(2) || today["failure_calls"] != float64(1) {
		t.Fatalf("today = %#v, want 2 calls and 1 failure", today)
	}
	topModels := body["top_models_today"].([]any)
	if len(topModels) != 1 || topModels[0].(map[string]any)["calls"] != float64(2) {
		t.Fatalf("top_models_today = %#v, want one 2-call model", topModels)
	}
	providers := body["provider_activity"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["provider"] != "opencode-go" {
		t.Fatalf("provider_activity = %#v, want opencode-go", providers)
	}
	if providers[0].(map[string]any)["success_calls"] != float64(1) || providers[0].(map[string]any)["failure_calls"] != float64(1) {
		t.Fatalf("provider_activity calls = %#v, want 1 success and 1 failure", providers[0])
	}
	channels := body["channel_health"].([]any)
	if len(channels) != 1 {
		t.Fatalf("channel_health length = %d, want 1", len(channels))
	}
	channel := channels[0].(map[string]any)
	if channel["account_snapshot"] != "alice" || channel["auth_label_snapshot"] != "alice" || channel["auth_provider_snapshot"] != "claude" {
		t.Fatalf("channel identity = %#v, want alice/claude", channel)
	}
	failures := body["recent_failures"].([]any)
	if len(failures) != 1 || failures[0].(map[string]any)["fail_status_code"] != float64(429) {
		t.Fatalf("recent_failures = %#v, want one 429 failure", failures)
	}
}

func TestDashboardSummaryRejectsOutOfRangeLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	bridge := newTestUsageBridge(t, dbPath)
	router := gin.New()
	router.GET("/summary", NewHandlers(bridge).DashboardSummary)
	req := httptest.NewRequest(http.MethodGet, "/summary?today_start_ms=1&now_ms=2&recent_failures=101", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestStoreStatusEnvelopeEmptyDBAndCollector(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	bridge := newTestUsageBridge(t, dbPath)
	router := gin.New()
	router.GET("/status", NewHandlers(bridge).Status)
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	status := body["status"].(map[string]any)
	if status["state"] != "empty" || status["has_data"] != false {
		t.Fatalf("status = %#v, want empty has_data=false", status)
	}
	store := body["store"].(map[string]any)
	if store["configured"] != true || store["events"] != float64(0) {
		t.Fatalf("store = %#v, want configured empty store", store)
	}
	collector := body["collector"].(map[string]any)
	if collector["mode"] != "local-sqlite" {
		t.Fatalf("collector = %#v, want local-sqlite", collector)
	}
}

func newTestUsageBridge(t *testing.T, dbPath string) *Bridge {
	t.Helper()
	bridge, err := NewBridge(BridgeConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	return bridge
}

func insertDashboardEvents(t *testing.T, dbPath string, events []plusstore.Event) {
	t.Helper()
	store, err := plusstore.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if _, err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
}

func performDashboardRequest(t *testing.T, router http.Handler, format string, args ...any) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf(format, args...), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func int64Ptr(value int64) *int64 {
	return &value
}
