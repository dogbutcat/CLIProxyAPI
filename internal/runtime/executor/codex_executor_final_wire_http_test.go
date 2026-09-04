package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const codexHTTPFinalWirePayload = `{
	"model":"gpt-5.4",
	"prompt_cache_key":"cache-http-final",
	"client_metadata":{
		"x-codex-installation-id":"install-http-final",
		"x-codex-turn-metadata":"{\"prompt_cache_key\":\"cache-http-final\",\"turn_id\":\"turn-http-final\",\"window_id\":\"cache-http-final:0\"}",
		"x-codex-window-id":"cache-http-final:0"
	},
	"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"policy"}]}],
	"tools":[{"type":"web_search_preview"}],
	"tool_choice":{"type":"web_search_preview"},
	"temperature":1,
	"top_p":1,
	"max_output_tokens":1,
	"max_completion_tokens":1,
	"truncation":"auto",
	"user":"payload-user",
	"context_management":{"type":"evil"},
	"service_tier":"default",
	"include":["bad"],
	"store":true,
	"stream":false,
	"parallel_tool_calls":false
}`

func TestCodexExecutorFinalWireConstraintsHTTPExecute(t *testing.T) {
	var gotBody []byte
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexHTTPFinalWireConfig())
	_, err := executor.Execute(context.Background(), codexHTTPFinalWireAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(codexHTTPFinalWirePayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("upstream path = %q, want /responses", gotPath)
	}
	assertCodexHTTPFinalWireBody(t, gotBody)
}

func TestCodexExecutorFinalWireConstraintsHTTPStream(t *testing.T) {
	var gotBody []byte
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexHTTPFinalWireConfig())
	result, err := executor.ExecuteStream(context.Background(), codexHTTPFinalWireAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(codexHTTPFinalWirePayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	if gotPath != "/responses" {
		t.Fatalf("upstream path = %q, want /responses", gotPath)
	}
	assertCodexHTTPFinalWireBody(t, gotBody)
}

func codexHTTPFinalWireConfig() *config.Config {
	return &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Routing:   config.RoutingConfig{Strategy: "fill-first"},
		Codex:     config.CodexConfig{IdentityConfuse: true},
		Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{
				Name:         "gpt-5.4",
				Protocol:     "codex",
				FromProtocol: "openai-response",
			}},
			Params: map[string]any{
				"temperature":              0.7,
				"top_p":                    0.8,
				"max_output_tokens":        4096,
				"max_completion_tokens":    2048,
				"truncation":               "auto",
				"user":                     "payload-rule-user",
				"context_management":       map[string]any{"type": "evil"},
				"store":                    true,
				"stream":                   false,
				"parallel_tool_calls":      false,
				"include":                  []any{"payload.rule.include"},
				"service_tier":             "default",
				"tools.0.type":             "web_search_preview_2025_03_11",
				"tool_choice.type":         "web_search_preview_2025_03_11",
				"client_metadata.injected": "after-translation",
			},
		}}},
	}
}

func codexHTTPFinalWireAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "auth-http-final",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": serverURL,
			"api_key":  "test",
		},
	}
}

func assertCodexHTTPFinalWireBody(t *testing.T, body []byte) {
	t.Helper()

	if !json.Valid(body) {
		t.Fatalf("body is not valid JSON: %s", string(body))
	}
	if got := gjson.GetBytes(body, "store"); got.Type != gjson.False {
		t.Fatalf("store = %s, want false; body=%s", got.Raw, string(body))
	}
	if got := gjson.GetBytes(body, "stream"); got.Type != gjson.True {
		t.Fatalf("stream = %s, want true; body=%s", got.Raw, string(body))
	}
	if got := gjson.GetBytes(body, "parallel_tool_calls"); got.Type != gjson.True {
		t.Fatalf("parallel_tool_calls = %s, want true; body=%s", got.Raw, string(body))
	}
	include := gjson.GetBytes(body, "include").Array()
	if len(include) != 1 || include[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want reasoning.encrypted_content only; body=%s", gjson.GetBytes(body, "include").Raw, string(body))
	}
	for _, field := range []string{
		"max_output_tokens",
		"max_completion_tokens",
		"temperature",
		"top_p",
		"truncation",
		"user",
		"context_management",
		"service_tier",
	} {
		if gjson.GetBytes(body, field).Exists() {
			t.Fatalf("rejected field %q survived; body=%s", field, string(body))
		}
	}
	if got := gjson.GetBytes(body, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want web_search; body=%s", got, string(body))
	}
	if got := gjson.GetBytes(body, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want developer; body=%s", got, string(body))
	}
	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-http-final", "prompt-cache", "cache-http-final")
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != expectedPromptCacheKey {
		t.Fatalf("prompt_cache_key = %q, want confused key %q; body=%s", got, expectedPromptCacheKey, string(body))
	}
	expectedInstallationID := codexIdentityConfuseUUID("auth-http-final", "installation", "install-http-final")
	if got := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); got != expectedInstallationID {
		t.Fatalf("installation id = %q, want confused id %q; body=%s", got, expectedInstallationID, string(body))
	}
	if got := gjson.GetBytes(body, "client_metadata.injected").String(); got != "after-translation" {
		t.Fatalf("payload override marker = %q, want after-translation; body=%s", got, string(body))
	}
}
