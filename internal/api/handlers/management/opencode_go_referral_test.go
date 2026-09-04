package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type testOpenCodeGoReferralRuntime struct {
	resolve func(context.Context, string) (string, string, bool, bool, error)
}

func (r *testOpenCodeGoReferralRuntime) ResolveOpenCodeGoReferralWorkspace(ctx context.Context, workspaceID string) (string, string, bool, bool, error) {
	if r.resolve == nil {
		return "", "", true, false, nil
	}
	return r.resolve(ctx, workspaceID)
}

type testOpenCodeGoReferralDoer func(*http.Request) (*http.Response, error)

func (f testOpenCodeGoReferralDoer) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestOpenCodeGoReferralSummaryPreviewAndApplyRequestShape(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	h.SetOpenCodeGoReferralRuntime(&testOpenCodeGoReferralRuntime{resolve: func(_ context.Context, workspaceID string) (string, string, bool, bool, error) {
		if workspaceID != "workspace-1" {
			t.Fatalf("workspaceID = %q, want workspace-1", workspaceID)
		}
		return "session=secret-cookie", "direct", true, true, nil
	}})

	var captured []*http.Request
	oldGateway := openCodeGoReferralGatewayURL
	oldDoer := newOpenCodeGoReferralHTTPDoer
	openCodeGoReferralGatewayURL = "https://opencode.ai/_server"
	newOpenCodeGoReferralHTTPDoer = func(proxyURL string) (openCodeGoReferralHTTPDoer, error) {
		if proxyURL != "direct" {
			t.Fatalf("proxyURL = %q, want direct", proxyURL)
		}
		return testOpenCodeGoReferralDoer(func(req *http.Request) (*http.Response, error) {
			captured = append(captured, req)
			if req.Header.Get("Cookie") != "session=secret-cookie" {
				t.Fatalf("Cookie header = %q, want configured cookie", req.Header.Get("Cookie"))
			}
			if req.Header.Get("Referer") != "https://opencode.ai/workspace/workspace-1/go" {
				t.Fatalf("Referer header = %q", req.Header.Get("Referer"))
			}
			if req.Header.Get("x-server-instance") != "server-fn:1" {
				t.Fatalf("x-server-instance = %q", req.Header.Get("x-server-instance"))
			}
			switch req.Header.Get("x-server-id") {
			case openCodeGoReferralHashSummary, openCodeGoReferralHashPreview:
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`;$R[0]={total:1,rewards:[{id:"reward-1",enabled:!0,created:new Date("2026-07-27T00:00:00.000Z")}]}`)), Header: make(http.Header)}, nil
			case openCodeGoReferralHashApply:
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`undefined`)), Header: make(http.Header)}, nil
			default:
				t.Fatalf("unexpected x-server-id %q", req.Header.Get("x-server-id"))
				return nil, nil
			}
		}), nil
	}
	t.Cleanup(func() {
		openCodeGoReferralGatewayURL = oldGateway
		newOpenCodeGoReferralHTTPDoer = oldDoer
	})

	summary := performOpenCodeGoReferralRequest(t, h, http.MethodGet, "/referral/workspace-1", nil)
	if summary.Code != http.StatusOK || !strings.Contains(summary.Body.String(), `"total":1`) {
		t.Fatalf("summary status/body = %d %s", summary.Code, summary.Body.String())
	}
	preview := performOpenCodeGoReferralRequest(t, h, http.MethodGet, "/referral/workspace-1/rewards/reward-1/preview", nil)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"reward-1"`) {
		t.Fatalf("preview status/body = %d %s", preview.Code, preview.Body.String())
	}
	apply := performOpenCodeGoReferralRequest(t, h, http.MethodPost, "/referral/workspace-1/rewards/reward-1/apply", nil)
	if apply.Code != http.StatusOK || !strings.Contains(apply.Body.String(), `"success":true`) {
		t.Fatalf("apply status/body = %d %s", apply.Code, apply.Body.String())
	}
	if len(captured) != 3 {
		t.Fatalf("captured requests = %d, want 3", len(captured))
	}

	assertOpenCodeGoReferralRequest(t, captured[0], http.MethodGet, openCodeGoReferralHashSummary, []string{"workspace-1"})
	assertOpenCodeGoReferralRequest(t, captured[1], http.MethodGet, openCodeGoReferralHashPreview, []string{"workspace-1", "reward-1"})
	assertOpenCodeGoReferralRequest(t, captured[2], http.MethodPost, openCodeGoReferralHashApply, []string{"workspace-1", "reward-1"})
	if captured[2].URL.RawQuery != "" {
		t.Fatalf("apply raw query = %q, want empty", captured[2].URL.RawQuery)
	}
	if captured[2].Header.Get("x-single-flight") != "true" {
		t.Fatalf("apply x-single-flight = %q", captured[2].Header.Get("x-single-flight"))
	}
}

func TestOpenCodeGoReferralValidationRuntimeBodyLimitAndNoTimeout(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	missingRuntime := performOpenCodeGoReferralRequest(t, h, http.MethodGet, "/referral/workspace-1", nil)
	if missingRuntime.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing runtime status = %d, want %d", missingRuntime.Code, http.StatusServiceUnavailable)
	}

	h.SetOpenCodeGoReferralRuntime(&testOpenCodeGoReferralRuntime{})
	missingWorkspace := performOpenCodeGoReferralRequest(t, h, http.MethodGet, "/referral/workspace-1", nil)
	if missingWorkspace.Code != http.StatusNotFound {
		t.Fatalf("missing workspace status = %d, want %d", missingWorkspace.Code, http.StatusNotFound)
	}
	badWorkspace := performOpenCodeGoReferralRequest(t, h, http.MethodGet, "/referral/bad%0Aid", nil)
	if badWorkspace.Code != http.StatusBadRequest {
		t.Fatalf("bad workspace status = %d, want %d", badWorkspace.Code, http.StatusBadRequest)
	}
	if validOpenCodeGoReferralID("bad&id") {
		t.Fatal("reward validator accepted an id with a query delimiter")
	}
	canceled := performOpenCodeGoReferralRequest(t, h, http.MethodGet, "/referral/workspace-1", func(req *http.Request) *http.Request {
		ctx, cancel := context.WithCancel(req.Context())
		cancel()
		return req.WithContext(ctx)
	})
	if canceled.Code != statusClientClosedRequest {
		t.Fatalf("canceled status = %d, want %d", canceled.Code, statusClientClosedRequest)
	}

	h.SetOpenCodeGoReferralRuntime(&testOpenCodeGoReferralRuntime{resolve: func(context.Context, string) (string, string, bool, bool, error) {
		return "session=secret-cookie", "", true, true, nil
	}})
	oldDoer := newOpenCodeGoReferralHTTPDoer
	newOpenCodeGoReferralHTTPDoer = func(string) (openCodeGoReferralHTTPDoer, error) {
		return testOpenCodeGoReferralDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(openCodeGoReferralMaxBodyLen)+1))), Header: make(http.Header)}, nil
		}), nil
	}
	t.Cleanup(func() { newOpenCodeGoReferralHTTPDoer = oldDoer })
	tooLarge := performOpenCodeGoReferralRequest(t, h, http.MethodGet, "/referral/workspace-1", nil)
	if tooLarge.Code != http.StatusBadGateway {
		t.Fatalf("too large status = %d, want %d", tooLarge.Code, http.StatusBadGateway)
	}

	client, errClient := newOpenCodeGoReferralHTTPClient("")
	if errClient != nil {
		t.Fatalf("newOpenCodeGoReferralHTTPClient returned error: %v", errClient)
	}
	if client.Timeout != 0 {
		t.Fatalf("referral client timeout = %v, want zero", client.Timeout)
	}
}

func TestOpenCodeGoAdaptiveRetryArtifactsOmitted(t *testing.T) {
	repoRoot, errRoot := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if errRoot != nil {
		t.Fatalf("repo root: %v", errRoot)
	}
	for _, rel := range []string{
		"internal/runtime/executor/opencode_go_executor.go",
		"internal/runtime/executor/helps/opencode_go_truncation.go",
	} {
		data, errRead := os.ReadFile(filepath.Join(repoRoot, rel))
		if errRead != nil {
			t.Fatalf("read %s: %v", rel, errRead)
		}
		lower := strings.ToLower(string(data))
		for _, forbidden := range []string{"adaptive", "retrier", "nexttier", "pretruncate"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s contains omitted adaptive retry artifact %q", rel, forbidden)
			}
		}
	}
	if _, errStat := os.Stat(filepath.Join(repoRoot, "internal/runtime/executor/helps/opencode_go_adaptive.go")); !os.IsNotExist(errStat) {
		t.Fatalf("opencode_go_adaptive.go exists or stat failed unexpectedly: %v", errStat)
	}
}

func performOpenCodeGoReferralRequest(t *testing.T, h *Handler, method, target string, mutate func(*http.Request) *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.GET("/referral/:workspace", h.GetOpenCodeGoReferralSummary)
	router.GET("/referral/:workspace/rewards/:reward/preview", h.GetOpenCodeGoReferralRewardPreview)
	router.POST("/referral/:workspace/rewards/:reward/apply", h.PostOpenCodeGoReferralRewardApply)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	if mutate != nil {
		req = mutate(req)
	}
	router.ServeHTTP(rec, req)
	return rec
}

func assertOpenCodeGoReferralRequest(t *testing.T, req *http.Request, method, hashID string, wantArgs []string) {
	t.Helper()
	if req.Method != method {
		t.Fatalf("method = %s, want %s", req.Method, method)
	}
	if req.URL.Path != "/_server" {
		t.Fatalf("path = %q, want /_server", req.URL.Path)
	}
	if req.Header.Get("x-server-id") != hashID {
		t.Fatalf("x-server-id = %q, want %q", req.Header.Get("x-server-id"), hashID)
	}
	var payload string
	if method == http.MethodGet {
		if req.URL.Query().Get("id") != hashID {
			t.Fatalf("query id = %q, want %q", req.URL.Query().Get("id"), hashID)
		}
		payload = req.URL.Query().Get("args")
	} else {
		data, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		payload = string(data)
	}
	args := decodeOpenCodeGoReferralArgs(t, payload)
	if strings.Join(args, "|") != strings.Join(wantArgs, "|") {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
}

func decodeOpenCodeGoReferralArgs(t *testing.T, payload string) []string {
	t.Helper()
	var decoded struct {
		T struct {
			L int `json:"l"`
			A []struct {
				T int    `json:"t"`
				S string `json:"s"`
			} `json:"a"`
		} `json:"t"`
	}
	if errUnmarshal := json.Unmarshal([]byte(payload), &decoded); errUnmarshal != nil {
		t.Fatalf("payload is not JSON: %v; payload=%s", errUnmarshal, payload)
	}
	if decoded.T.L != len(decoded.T.A) {
		t.Fatalf("payload length = %d, args = %d", decoded.T.L, len(decoded.T.A))
	}
	out := make([]string, 0, len(decoded.T.A))
	for _, arg := range decoded.T.A {
		if arg.T != 1 {
			t.Fatalf("arg type = %d, want 1", arg.T)
		}
		out = append(out, arg.S)
	}
	return out
}
