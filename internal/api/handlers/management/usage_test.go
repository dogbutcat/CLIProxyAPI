package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	usagebridge "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	plusstore "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/plusvendor/store"
)

func TestGetUsageQueuePopsRequestedRecords(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload []json.RawMessage
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if len(payload) != 2 {
			t.Fatalf("response records = %d, want 2", len(payload))
		}
		requireRecordID(t, payload[0], 1)
		requireRecordID(t, payload[1], 2)

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":3}` {
			t.Fatalf("remaining queue = %q, want third item only", remaining)
		}
	})
}

func TestGetUsageQueueInvalidCountDoesNotPop(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=0", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":1}` {
			t.Fatalf("remaining queue = %q, want original item", remaining)
		}
	})
}

func TestDashboardSummaryManagementHandlerUsesUsageBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	nowMS := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixMilli()
	insertManagementDashboardEvents(t, dbPath, []plusstore.Event{{
		RequestID:     "req-1",
		TimestampMS:   nowMS - 5*60*1000,
		Provider:      "claude",
		Model:         "claude-sonnet-4",
		ResolvedModel: "claude-sonnet-4",
		Endpoint:      "/v1/messages",
		InputTokens:   5,
		OutputTokens:  7,
		TotalTokens:   12,
	}})
	bridge := newManagementUsageBridge(t, dbPath)
	handler := &Handler{}
	handler.SetUsageBridge(bridge)

	router := gin.New()
	router.GET("/v0/management/dashboard/summary", handler.GetDashboardSummary)
	todayMS := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v0/management/dashboard/summary?today_start_ms=%d&now_ms=%d", todayMS, nowMS), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	today := body["today"].(map[string]any)
	if today["total_calls"] != float64(1) {
		t.Fatalf("today.total_calls = %#v, want 1", today["total_calls"])
	}
}

func TestStoreStatusManagementHandlerReportsCollectorAndStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	bridge := newManagementUsageBridge(t, dbPath)
	handler := &Handler{}
	handler.SetUsageBridge(bridge)

	router := gin.New()
	router.GET("/v0/management/usage/status", handler.GetUsageStoreStatus)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	store := body["store"].(map[string]any)
	if store["state"] != "empty" || store["configured"] != true {
		t.Fatalf("store = %#v, want empty configured store", store)
	}
	collector := body["collector"].(map[string]any)
	if collector["collector"] != "integrated" {
		t.Fatalf("collector = %#v, want integrated collector", collector)
	}
}

func TestMonitoringManagementHandlerUsesUsageBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	nowMS := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC).UnixMilli()
	insertManagementDashboardEvents(t, dbPath, []plusstore.Event{{
		RequestID:    "monitoring-1",
		TimestampMS:  nowMS,
		Provider:     "claude",
		Model:        "claude-sonnet-4",
		Endpoint:     "/v1/messages",
		AuthIndex:    "account-a",
		InputTokens:  2,
		OutputTokens: 3,
		TotalTokens:  5,
	}})
	bridge := newManagementUsageBridge(t, dbPath)
	handler := &Handler{}
	handler.SetUsageBridge(bridge)

	router := gin.New()
	router.GET("/v0/management/usage/monitoring/realtime", handler.GetMonitoringRealtime)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/monitoring/realtime?limit=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	summary := body["summary"].(map[string]any)
	if summary["total_calls"] != float64(1) {
		t.Fatalf("summary = %#v, want one call", summary)
	}
	events := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
}

func TestMonitoringAnalyticsManagementHandlerUsesFrontendRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	nowMS := time.Date(2026, 8, 2, 15, 30, 0, 0, time.UTC).UnixMilli()
	insertManagementDashboardEvents(t, dbPath, []plusstore.Event{{
		RequestID:    "monitoring-post-1",
		TimestampMS:  nowMS,
		Provider:     "claude",
		Model:        "claude-sonnet-4",
		Endpoint:     "/v1/messages",
		AuthIndex:    "account-a",
		APIKeyHash:   "key-a",
		InputTokens:  2,
		OutputTokens: 3,
		TotalTokens:  5,
	}})
	bridge := newManagementUsageBridge(t, dbPath)
	handler := &Handler{}
	handler.SetUsageBridge(bridge)

	router := gin.New()
	router.POST("/v0/management/monitoring/analytics", handler.PostMonitoringAnalytics)
	payload := fmt.Sprintf(`{"from_ms":%d,"to_ms":%d,"include":{"summary":true,"account_stats":true,"events_page":{"limit":1}}}`, nowMS-1000, nowMS+1000)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/monitoring/analytics", bytes.NewBufferString(payload))
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
	summary := body["summary"].(map[string]any)
	if summary["total_calls"] != float64(1) {
		t.Fatalf("summary = %#v, want one call", summary)
	}
	if _, ok := body["events"].(map[string]any); !ok {
		t.Fatalf("events page missing from response %#v", body)
	}
}

func TestModelPriceManagementContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	nowMS := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC).UnixMilli()
	insertManagementDashboardEvents(t, dbPath, []plusstore.Event{{
		RequestID:      "price-usage-1",
		TimestampMS:    nowMS,
		Provider:       "openai",
		Model:          "gpt-5",
		RequestedModel: "gpt-5-alias",
		ResolvedModel:  "gpt-5",
		Endpoint:       "/v1/responses",
		InputTokens:    10,
		OutputTokens:   20,
		TotalTokens:    30,
	}})
	handler := &Handler{}
	handler.SetUsageBridge(newManagementUsageBridge(t, dbPath))

	router := gin.New()
	router.GET("/v0/management/model-prices", handler.GetModelPrices)
	router.PUT("/v0/management/model-prices", handler.PutModelPrices)
	router.DELETE("/v0/management/model-prices/:model", handler.DeleteModelPrice)
	router.GET("/v0/management/model-prices/usage-summary", handler.GetModelPriceUsageSummary)
	router.POST("/v0/management/model-prices/sync", handler.PostModelPricesSync)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/model-prices", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty prices status = %d body=%s", rec.Code, rec.Body.String())
	}
	var pricesBody map[string]map[string]plusstore.ModelPrice
	if err := json.Unmarshal(rec.Body.Bytes(), &pricesBody); err != nil {
		t.Fatalf("decode prices: %v", err)
	}
	if len(pricesBody["prices"]) != 0 {
		t.Fatalf("prices = %#v, want empty success", pricesBody["prices"])
	}

	payload := `{"prices":{"gpt-5":{"prompt":0,"completion":2.5,"cache":0,"cacheRead":0.1,"cacheCreation":0.2,"source":"manual"}}}`
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/model-prices", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save prices status = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pricesBody); err != nil {
		t.Fatalf("decode saved prices: %v", err)
	}
	if pricesBody["prices"]["gpt-5"].Prompt != 0 || pricesBody["prices"]["gpt-5"].Completion != 2.5 {
		t.Fatalf("saved price = %#v", pricesBody["prices"]["gpt-5"])
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/model-prices/usage-summary?limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("usage summary status = %d body=%s", rec.Code, rec.Body.String())
	}
	var summary plusstore.ModelPriceUsageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode usage summary: %v", err)
	}
	if summary.TotalEvents != 1 || len(summary.Models) == 0 {
		t.Fatalf("usage summary = %#v, want one sampled model", summary)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/model-prices/sync", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	var syncBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &syncBody); err != nil {
		t.Fatalf("decode sync body: %v", err)
	}
	if syncBody["imported"] != float64(0) {
		t.Fatalf("sync body = %#v, want empty sync success", syncBody)
	}
}

func TestAPIKeyUsageAliasesManagementContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	handler.SetUsageBridge(newManagementUsageBridge(t, filepath.Join(t.TempDir(), "usage.db")))
	router := gin.New()
	router.GET("/v0/management/api-key-aliases", handler.GetAPIKeyAliases)
	router.PUT("/v0/management/api-key-aliases", handler.PutAPIKeyAliases)
	router.DELETE("/v0/management/api-key-aliases/:api_key_hash", handler.DeleteAPIKeyAlias)

	hash := plusstore.HashString("sk-secret-alias-key")
	payload := fmt.Sprintf(`{"items":[{"apiKeyHash":%q,"alias":"production"}],"activeApiKeyHashes":[%q],"allowOrphanAliasCleanup":true}`, hash, hash)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/api-key-aliases", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save aliases status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []plusstore.APIKeyAlias `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode aliases: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].APIKeyHash != hash || body.Items[0].Alias != "production" {
		t.Fatalf("aliases = %#v", body.Items)
	}
	if strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("alias response leaked secret: %s", rec.Body.String())
	}

	duplicate := fmt.Sprintf(`{"items":[{"apiKeyHash":%q,"alias":"dup"},{"apiKeyHash":%q,"alias":"dup"}]}`, hash, plusstore.HashString("other"))
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v0/management/api-key-aliases", bytes.NewBufferString(duplicate))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate alias status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v0/management/api-key-aliases/"+hash, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete alias status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUsageExportImportManagementContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sourceDB := filepath.Join(t.TempDir(), "source.db")
	nowMS := time.Date(2026, 8, 2, 17, 0, 0, 0, time.UTC).UnixMilli()
	insertManagementDashboardEvents(t, sourceDB, []plusstore.Event{{
		RequestID:      "export-1",
		TimestampMS:    nowMS,
		Provider:       "openai",
		Model:          "gpt-5",
		Endpoint:       "/v1/responses",
		Source:         "sk-secret-export-key-123456",
		InputTokens:    1,
		OutputTokens:   2,
		TotalTokens:    3,
		FailBody:       "Authorization: Bearer secret-token-value",
		FailSummary:    "x-api-key=sk-secret-export-key-123456",
		Failed:         true,
		FailStatusCode: 429,
	}})
	sourceHandler := &Handler{}
	sourceHandler.SetUsageBridge(newManagementUsageBridge(t, sourceDB))
	sourceRouter := gin.New()
	sourceRouter.GET("/v0/management/usage/export", sourceHandler.ExportUsage)

	rec := httptest.NewRecorder()
	sourceRouter.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/jsonl") {
		t.Fatalf("export content-type = %q", got)
	}
	exported := rec.Body.String()
	if strings.Contains(exported, "sk-secret-export-key") || strings.Contains(exported, "secret-token-value") {
		t.Fatalf("export leaked secret: %s", exported)
	}

	targetHandler := &Handler{}
	targetHandler.SetUsageBridge(newManagementUsageBridge(t, filepath.Join(t.TempDir(), "target.db")))
	targetRouter := gin.New()
	targetRouter.POST("/v0/management/usage/import", targetHandler.ImportUsage)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader(exported))
	targetRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	var result plusstore.UsageImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if result.Added != 1 || result.Failed != 0 || result.Total != 1 {
		t.Fatalf("import result = %#v, want one imported event", result)
	}
}

func TestAccountActionManagementRoutesUseLocalCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	insertManagementDashboardEvents(t, dbPath, []plusstore.Event{{
		RequestID:             "account-action-auth-failure",
		EventHash:             "account-action-auth-failure",
		TimestampMS:           time.Date(2026, 8, 2, 19, 0, 0, 0, time.UTC).UnixMilli(),
		Timestamp:             time.Date(2026, 8, 2, 19, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Provider:              "codex",
		Model:                 "gpt-5",
		Endpoint:              "/v1/responses",
		AuthFileSnapshot:      "codex-expired.json",
		AuthIndex:             "codex-auth-1",
		AccountSnapshot:       "expired@example.test",
		AuthLabelSnapshot:     "expired@example.test",
		AuthProviderSnapshot:  "codex",
		AuthProjectIDSnapshot: "workspace-1",
		Failed:                true,
		FailStatusCode:        http.StatusUnauthorized,
		FailSummary:           "invalid_token: token is expired",
		InputTokens:           1,
		OutputTokens:          1,
		TotalTokens:           2,
	}})
	handler := &Handler{}
	handler.SetUsageBridge(newManagementUsageBridge(t, dbPath))
	router := gin.New()
	router.GET("/v0/management/usage/capabilities", handler.GetUsageCapabilities)
	router.GET("/v0/management/account-action-candidates", handler.ListAccountActionCandidates)
	router.POST("/v0/management/account-action-candidates/:id/ignore", handler.IgnoreAccountActionCandidate)
	router.POST("/v0/management/account-action-candidates/:id/resolve", handler.ResolveAccountActionCandidate)
	router.POST("/v0/management/account-action-candidates/:id/enable", handler.EnableAccountActionCandidate)
	router.DELETE("/v0/management/account-action-candidates/:id/auth-file", handler.DeleteAccountActionCandidateAuthFile)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d body=%s", rec.Code, rec.Body.String())
	}
	var caps map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	accountActions := caps["account_actions"].(map[string]any)
	if accountActions["supported"] != true || accountActions["version"] != "local-sqlite-v1" {
		t.Fatalf("account action capability = %#v", accountActions)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/account-action-candidates?status=pending&limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var listBody struct {
		Items []struct {
			ID           int64  `json:"id"`
			ActionType   string `json:"actionType"`
			Status       string `json:"status"`
			AuthFileName string `json:"authFileName"`
			ReasonCode   string `json:"reasonCode"`
		} `json:"items"`
		PendingCount int64 `json:"pendingCount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	if listBody.PendingCount != 1 || len(listBody.Items) != 1 {
		t.Fatalf("list body = %#v", listBody)
	}
	item := listBody.Items[0]
	if item.ActionType != plusstore.AccountActionTypeReauth || item.AuthFileName != "codex-expired.json" || item.ReasonCode != "invalid_credentials" {
		t.Fatalf("candidate item = %#v", item)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/account-action-candidates?status=all&limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list all status = %d body=%s", rec.Code, rec.Body.String())
	}
	listBody = struct {
		Items []struct {
			ID           int64  `json:"id"`
			ActionType   string `json:"actionType"`
			Status       string `json:"status"`
			AuthFileName string `json:"authFileName"`
			ReasonCode   string `json:"reasonCode"`
		} `json:"items"`
		PendingCount int64 `json:"pendingCount"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list all body: %v", err)
	}
	if listBody.PendingCount != 1 || len(listBody.Items) != 1 {
		t.Fatalf("list all body = %#v", listBody)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v0/management/account-action-candidates/%d/ignore", item.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ignore status = %d body=%s", rec.Code, rec.Body.String())
	}
	var mutationBody struct {
		Item struct {
			Status string `json:"status"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &mutationBody); err != nil {
		t.Fatalf("decode ignore body: %v", err)
	}
	if mutationBody.Item.Status != plusstore.AccountActionStatusIgnored {
		t.Fatalf("ignored item status = %q", mutationBody.Item.Status)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v0/management/account-action-candidates/%d/enable", item.ID), nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("enable status = %d body=%s", rec.Code, rec.Body.String())
	}
	mutationBody = struct {
		Item struct {
			Status string `json:"status"`
		} `json:"item"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &mutationBody); err != nil {
		t.Fatalf("decode enable body: %v", err)
	}
	if mutationBody.Item.Status != plusstore.AccountActionStatusIgnored {
		t.Fatalf("enable failure changed status to %q", mutationBody.Item.Status)
	}

	store, err := plusstore.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, dead, err := store.Counts(context.Background())
	if err != nil {
		t.Fatalf("count store: %v", err)
	}
	if events != 1 || dead != 0 {
		t.Fatalf("store counts = events:%d dead:%d, want usage event preserved", events, dead)
	}
}

