package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

func TestMonitoringAccountsFullIncludeOpenCodeIdentityAndRedaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	nowMS := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC).UnixMilli()
	rawKey := "sk-ant-secretvalue123456"
	insertDashboardEvents(t, dbPath, []plusstore.Event{{
		RequestID:             "opencode-1",
		TimestampMS:           nowMS,
		Provider:              "opencode-go",
		ExecutorType:          "opencode-go",
		Model:                 "claude-sonnet-4",
		ResolvedModel:         "claude-sonnet-4",
		Endpoint:              "/v1/messages",
		AuthIndex:             "auth-opencode",
		Source:                rawKey,
		SourceHash:            plusstore.HashString(rawKey),
		APIKeyHash:            plusstore.HashString(rawKey),
		AccountSnapshot:       "opencode-go:anthropic:bad-token",
		AuthLabelSnapshot:     "opencode-go:anthropic:bad-token",
		AuthProviderSnapshot:  "opencode-go:anthropic:bad-token",
		AuthProjectIDSnapshot: "opencode-go:anthropic:bad-token",
		InputTokens:           9,
		OutputTokens:          11,
		TotalTokens:           20,
		ResponseMetadata: plusstore.ParseResponseHeaderMetadata(map[string]any{
			"authorization":     "Bearer should-not-leak",
			"set-cookie":        "session=should-not-leak",
			"x-oai-request-id":  "trace-opencode",
			"x-codex-plan-type": "plus",
			"content-type":      "application/json",
		}, time.UnixMilli(nowMS)),
	}})
	bridge := newTestUsageBridge(t, dbPath)
	router := gin.New()
	router.GET("/accounts", NewHandlers(bridge, WithMonitoringAuthMetadataProvider(func(_ context.Context) map[string]MonitoringAuthMetadata {
		return map[string]MonitoringAuthMetadata{
			"auth-opencode": {
				AccountName:   "alice-workspace",
				AuthLabel:     "opencode-key",
				AuthFile:      "entry-a.json",
				Provider:      "claude",
				AuthProvider:  "opencode-go",
				Protocol:      "anthropic",
				GeneratedName: "entry-a",
				ProjectID:     "workspace-a",
			},
		}
	})).MonitoringAccounts)

	body, raw := performMonitoringRequest(t, router, "/accounts?include=full&limit=1")
	if strings.Contains(raw, rawKey) || strings.Contains(raw, "should-not-leak") {
		t.Fatalf("monitoring response leaked secret material: %s", raw)
	}
	summary := body["summary"].(map[string]any)
	if summary["total_calls"] != float64(1) {
		t.Fatalf("summary = %#v, want one call", summary)
	}
	accounts := body["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts len = %d, want 1", len(accounts))
	}
	account := accounts[0].(map[string]any)
	if account["workspace_id"] != "workspace-a" || account["entry"] != "entry-a.json" || account["protocol"] != "claude" {
		t.Fatalf("account identity = %#v, want workspace/entry/protocol", account)
	}
	page := body["events_page"].(map[string]any)
	events := page["events"].([]any)
	event := events[0].(map[string]any)
	if event["workspace_id"] != "workspace-a" || event["entry"] != "entry-a.json" || event["key_alias"] != "opencode-key" {
		t.Fatalf("event identity = %#v, want enriched OpenCode identity", event)
	}
	if event["source"] == rawKey {
		t.Fatalf("event source = %q, want masked source", event["source"])
	}
}

func TestMonitoringRealtimeKeysetPaginationHandlerNoDuplicateOrGap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	baseMS := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC).UnixMilli()
	events := []plusstore.Event{}
	for i := 0; i < 3; i++ {
		events = append(events, plusstore.Event{
			RequestID:    fmt.Sprintf("rt-%d", i),
			TimestampMS:  baseMS,
			Provider:     "claude",
			Model:        "claude-sonnet-4",
			Endpoint:     "/v1/messages",
			AuthIndex:    "account-a",
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		})
	}
	insertDashboardEvents(t, dbPath, events)
	bridge := newTestUsageBridge(t, dbPath)
	router := gin.New()
	router.GET("/realtime", NewHandlers(bridge).MonitoringRealtime)

	first, _ := performMonitoringRequest(t, router, "/realtime?limit=2")
	firstPage := first["events_page"].(map[string]any)
	firstEvents := firstPage["events"].([]any)
	if len(firstEvents) != 2 || firstPage["next_cursor"] == nil {
		t.Fatalf("first page = %#v, want two events and cursor", firstPage)
	}
	cursor := firstPage["next_cursor"].(string)
	second, _ := performMonitoringRequest(t, router, "/realtime?limit=2&cursor="+cursor)
	secondEvents := second["events_page"].(map[string]any)["events"].([]any)
	if len(secondEvents) != 1 {
		t.Fatalf("second events len = %d, want 1", len(secondEvents))
	}
	seen := map[string]bool{}
	for _, rawEvent := range append(firstEvents, secondEvents...) {
		event := rawEvent.(map[string]any)
		hash := event["event_hash"].(string)
		if seen[hash] {
			t.Fatalf("duplicate event hash %s across pages", hash)
		}
		seen[hash] = true
	}
	if len(seen) != 3 {
		t.Fatalf("seen = %#v, want 3 events", seen)
	}
}

