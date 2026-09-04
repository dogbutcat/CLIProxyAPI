package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUsageCapabilitiesReportAccountActionsSupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/v0/management"), nil, nil, nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/management/usage/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	accountActions := body["account_actions"].(map[string]any)
	if accountActions["supported"] != true || accountActions["version"] != "local-sqlite-v1" {
		t.Fatalf("account_actions = %#v", accountActions)
	}
}
