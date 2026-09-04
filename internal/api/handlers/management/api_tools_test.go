package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallUsesRequestProxyURL(t *testing.T) {
	t.Parallel()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxyServer.Close()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:1"},
		},
	}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"method":"GET","url":"http://upstream.invalid/test","proxy_url":"` + proxyServer.URL + `"}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status code = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Body != "proxied" {
		t.Fatalf("upstream body = %q, want %q", response.Body, "proxied")
	}
}

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportRequestProxyOverridesCredentialAndGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, " http://request-proxy.example.com:8080 ")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://request-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportInvalidRequestProxyDoesNotFallBack(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, "bad-value")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected invalid request proxy to avoid lower-priority proxy settings")
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			XAIKey: []config.XAIKey{{
				APIKey:   "xai-key",
				ProxyURL: "http://xai-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "xai",
			auth: &coreauth.Auth{
				Provider:   "xai",
				Attributes: map[string]string{"api_key": "xai-key"},
			},
			wantProxy: "http://xai-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth, "")
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestAPICallQuotaHubSynchronizesActiveProviders(t *testing.T) {
	testDate := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name            string
		provider        string
		url             string
		body            string
		wantRecoverAt   time.Time
		wantScore       float64
		wantMethod      string
		wantRequestData string
	}{
		{
			name:          "codex",
			provider:      "codex",
			url:           "https://chatgpt.com/backend-api/wham/usage",
			body:          `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_after_seconds":3600}}}`,
			wantRecoverAt: testDate.Add(time.Hour),
			wantScore:     0,
			wantMethod:    http.MethodGet,
		},
		{
			name:          "claude",
			provider:      "claude",
			url:           "https://api.anthropic.com/api/oauth/usage",
			body:          `{"five_hour":{"utilization":100,"resets_at":"` + testDate.Add(2*time.Hour).Format(time.RFC3339) + `"},"seven_day":{"utilization":20}}`,
			wantRecoverAt: testDate.Add(2 * time.Hour),
			wantScore:     0,
			wantMethod:    http.MethodGet,
		},
		{
			name:       "kimi",
			provider:   "kimi",
			url:        "https://api.kimi.com/coding/v1/usages",
			body:       `{"usage":{"limit":100,"used":100,"remaining":0,"resetIn":3600}}`,
			wantScore:  0,
			wantMethod: http.MethodGet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			resolved := registerAPICallTestAuth(t, manager, "api-call-"+tt.name, tt.provider)
			h := &Handler{authManager: manager}

			installAPICallTestTransport(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != tt.wantMethod || req.URL.String() != tt.url {
					t.Fatalf("upstream request = %s %s, want %s %s", req.Method, req.URL, tt.wantMethod, tt.url)
				}
				if got := req.Header.Get("Authorization"); got != "Bearer resolved-token" {
					t.Fatalf("Authorization = %q, want resolved token", got)
				}
				var requestData []byte
				if req.Body != nil {
					var errRead error
					requestData, errRead = io.ReadAll(req.Body)
					if errRead != nil {
						t.Fatalf("read upstream request: %v", errRead)
					}
				}
				if string(requestData) != tt.wantRequestData {
					t.Fatalf("upstream body = %q, want %q", requestData, tt.wantRequestData)
				}
				return newAPICallTestResponse(req, http.StatusOK, http.Header{
					"Date":         []string{testDate.Format(http.TimeFormat)},
					"X-Quota-Test": []string{"preserved"},
				}, tt.body), nil
			})

			response := performAPICallTestRequest(t, context.Background(), h, apiCallRequest{
				AuthIndexSnake: stringPointer(resolved.EnsureIndex()),
				Method:         tt.wantMethod,
				URL:            tt.url,
				Header:         map[string]string{"Authorization": "Bearer $TOKEN$"},
				Data:           tt.wantRequestData,
			})

			assertAPICallTestEnvelope(t, response, http.StatusOK, tt.body, testDate.Format(http.TimeFormat), "preserved")
			score, ok := manager.QuotaScore(resolved.ID)
			if !ok || score != tt.wantScore {
				t.Fatalf("quota score = %v, %v; want %v, true", score, ok, tt.wantScore)
			}
			current, ok := manager.GetByID(resolved.ID)
			if !ok || !current.Unavailable || !current.Quota.Exceeded || current.Quota.Reason != "quota_hub" {
				t.Fatalf("synchronized auth = %+v, want QuotaHub cooldown", current)
			}
			if !tt.wantRecoverAt.IsZero() && !current.Quota.NextRecoverAt.Equal(tt.wantRecoverAt) {
				t.Fatalf("recover at = %v, want Date-anchored %v", current.Quota.NextRecoverAt, tt.wantRecoverAt)
			}
		})
	}
}

