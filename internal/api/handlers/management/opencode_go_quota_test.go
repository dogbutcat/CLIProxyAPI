package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

type testOpenCodeGoQuotaRuntime struct {
	snapshot map[string]*quota.PollResult
	refresh  func(context.Context, string) (*quota.PollResult, bool, error)
}

func (r *testOpenCodeGoQuotaRuntime) SnapshotOpenCodeGoQuota() map[string]*quota.PollResult {
	return r.snapshot
}

func (r *testOpenCodeGoQuotaRuntime) RefreshOpenCodeGoQuota(ctx context.Context, entryName string) (*quota.PollResult, bool, error) {
	if r.refresh == nil {
		return nil, true, nil
	}
	return r.refresh(ctx, entryName)
}

func TestOpenCodeGoQuotaSnapshotsAndManualRefresh(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	h.SetOpenCodeGoQuotaRuntime(&testOpenCodeGoQuotaRuntime{
		snapshot: map[string]*quota.PollResult{
			"entry-b": {EntryName: "entry-b", Error: errors.New("poll failed"), Timestamp: time.Date(2026, 7, 27, 1, 2, 3, 4, time.UTC)},
			"entry-a": {EntryName: "entry-a", Quota: &quota.OpenCodeGoQuota{Rolling: &quota.OpenCodeGoWindow{PercentRemaining: 80}}, Timestamp: time.Date(2026, 7, 27, 1, 2, 3, 4, time.UTC)},
		},
		refresh: func(_ context.Context, entryName string) (*quota.PollResult, bool, error) {
			if entryName != "entry-a" {
				return nil, true, nil
			}
			return &quota.PollResult{EntryName: entryName, Quota: &quota.OpenCodeGoQuota{Monthly: &quota.OpenCodeGoWindow{PercentRemaining: 25}}, Timestamp: time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)}, true, nil
		},
	})

	snapshotRec := performOpenCodeGoQuotaRequest(t, h, http.MethodGet, "/quota", nil)
	if snapshotRec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, want %d; body=%s", snapshotRec.Code, http.StatusOK, snapshotRec.Body.String())
	}
	if strings.Index(snapshotRec.Body.String(), "entry-a") > strings.Index(snapshotRec.Body.String(), "entry-b") {
		t.Fatalf("snapshots are not sorted: %s", snapshotRec.Body.String())
	}

	refreshRec := performOpenCodeGoQuotaRequest(t, h, http.MethodPost, "/quota/entry-a/refresh", nil)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want %d; body=%s", refreshRec.Code, http.StatusOK, refreshRec.Body.String())
	}
	if !strings.Contains(refreshRec.Body.String(), `"entry-name":"entry-a"`) || !strings.Contains(refreshRec.Body.String(), `"percentRemaining":25`) {
		t.Fatalf("unexpected refresh response: %s", refreshRec.Body.String())
	}

	legacyRec := performOpenCodeGoQuotaRequest(t, h, http.MethodGet, "/entry-a/quota", nil)
	if legacyRec.Code != http.StatusOK {
		t.Fatalf("legacy refresh status = %d, want %d; body=%s", legacyRec.Code, http.StatusOK, legacyRec.Body.String())
	}
	if strings.Contains(legacyRec.Body.String(), `"quota":{"entry-name"`) {
		t.Fatalf("legacy response used wrapped shape: %s", legacyRec.Body.String())
	}
	if !strings.Contains(legacyRec.Body.String(), `"entry-name":"entry-a"`) || !strings.Contains(legacyRec.Body.String(), `"percentRemaining":25`) {
		t.Fatalf("unexpected legacy response: %s", legacyRec.Body.String())
	}
}

func TestOpenCodeGoQuotaRefreshMissingRuntimeEntryAndCancellation(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	missingRuntime := performOpenCodeGoQuotaRequest(t, h, http.MethodPost, "/quota/entry-a/refresh", nil)
	if missingRuntime.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing runtime status = %d, want %d; body=%s", missingRuntime.Code, http.StatusServiceUnavailable, missingRuntime.Body.String())
	}

	h.SetOpenCodeGoQuotaRuntime(&testOpenCodeGoQuotaRuntime{})
	missingEntry := performOpenCodeGoQuotaRequest(t, h, http.MethodPost, "/quota/entry-a/refresh", nil)
	if missingEntry.Code != http.StatusNotFound {
		t.Fatalf("missing entry status = %d, want %d; body=%s", missingEntry.Code, http.StatusNotFound, missingEntry.Body.String())
	}

	canceled := performOpenCodeGoQuotaRequest(t, h, http.MethodPost, "/quota/bad-entry/refresh", func(req *http.Request) *http.Request {
		ctx, cancel := context.WithCancel(req.Context())
		cancel()
		return req.WithContext(ctx)
	})
	if canceled.Code != statusClientClosedRequest {
		t.Fatalf("canceled status = %d, want %d; body=%s", canceled.Code, statusClientClosedRequest, canceled.Body.String())
	}

	badEntry := performOpenCodeGoQuotaRequest(t, h, http.MethodPost, "/quota/bad/entry/refresh", nil)
	if badEntry.Code != http.StatusNotFound {
		t.Fatalf("bad path status = %d, want route 404 before handler; body=%s", badEntry.Code, badEntry.Body.String())
	}
}

func performOpenCodeGoQuotaRequest(t *testing.T, h *Handler, method, target string, mutate func(*http.Request) *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.GET("/quota", h.GetOpenCodeGoQuota)
	router.POST("/quota/:entry/refresh", h.PostOpenCodeGoQuotaRefresh)
	router.GET("/:entry/quota", h.GetOpenCodeGoQuotaEntry)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	if mutate != nil {
		req = mutate(req)
	}
	router.ServeHTTP(rec, req)
	return rec
}
