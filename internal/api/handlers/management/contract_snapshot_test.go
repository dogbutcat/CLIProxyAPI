package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type managementContractSnapshot struct {
	SchemaVersion int                       `json:"schema_version"`
	Redaction     managementRedaction       `json:"redaction"`
	Routes        []managementContractRoute `json:"routes"`
}

type managementRedaction struct {
	Secrets       string `json:"secrets"`
	DynamicValues string `json:"dynamic_values"`
}

type managementContractRoute struct {
	Category      string `json:"category"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Disposition   string `json:"disposition"`
	ErrorEnvelope string `json:"error_envelope"`
}

func managementContractSnapshotForTest() managementContractSnapshot {
	return managementContractSnapshot{
		SchemaVersion: 1,
		Redaction: managementRedaction{
			Secrets:       "omitted",
			DynamicValues: "omitted",
		},
		Routes: []managementContractRoute{
			{Category: "actions", Method: http.MethodDelete, Path: "/v0/management/account-action-candidates/:id/auth-file", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "actions", Method: http.MethodGet, Path: "/v0/management/account-action-candidates", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "actions", Method: http.MethodPost, Path: "/v0/management/account-action-candidates/:id/enable", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "actions", Method: http.MethodPost, Path: "/v0/management/account-action-candidates/:id/ignore", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "actions", Method: http.MethodPost, Path: "/v0/management/account-action-candidates/:id/resolve", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "capability", Method: http.MethodGet, Path: "/v0/management/usage/capabilities", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "dashboard", Method: http.MethodGet, Path: "/v0/management/dashboard/summary", Disposition: "legacy", ErrorEnvelope: `{"error":string}`},
			{Category: "dashboard", Method: http.MethodGet, Path: "/v0/management/usage/dashboard/summary", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "import", Method: http.MethodDelete, Path: "/v0/management/usage/import-sessions/:id", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "import", Method: http.MethodGet, Path: "/v0/management/usage/import-sessions/:id", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "import", Method: http.MethodPost, Path: "/v0/management/usage/import", Disposition: "legacy-single-post", ErrorEnvelope: `{"error":string}`},
			{Category: "import", Method: http.MethodPost, Path: "/v0/management/usage/import-sessions", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "import", Method: http.MethodPost, Path: "/v0/management/usage/import-sessions/:id/complete", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "import", Method: http.MethodPut, Path: "/v0/management/usage/import-sessions/:id/chunk", Disposition: "canonical", ErrorEnvelope: `{"error":string,"code":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/monitoring/accounts", Disposition: "legacy", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/monitoring/keys", Disposition: "legacy", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/monitoring/realtime", Disposition: "legacy", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/monitoring/selectors", Disposition: "legacy", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/usage/monitoring/accounts", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/usage/monitoring/keys", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/usage/monitoring/realtime", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodGet, Path: "/v0/management/usage/monitoring/selectors", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodPost, Path: "/v0/management/monitoring/analytics", Disposition: "legacy", ErrorEnvelope: `{"error":string}`},
			{Category: "monitoring", Method: http.MethodPost, Path: "/v0/management/usage/monitoring/analytics", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "opencode", Method: http.MethodGet, Path: "/v0/management/opencode-go", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "opencode", Method: http.MethodGet, Path: "/v0/management/opencode-go/quota", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "opencode", Method: http.MethodGet, Path: "/v0/management/opencode-go/referral/:workspace", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "opencode", Method: http.MethodGet, Path: "/v0/management/opencode-go/referral/:workspace/rewards/:reward/preview", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "opencode", Method: http.MethodPost, Path: "/v0/management/opencode-go/quota/:entry/refresh", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "opencode", Method: http.MethodPost, Path: "/v0/management/opencode-go/referral/:workspace/rewards/:reward/apply", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "prices", Method: http.MethodDelete, Path: "/v0/management/model-prices/:model", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "prices", Method: http.MethodGet, Path: "/v0/management/model-prices", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "prices", Method: http.MethodGet, Path: "/v0/management/model-prices/usage-summary", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "prices", Method: http.MethodPost, Path: "/v0/management/model-prices/sync", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
			{Category: "prices", Method: http.MethodPut, Path: "/v0/management/model-prices", Disposition: "canonical", ErrorEnvelope: `{"error":string}`},
		},
	}
}