func TestAPICallQuotaHubRejectsUnmatchedAndInvalidObservations(t *testing.T) {
	validCodexBody := `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`
	oversizedBody := strings.Repeat(" ", (1<<20)+1)
	tests := []struct {
		name           string
		provider       string
		method         string
		url            string
		statusCode     int
		body           string
		wantTicketUsed bool
	}{
		{name: "wrong endpoint", provider: "codex", method: http.MethodGet, url: "https://chatgpt.com/backend-api/wham/usage/", statusCode: http.StatusOK, body: validCodexBody},
		{name: "wrong provider", provider: "gemini", method: http.MethodGet, url: "https://chatgpt.com/backend-api/wham/usage", statusCode: http.StatusOK, body: validCodexBody},
		{name: "wrong status", provider: "codex", method: http.MethodGet, url: "https://chatgpt.com/backend-api/wham/usage", statusCode: http.StatusTooManyRequests, body: validCodexBody, wantTicketUsed: true},
		{name: "wrong schema", provider: "codex", method: http.MethodGet, url: "https://chatgpt.com/backend-api/wham/usage", statusCode: http.StatusOK, body: `{"rate_limit":"invalid"}`, wantTicketUsed: true},
		{name: "oversized body", provider: "codex", method: http.MethodGet, url: "https://chatgpt.com/backend-api/wham/usage", statusCode: http.StatusOK, body: oversizedBody, wantTicketUsed: true},
		{name: "antigravity real shape", provider: "antigravity", method: http.MethodPost, url: "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", statusCode: http.StatusOK, body: `{"buckets":[{"remainingFraction":0.5}]}`},
		{name: "xai real shape", provider: "xai", method: http.MethodGet, url: "https://cli-chat-proxy.grok.com/v1/billing?format=credits", statusCode: http.StatusOK, body: `{"config":{"usage":{"remaining":10}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			resolved := registerAPICallTestAuth(t, manager, "api-call-noop-"+strings.ReplaceAll(tt.name, " ", "-"), tt.provider)
			h := &Handler{authManager: manager}
			responseHeader := http.Header{"Date": []string{"Mon, 02 Jan 2006 15:04:05 GMT"}, "X-Noop-Test": []string{"preserved"}}
			installAPICallTestTransport(t, func(req *http.Request) (*http.Response, error) {
				return newAPICallTestResponse(req, tt.statusCode, responseHeader, tt.body), nil
			})

			response := performAPICallTestRequest(t, context.Background(), h, apiCallRequest{
				AuthIndexSnake: stringPointer(resolved.EnsureIndex()),
				Method:         tt.method,
				URL:            tt.url,
				Data:           `{"project":"test-project"}`,
			})
			assertAPICallTestEnvelope(t, response, tt.statusCode, tt.body, responseHeader.Get("Date"), "")
			if _, ok := manager.QuotaScore(resolved.ID); ok {
				t.Fatal("no-op response unexpectedly set a quota score")
			}
			current, ok := manager.GetByID(resolved.ID)
			if !ok || current.Unavailable || current.Quota.Exceeded {
				t.Fatalf("no-op response changed auth = %+v", current)
			}
			ticket, issued := manager.IssueQuotaObservationTicketForAuth(resolved)
			if !issued {
				t.Fatal("failed to issue test observation ticket")
			}
			wantStartOrder := uint64(1)
			if tt.wantTicketUsed {
				wantStartOrder = 2
			}
			if ticket.StartOrder != wantStartOrder {
				t.Fatalf("next ticket start order = %d, want %d", ticket.StartOrder, wantStartOrder)
			}
		})
	}
}

func TestAPICallQuotaHubSpoofedAuthIndexCannotMutateRegisteredAuth(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	resolved := registerAPICallTestAuth(t, manager, "codex-auth-id-is-not-an-index", "codex")
	h := &Handler{authManager: manager}
	body := `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_after_seconds":3600}}}`
	installAPICallTestTransport(t, func(req *http.Request) (*http.Response, error) {
		return newAPICallTestResponse(req, http.StatusOK, nil, body), nil
	})

	response := performAPICallTestRequest(t, context.Background(), h, apiCallRequest{
		AuthIndexSnake: stringPointer(resolved.ID),
		Method:         http.MethodGet,
		URL:            "https://chatgpt.com/backend-api/wham/usage",
	})
	assertAPICallTestEnvelope(t, response, http.StatusOK, body, "", "")
	if _, ok := manager.QuotaScore(resolved.ID); ok {
		t.Fatal("spoofed auth_index set a quota score")
	}
	current, ok := manager.GetByID(resolved.ID)
	if !ok || current.Unavailable || current.Quota.Exceeded {
		t.Fatalf("spoofed auth_index changed registered auth = %+v", current)
	}
	ticket, issued := manager.IssueQuotaObservationTicketForAuth(resolved)
	if !issued || ticket.StartOrder != 1 {
		t.Fatalf("ticket after spoof = %+v, %v; want first ticket", ticket, issued)
	}
}

func TestAPICallQuotaHubBeginsBeforeUpstreamRequest(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	resolved := registerAPICallTestAuth(t, manager, "api-call-causal-codex", "codex")
	h := &Handler{authManager: manager}
	body := `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`
	installAPICallTestTransport(t, func(req *http.Request) (*http.Response, error) {
		manager.MarkResult(context.Background(), coreauth.Result{
			AuthID:   resolved.ID,
			Provider: resolved.Provider,
			Error:    &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "runtime quota"},
		})
		return newAPICallTestResponse(req, http.StatusOK, nil, body), nil
	})

	response := performAPICallTestRequest(t, context.Background(), h, apiCallRequest{
		AuthIndexSnake: stringPointer(resolved.EnsureIndex()),
		Method:         http.MethodGet,
		URL:            "https://chatgpt.com/backend-api/wham/usage",
	})
	assertAPICallTestEnvelope(t, response, http.StatusOK, body, "", "")
	if _, ok := manager.QuotaScore(resolved.ID); ok {
		t.Fatal("stale manual completion set quota score after runtime 429")
	}
	current, ok := manager.GetByID(resolved.ID)
	if !ok || !current.Unavailable || !current.Quota.Exceeded || current.Quota.Reason == "quota_hub" {
		t.Fatalf("runtime 429 state = %+v, want preserved non-Hub cooldown", current)
	}
}

func TestAPICallQuotaHubCompletionFailuresPreserveResponse(t *testing.T) {
	t.Run("adapter error", func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		resolved := registerAPICallTestAuth(t, manager, "api-call-adapter-error", "codex")
		h := &Handler{authManager: manager}
		body := `{"rate_limit":"malformed"}`
		installAPICallTestTransport(t, func(req *http.Request) (*http.Response, error) {
			return newAPICallTestResponse(req, http.StatusAccepted, http.Header{"X-Adapter-Test": []string{"preserved"}}, body), nil
		})

		response := performAPICallTestRequest(t, context.Background(), h, apiCallRequest{
			AuthIndexSnake: stringPointer(resolved.EnsureIndex()),
			Method:         http.MethodGet,
			URL:            "https://chatgpt.com/backend-api/wham/usage",
		})
		assertAPICallTestEnvelope(t, response, http.StatusAccepted, body, "", "")
		if http.Header(response.Header).Get("X-Adapter-Test") != "preserved" {
			t.Fatalf("adapter failure response header = %q", http.Header(response.Header).Get("X-Adapter-Test"))
		}
	})

	t.Run("completion panic", func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		resolved := registerAPICallTestAuth(t, manager, "api-call-completion-panic", "kimi")
		manager.SetCooldownStateStore(panicAPICallCooldownStore{})
		h := &Handler{authManager: manager}
		body := `{"usage":{"limit":100,"used":100,"remaining":0,"resetIn":3600}}`
		installAPICallTestTransport(t, func(req *http.Request) (*http.Response, error) {
			return newAPICallTestResponse(req, http.StatusOK, http.Header{"X-Panic-Test": []string{"preserved"}}, body), nil
		})

		response := performAPICallTestRequest(t, context.Background(), h, apiCallRequest{
			AuthIndexSnake: stringPointer(resolved.EnsureIndex()),
			Method:         http.MethodGet,
			URL:            "https://api.kimi.com/coding/v1/usages",
		})
		assertAPICallTestEnvelope(t, response, http.StatusOK, body, "", "")
		if http.Header(response.Header).Get("X-Panic-Test") != "preserved" {
			t.Fatalf("completion panic response header = %q", http.Header(response.Header).Get("X-Panic-Test"))
		}
	})
}

