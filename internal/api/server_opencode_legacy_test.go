package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

type legacyOpenCodeGoQuotaRuntime struct {
	entryName string
}

func (r *legacyOpenCodeGoQuotaRuntime) SnapshotOpenCodeGoQuota() map[string]*quota.PollResult {
	return nil
}

func (r *legacyOpenCodeGoQuotaRuntime) RefreshOpenCodeGoQuota(_ context.Context, entryName string) (*quota.PollResult, bool, error) {
	r.entryName = entryName
	return &quota.PollResult{
		EntryName: entryName,
		Quota:     &quota.OpenCodeGoQuota{Monthly: &quota.OpenCodeGoWindow{PercentRemaining: 42}},
		Timestamp: time.Date(2026, 7, 27, 1, 2, 3, 0, time.UTC),
	}, true, nil
}

func TestLegacyOpenCodeGoManagementRoutesUseBackendRuntime(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	quotaRuntime := &legacyOpenCodeGoQuotaRuntime{}
	server := newTestServerWithOptions(t, WithOpenCodeGoQuotaRuntime(quotaRuntime))

	missingKey := httptest.NewRequest(http.MethodGet, "/v0/management/opencode-go/workspace-1/quota", nil)
	missingKeyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(missingKeyRR, missingKey)
	if missingKeyRR.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want %d; body=%s", missingKeyRR.Code, http.StatusUnauthorized, missingKeyRR.Body.String())
	}

	quotaReq := httptest.NewRequest(http.MethodGet, "/v0/management/opencode-go/workspace-1/quota", nil)
	quotaReq.Header.Set("Authorization", "Bearer test-management-key")
	quotaRR := httptest.NewRecorder()
	server.engine.ServeHTTP(quotaRR, quotaReq)
	if quotaRR.Code != http.StatusOK {
		t.Fatalf("legacy quota status = %d, want %d; body=%s", quotaRR.Code, http.StatusOK, quotaRR.Body.String())
	}
	if quotaRuntime.entryName != "workspace-1" {
		t.Fatalf("quota runtime entry = %q, want workspace-1", quotaRuntime.entryName)
	}
	var quotaBody struct {
		EntryName string                 `json:"entry-name"`
		Quota     *quota.OpenCodeGoQuota `json:"quota"`
	}
	if errUnmarshal := json.Unmarshal(quotaRR.Body.Bytes(), &quotaBody); errUnmarshal != nil {
		t.Fatalf("decode quota response: %v", errUnmarshal)
	}
	if quotaBody.EntryName != "workspace-1" || quotaBody.Quota == nil || quotaBody.Quota.Monthly == nil || quotaBody.Quota.Monthly.PercentRemaining != 42 {
		t.Fatalf("legacy quota response = %#v", quotaBody)
	}

	referralReq := httptest.NewRequest(http.MethodGet, "/v0/management/opencode-go/workspace-1/referral", nil)
	referralReq.Header.Set("Authorization", "Bearer test-management-key")
	referralRR := httptest.NewRecorder()
	server.engine.ServeHTTP(referralRR, referralReq)
	if referralRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy referral status = %d, want %d; body=%s", referralRR.Code, http.StatusServiceUnavailable, referralRR.Body.String())
	}
}
