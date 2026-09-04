package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetOpenAICompatIncludesDisableCooling(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	requestRetry := 0
	disableCooling := true
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "test-key"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "mimo-v2.5", Alias: ""},
				},
				SupportPromptCacheKey: true,
				DisableCooling:        &disableCooling,
				RequestRetry:          &requestRetry,
			},
		},
	}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body struct {
		OpenAICompatibility []struct {
			SupportPromptCacheKey *bool `json:"support-prompt-cache-key"`
			DisableCooling        *bool `json:"disable-cooling"`
			RequestRetry          *int  `json:"request-retry"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body.OpenAICompatibility) != 1 {
		t.Fatalf("expected 1 openai-compatibility entry, got %d", len(body.OpenAICompatibility))
	}
	if body.OpenAICompatibility[0].SupportPromptCacheKey == nil || !*body.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatalf("expected support-prompt-cache-key to be present and true, got %#v", body.OpenAICompatibility[0].SupportPromptCacheKey)
	}
	if body.OpenAICompatibility[0].DisableCooling == nil || !*body.OpenAICompatibility[0].DisableCooling {
		t.Fatalf("expected disable-cooling to be present and true, got %#v", body.OpenAICompatibility[0].DisableCooling)
	}
	if body.OpenAICompatibility[0].RequestRetry == nil || *body.OpenAICompatibility[0].RequestRetry != 0 {
		t.Fatalf("expected request-retry to be present and 0, got %#v", body.OpenAICompatibility[0].RequestRetry)
	}
}

func TestPutOpenAICompatPreservesPromptCacheKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
	body := []byte(`[
		{
			"name": "compat-a",
			"base-url": "https://compat-a.example.com/v1",
			"support-prompt-cache-key": true,
			"api-key-entries": [{"api-key": "key-a"}],
			"models": [{"name": "upstream-a", "alias": "alias-a"}]
		},
		{
			"name": "compat-b",
			"base-url": "https://compat-b.example.com/v1",
			"support-prompt-cache-key": false,
			"api-key-entries": [{"api-key": "key-b"}]
		}
	]`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if len(cfg.OpenAICompatibility) != 2 {
		t.Fatalf("openai-compatibility count = %d, want 2", len(cfg.OpenAICompatibility))
	}
	if !cfg.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatal("PUT lost support-prompt-cache-key=true")
	}
	if cfg.OpenAICompatibility[1].SupportPromptCacheKey {
		t.Fatal("PUT changed explicit support-prompt-cache-key=false")
	}
}

func TestPatchOpenAICompatPromptCacheKeyByIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "duplicate", BaseURL: "https://first.example.com/v1"},
			{Name: "duplicate", BaseURL: "https://second.example.com/v1"},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
	body := []byte(`{"index":1,"value":{"support-prompt-cache-key":true}}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if cfg.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatal("PATCH by index updated the wrong duplicate provider")
	}
	if !cfg.OpenAICompatibility[1].SupportPromptCacheKey {
		t.Fatal("PATCH by index did not update support-prompt-cache-key")
	}
}

func TestGetOpenAICompatAuthIndexDuplicateNames(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:                  "duplicate",
				BaseURL:               "https://duplicate.example.com/v1",
				SupportPromptCacheKey: true,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "key-first"},
				},
			},
			{
				Name:    "duplicate",
				BaseURL: "https://duplicate.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "key-second"},
				},
			},
		},
	}
	synth := synthesizer.NewConfigSynthesizer()
	auths, errSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynth != nil {
		t.Fatalf("Synthesize() error = %v", errSynth)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	wantIndexByKey := map[string]string{}
	for _, auth := range auths {
		registered, errRegister := manager.Register(context.Background(), auth)
		if errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		wantIndexByKey[registered.Attributes["api_key"]] = registered.EnsureIndex()
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)

	h.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var body struct {
		OpenAICompatibility []struct {
			SupportPromptCacheKey bool `json:"support-prompt-cache-key"`
			APIKeyEntries         []struct {
				APIKey    string `json:"api-key"`
				AuthIndex string `json:"auth-index"`
			} `json:"api-key-entries"`
		} `json:"openai-compatibility"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(body.OpenAICompatibility) != 2 {
		t.Fatalf("openai-compatibility count = %d, want 2", len(body.OpenAICompatibility))
	}
	for entryIndex, entry := range body.OpenAICompatibility {
		if len(entry.APIKeyEntries) != 1 {
			t.Fatalf("entry %d api-key-entries count = %d, want 1", entryIndex, len(entry.APIKeyEntries))
		}
		keyEntry := entry.APIKeyEntries[0]
		if got, want := keyEntry.AuthIndex, wantIndexByKey[keyEntry.APIKey]; got == "" || got != want {
			t.Fatalf("entry %d key %q auth-index = %q, want %q", entryIndex, keyEntry.APIKey, got, want)
		}
	}
	if !body.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatal("GET lost support-prompt-cache-key=true for first duplicate")
	}
	if body.OpenAICompatibility[1].SupportPromptCacheKey {
		t.Fatal("GET changed missing support-prompt-cache-key to true for second duplicate")
	}
}
