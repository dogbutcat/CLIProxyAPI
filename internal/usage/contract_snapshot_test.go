package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUsageContractRoutesCoverDashboardMonitoringPricesImport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/v0/management"), router, router, nil)

	seen := make(map[string]struct{})
	for _, route := range router.Routes() {
		seen[route.Method+" "+route.Path] = struct{}{}
	}

	want := []string{
		"GET /usage-service/info",
		"GET /usage-service/config",
		"GET /status",
		"GET /v0/management/usage/dashboard/summary",
		"GET /v0/management/usage/capabilities",
		"GET /v0/management/usage/monitoring/accounts",
		"GET /v0/management/usage/monitoring/keys",
		"GET /v0/management/usage/monitoring/realtime",
		"GET /v0/management/usage/monitoring/selectors",
		"POST /v0/management/usage/monitoring/analytics",
		"GET /v0/management/usage/monitoring/header-snapshots",
		"GET /v0/management/usage/model-prices",
		"PUT /v0/management/usage/model-prices",
		"DELETE /v0/management/usage/model-prices/:model",
		"GET /v0/management/usage/model-prices/usage-summary",
		"POST /v0/management/usage/model-prices/sync",
		"POST /v0/management/usage/import",
		"POST /v0/management/usage/import-sessions",
		"GET /v0/management/usage/import-sessions/:id",
		"PUT /v0/management/usage/import-sessions/:id/chunk",
		"POST /v0/management/usage/import-sessions/:id/complete",
		"DELETE /v0/management/usage/import-sessions/:id",
		"GET /v0/management/usage/status",
	}
	sort.Strings(want)
	for _, key := range want {
		if _, ok := seen[key]; !ok {
			t.Fatalf("missing usage contract route %s", key)
		}
	}
}

func TestUsageContractCapabilityAndImportErrorSchemas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/v0/management"), nil, nil, nil)

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
	if accountActions["supported"] != true || accountActions["reason"] != "" || accountActions["version"] != "local-sqlite-v1" {
		t.Fatalf("account_actions capability = %#v", accountActions)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("import without bridge status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode import error body: %v", err)
	}
	if body["error"] != "usage bridge is unavailable" {
		t.Fatalf("import error envelope = %#v", body)
	}
	status, ok := body["status"].(map[string]any)
	if !ok || status["state"] != "unavailable" {
		t.Fatalf("import error status = %#v", body["status"])
	}
}
