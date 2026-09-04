package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexHTTPHeadersDefaultCloakingOverridesCustomIdentity(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"header:User-Agent": "custom-ua",
			"header:Originator": "custom-origin",
			"header:X-Custom":   "custom-value",
		},
	}

	applyCodexHeadersFromSources(req, auth, "oauth-token", true, nil, nil)

	if got := req.Header.Get("User-Agent"); got != codexUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, codexUserAgent)
	}
	if got := req.Header.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %q, want %q", got, codexOriginator)
	}
	if got := req.Header.Get("X-Custom"); got != "custom-value" {
		t.Fatalf("X-Custom = %q, want custom-value", got)
	}
}

func TestCodexHTTPHeadersDisabledCloakingPreservesCustomIdentity(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{Codex: config.CodexConfig{DisableCodexCloaking: true}}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"header:User-Agent": "custom-ua",
			"header:Originator": "custom-origin",
			"header:X-Custom":   "custom-value",
		},
	}

	applyCodexHeadersFromSources(req, auth, "oauth-token", true, cfg, nil)

	if got := req.Header.Get("User-Agent"); got != "custom-ua" {
		t.Fatalf("User-Agent = %q, want custom-ua", got)
	}
	if got := req.Header.Get("Originator"); got != "custom-origin" {
		t.Fatalf("Originator = %q, want custom-origin", got)
	}
	if got := req.Header.Get("X-Custom"); got != "custom-value" {
		t.Fatalf("X-Custom = %q, want custom-value", got)
	}
}

func TestCodexHTTPHeadersNilRequestHeaderDoesNotPanic(t *testing.T) {
	applyCodexCloakingHeaders(nil, nil)
}
