package auth

import "testing"

func TestIsConfigAPIKeyAuth(t *testing.T) {
	if IsConfigAPIKeyAuth(nil) {
		t.Fatal("expected nil auth to be false")
	}
	if IsConfigAPIKeyAuth(&Auth{Attributes: map[string]string{"source": "config:codex[x]"}}) {
		t.Fatal("expected missing auth_kind and api_key to be false")
	}
	if IsConfigAPIKeyAuth(&Auth{
		ID:       "codex:oauth:abc",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "k",
			"source":    "config:codex[abc]",
		},
	}) {
		t.Fatal("expected explicit oauth auth to be false")
	}
	if !IsConfigAPIKeyAuth(&Auth{
		ID:       "codex:apikey:abc",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"source":    "config:codex[abc]",
		},
	}) {
		t.Fatal("expected empty api_key with auth_kind=apikey and config source to be true")
	}
	if !IsConfigAPIKeyAuth(&Auth{
		ID:       "codex:apikey:abc",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "k",
			"source":  "config:codex[abc]",
		},
	}) {
		t.Fatal("expected config api key auth")
	}
	if !IsConfigAPIKeyAuth(&Auth{
		ID:       "opencode-go:openai:abc",
		Provider: "opencode-go",
		Attributes: map[string]string{
			"api_key":        "k",
			"auth_kind":      AuthKindAPIKey,
			"runtime_only":   "true",
			"source_backend": AuthSourceConfig,
		},
	}) {
		t.Fatal("expected runtime-only config api key auth")
	}
}