func TestAPICallQuotaHubCanceledContextStillPersistsValues(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	resolved := registerAPICallTestAuth(t, manager, "api-call-canceled-context", "kimi")
	type contextKey struct{}
	key := contextKey{}
	store := &recordingAPICallCooldownStore{contextKey: key}
	manager.SetCooldownStateStore(store)
	h := &Handler{authManager: manager}
	body := `{"usage":{"limit":100,"used":100,"remaining":0,"resetIn":3600}}`
	base := context.WithValue(context.Background(), contextKey{}, "preserved-value")
	requestContext, cancel := context.WithCancel(base)
	installAPICallTestTransport(t, func(req *http.Request) (*http.Response, error) {
		response := newAPICallTestResponse(req, http.StatusOK, nil, body)
		response.Body = &cancelingAPICallBody{reader: strings.NewReader(body), cancel: cancel}
		return response, nil
	})

	response := performAPICallTestRequest(t, requestContext, h, apiCallRequest{
		AuthIndexSnake: stringPointer(resolved.EnsureIndex()),
		Method:         http.MethodGet,
		URL:            "https://api.kimi.com/coding/v1/usages",
	})
	assertAPICallTestEnvelope(t, response, http.StatusOK, body, "", "")
	saveCount, contextErr, contextValue, hadDeadline := store.snapshot()
	if saveCount != 1 || contextErr != nil || contextValue != "preserved-value" || hadDeadline {
		t.Fatalf("cooldown save = count %d, err %v, value %v, deadline %v", saveCount, contextErr, contextValue, hadDeadline)
	}
}

type apiCallTestRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip apiCallTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return roundTrip(req)
}

func installAPICallTestTransport(t *testing.T, roundTrip apiCallTestRoundTripper) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = roundTrip
	t.Cleanup(func() {
		http.DefaultTransport = previous
	})
}

func registerAPICallTestAuth(t *testing.T, manager *coreauth.Manager, id, provider string) *coreauth.Auth {
	t.Helper()
	resolved, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       id,
		Provider: provider,
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "resolved-token"},
	})
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	if resolved == nil {
		t.Fatal("register auth returned nil")
	}
	return resolved
}

func performAPICallTestRequest(t *testing.T, ctx context.Context, h *Handler, request apiCallRequest) apiCallResponse {
	t.Helper()
	requestBody, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatalf("marshal APICall request: %v", errMarshal)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(requestBody)).WithContext(ctx)
	httpRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httpRequest
	h.APICall(ginContext)
	if recorder.Code != http.StatusOK {
		t.Fatalf("management status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var response apiCallResponse
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode APICall response: %v", errDecode)
	}
	return response
}

func newAPICallTestResponse(req *http.Request, statusCode int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     header.Clone(),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func assertAPICallTestEnvelope(t *testing.T, response apiCallResponse, statusCode int, body, date, quotaHeader string) {
	t.Helper()
	if response.StatusCode != statusCode || response.Body != body {
		t.Fatalf("APICall response = status %d body %q, want status %d body %q", response.StatusCode, response.Body, statusCode, body)
	}
	if got := http.Header(response.Header).Get("Date"); got != date {
		t.Fatalf("APICall Date = %q, want %q", got, date)
	}
	if quotaHeader != "" && http.Header(response.Header).Get("X-Quota-Test") != quotaHeader {
		t.Fatalf("APICall X-Quota-Test = %q, want %q", http.Header(response.Header).Get("X-Quota-Test"), quotaHeader)
	}
}

func stringPointer(value string) *string {
	return &value
}

type panicAPICallCooldownStore struct{}

func (panicAPICallCooldownStore) Load(context.Context) ([]coreauth.CooldownStateRecord, error) {
	return nil, nil
}

func (panicAPICallCooldownStore) Save(context.Context, []coreauth.CooldownStateRecord) error {
	panic("test cooldown store panic")
}

type cancelingAPICallBody struct {
	reader *strings.Reader
	cancel context.CancelFunc
	once   sync.Once
}

func (body *cancelingAPICallBody) Read(buffer []byte) (int, error) {
	read, err := body.reader.Read(buffer)
	body.once.Do(body.cancel)
	return read, err
}

func (*cancelingAPICallBody) Close() error {
	return nil
}

type recordingAPICallCooldownStore struct {
	mu           sync.Mutex
	saveCount    int
	contextErr   error
	contextValue any
	hadDeadline  bool
	contextKey   any
}

func (*recordingAPICallCooldownStore) Load(context.Context) ([]coreauth.CooldownStateRecord, error) {
	return nil, nil
}

func (store *recordingAPICallCooldownStore) Save(ctx context.Context, _ []coreauth.CooldownStateRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saveCount++
	store.contextErr = ctx.Err()
	store.contextValue = ctx.Value(store.contextKey)
	_, store.hadDeadline = ctx.Deadline()
	return nil
}

func (store *recordingAPICallCooldownStore) snapshot() (int, error, any, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saveCount, store.contextErr, store.contextValue, store.hadDeadline
}