func TestManagementContractSnapshotSchemaAndDeterminism(t *testing.T) {
	snapshot := managementContractSnapshotForTest()
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatalf("marshal contract snapshot: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var decoded managementContractSnapshot
	if err := dec.Decode(&decoded); err != nil {
		t.Fatalf("decode contract snapshot: %v", err)
	}
	if decoded.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", decoded.SchemaVersion)
	}
	text := string(raw)
	for _, forbidden := range []string{"test-management-key", "Authorization", "Bearer ", "secret-token"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("snapshot contains forbidden marker %q", forbidden)
		}
	}
	again, err := json.MarshalIndent(managementContractSnapshotForTest(), "", "  ")
	if err != nil {
		t.Fatalf("marshal contract snapshot again: %v", err)
	}
	if string(again) != text {
		t.Fatal("contract snapshot is not deterministic")
	}
}

func TestManagementContractRequiredRouteDisposition(t *testing.T) {
	snapshot := managementContractSnapshotForTest()
	seen := make(map[string]managementContractRoute)
	for _, route := range snapshot.Routes {
		key := route.Method + " " + route.Path
		if prev, ok := seen[key]; ok {
			t.Fatalf("duplicate route key %q: %#v and %#v", key, prev, route)
		}
		seen[key] = route
		if route.ErrorEnvelope != `{"error":string}` && route.ErrorEnvelope != `{"error":string,"code":string}` {
			t.Fatalf("%s unexpected error envelope %q", key, route.ErrorEnvelope)
		}
	}
	want := map[string]string{
		"GET /v0/management/opencode-go":                                            "canonical",
		"GET /v0/management/opencode-go/quota":                                      "canonical",
		"POST /v0/management/opencode-go/quota/:entry/refresh":                      "canonical",
		"GET /v0/management/dashboard/summary":                                      "legacy",
		"GET /v0/management/usage/dashboard/summary":                                "canonical",
		"GET /v0/management/monitoring/accounts":                                    "legacy",
		"GET /v0/management/usage/monitoring/accounts":                              "canonical",
		"GET /v0/management/model-prices":                                           "canonical",
		"GET /v0/management/account-action-candidates":                              "canonical",
		"POST /v0/management/usage/import":                                          "legacy-single-post",
		"POST /v0/management/usage/import-sessions":                                 "canonical",
		"POST /v0/management/opencode-go/referral/:workspace/rewards/:reward/apply": "canonical",
	}
	for key, disposition := range want {
		got, ok := seen[key]
		if !ok {
			t.Fatalf("missing route %s", key)
		}
		if got.Disposition != disposition {
			t.Fatalf("%s disposition = %q, want %q", key, got.Disposition, disposition)
		}
	}
	keys := make([]string, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		keys = append(keys, route.Category+" "+route.Method+" "+route.Path)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("snapshot routes are not sorted by category, method, path")
	}
}

func TestManagementContractErrorEnvelopeAndCapabilityChecks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	router := gin.New()
	router.GET("/v0/management/opencode-go/quota", handler.GetOpenCodeGoQuota)
	router.POST("/v0/management/opencode-go/quota/:entry/refresh", handler.PostOpenCodeGoQuotaRefresh)
	router.GET("/v0/management/opencode-go/referral/:workspace", handler.GetOpenCodeGoReferralSummary)
	router.GET("/v0/management/usage/capabilities", handler.GetUsageCapabilities)

	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/v0/management/opencode-go/quota", http.StatusServiceUnavailable},
		{http.MethodPost, "/v0/management/opencode-go/quota/" + strings.Repeat("a", 257) + "/refresh", http.StatusBadRequest},
		{http.MethodGet, "/v0/management/opencode-go/referral/workspace-1", http.StatusServiceUnavailable},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s %s status = %d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s %s error body: %v", tc.method, tc.path, err)
		}
		if _, ok := body["error"].(string); !ok {
			t.Fatalf("%s %s missing string error envelope: %#v", tc.method, tc.path, body)
		}
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d body=%s", rec.Code, rec.Body.String())
	}
	var caps struct {
		AccountActions struct {
			Supported bool   `json:"supported"`
			Version   string `json:"version"`
			Reason    string `json:"reason"`
		} `json:"account_actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if !caps.AccountActions.Supported || caps.AccountActions.Version != "local-sqlite-v1" || caps.AccountActions.Reason != "" {
		t.Fatalf("account action capability = %#v", caps.AccountActions)
	}
}