func TestManagementImportSessionCreateGetUsesConfiguredSessionDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	handler := &Handler{cfg: &config.Config{UsageImportSession: config.UsageImportSessionConfig{Dir: sessionDir}}}
	handler.SetUsageBridge(newManagementUsageBridge(t, filepath.Join(t.TempDir(), "usage.db")))
	router := gin.New()
	router.POST("/v0/management/usage/import-sessions", handler.CreateUsageImportSession)
	router.GET("/v0/management/usage/import-sessions/:id", handler.GetUsageImportSession)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import-sessions", strings.NewReader(`{"filename":"usage.jsonl","size_bytes":10,"resume_key":"mgmt-resume"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created usagebridge.UsageImportSession
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created session: %v", err)
	}
	if created.ID == "" || created.ChunkSizeBytes != config.DefaultUsageImportSessionChunkSizeBytes {
		t.Fatalf("created session = %#v", created)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, created.ID, "metadata.json")); err != nil {
		t.Fatalf("metadata not written under configured dir: %v", err)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/import-sessions/"+created.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func withManagementUsageQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	defer func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	}()

	fn()
}

func requireRecordID(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()

	var payload struct {
		ID int `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal record: %v", errUnmarshal)
	}
	if payload.ID != want {
		t.Fatalf("record id = %d, want %d", payload.ID, want)
	}
}

func newManagementUsageBridge(t *testing.T, dbPath string) *usagebridge.Bridge {
	t.Helper()
	bridge, err := usagebridge.NewBridge(usagebridge.BridgeConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("new usage bridge: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	return bridge
}

func insertManagementDashboardEvents(t *testing.T, dbPath string, events []plusstore.Event) {
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