func TestMonitoringAnalyticsAPIPostUsesFrontendSchemaAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	nowMS := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC).UnixMilli()
	insertDashboardEvents(t, dbPath, []plusstore.Event{
		{
			RequestID:             "analytics-ok",
			TimestampMS:           nowMS - 1000,
			Provider:              "opencode-go",
			Model:                 "claude-sonnet-4",
			ResolvedModel:         "claude-sonnet-4",
			Endpoint:              "/v1/messages",
			AuthIndex:             "auth-opencode",
			Source:                "entry-a.json",
			SourceHash:            "source-a",
			APIKeyHash:            "key-a",
			AccountSnapshot:       "opencode-go:anthropic:placeholder",
			AuthLabelSnapshot:     "opencode-go:anthropic:placeholder",
			AuthProviderSnapshot:  "opencode-go:anthropic:placeholder",
			AuthProjectIDSnapshot: "opencode-go:anthropic:placeholder",
			InputTokens:           10,
			OutputTokens:          20,
			TotalTokens:           30,
		},
		{
			RequestID:       "analytics-failed",
			TimestampMS:     nowMS - 500,
			Provider:        "openai",
			Model:           "gpt-5",
			Endpoint:        "/v1/responses",
			AuthIndex:       "auth-openai",
			APIKeyHash:      "key-b",
			InputTokens:     5,
			TotalTokens:     5,
			Failed:          true,
			FailSummary:     "quota",
			HeaderErrorKind: "quota",
		},
	})
	bridge := newTestUsageBridge(t, dbPath)
	router := gin.New()
	router.POST("/analytics", NewHandlers(bridge, WithMonitoringAuthMetadataProvider(func(_ context.Context) map[string]MonitoringAuthMetadata {
		return map[string]MonitoringAuthMetadata{
			"auth-opencode": {
				AccountName: "alice",
				AuthLabel:   "opencode-key",
				AuthFile:    "entry-a.json",
				Provider:    "claude",
				ProjectID:   "workspace-a",
			},
		}
	})).MonitoringAnalyticsAPI)

	payload := fmt.Sprintf(`{
		"from_ms": %d,
		"to_ms": %d,
		"search_query": "sonnet",
		"filters": {"auth_indices": ["auth-opencode"], "include_failed": false},
			"include": {
				"summary": true,
				"summary_profile": "compact",
				"timeline": true,
				"model_share": true,
				"channel_share": true,
				"model_stats": true,
				"granularity": "day",
				"account_stats": true,
				"credential_stats": true,
				"credential_timeline": true,
				"api_key_stats": true,
				"filter_options": true,
				"filter_selectors": true,
				"heatmap": true,
				"anomaly_points": true,
				"events_page": {"limit": 1, "before_ms": null, "before_id": null},
				"drilldown_preview": {"from_ms": %d, "to_ms": %d, "limit": 1}
			}
		}`, nowMS-10_000, nowMS+1000, nowMS-10_000, nowMS+1000)
	body, raw := performMonitoringPost(t, router, "/analytics", payload)
	if strings.Contains(raw, "placeholder") {
		t.Fatalf("analytics response kept OpenCode placeholder identity: %s", raw)
	}
	summary := body["summary"].(map[string]any)
	if summary["total_calls"] != float64(1) || summary["failure_calls"] != float64(0) {
		t.Fatalf("summary = %#v, want one successful filtered call", summary)
	}
	accounts := body["account_stats"].([]any)
	if len(accounts) != 1 || accounts[0].(map[string]any)["account_snapshot"] != "alice" {
		t.Fatalf("account_stats = %#v, want enriched alice row", accounts)
	}
	keys := body["api_key_stats"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["api_key_hash"] != "key-a" {
		t.Fatalf("api_key_stats = %#v, want key-a row", keys)
	}
	models := body["model_stats"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["model"] != "claude-sonnet-4" {
		t.Fatalf("model_stats = %#v, want claude-sonnet-4 row", models)
	}
	channels := body["channel_share"].([]any)
	if len(channels) != 1 || channels[0].(map[string]any)["auth_index"] != "auth-opencode" {
		t.Fatalf("channel_share = %#v, want auth-opencode row", channels)
	}
	credentials := body["credential_stats"].([]any)
	if len(credentials) != 1 || credentials[0].(map[string]any)["auth_index"] != "auth-opencode" {
		t.Fatalf("credential_stats = %#v, want auth-opencode row", credentials)
	}
	credentialTimeline := body["credential_timeline"].([]any)
	if len(credentialTimeline) != 1 || credentialTimeline[0].(map[string]any)["calls"] != float64(1) {
		t.Fatalf("credential_timeline = %#v, want one point", credentialTimeline)
	}
	timeline := body["timeline"].([]any)
	if len(timeline) != 1 || timeline[0].(map[string]any)["calls"] != float64(1) {
		t.Fatalf("timeline = %#v, want one daily point", timeline)
	}
	heatmap := body["heatmap"].([]any)
	if len(heatmap) != 1 || heatmap[0].(map[string]any)["calls"] != float64(1) {
		t.Fatalf("heatmap = %#v, want one cell", heatmap)
	}
	events := body["events"].(map[string]any)
	if events["has_more"] != false || len(events["items"].([]any)) != 1 {
		t.Fatalf("events = %#v, want one item without more", events)
	}
	drilldown := body["drilldown_preview"].(map[string]any)
	if drilldown["has_more"] != false || len(drilldown["items"].([]any)) != 1 {
		t.Fatalf("drilldown_preview = %#v, want one item without more", drilldown)
	}
	if body["filter_options"] == nil {
		t.Fatalf("filter_options missing from response %#v", body)
	}
	filterOptions := body["filter_options"].(map[string]any)
	if len(filterOptions["model_stats"].([]any)) != 1 || len(filterOptions["credential_stats"].([]any)) != 1 {
		t.Fatalf("filter_options = %#v, want frontend usage analytics stats", filterOptions)
	}
}

func performMonitoringRequest(t *testing.T, router http.Handler, path string) (map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body, rec.Body.String()
}

func performMonitoringPost(t *testing.T, router http.Handler, path string, payload string) (map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body, rec.Body.String()
}
