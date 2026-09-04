package synthesizer

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestConfigSynthesizer_OpenCodeGoKeyGroups(t *testing.T) {
	const apiKey = "ocg-secret-key"
	const authCookie = "session-secret"

	auths := synthesizeOpenCodeGoTestAuths(t, openCodeGoTestConfig(
		[]config.OpenCodeGoModelEntry{{Name: "gpt-upstream", Alias: "gpt-visible"}},
		[]config.OpenCodeGoKeyEntry{{KeyName: "acct-a", APIKey: apiKey, ProxyURL: "http://proxy.local:8080", WorkspaceID: "ws-123", AuthCookie: authCookie}},
	))
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1 per key", len(auths))
	}

	auth := auths[0]
	if auth.Provider != "opencode-go" {
		t.Fatalf("provider = %q, want opencode-go", auth.Provider)
	}
	if auth.Label != "acct-a" || auth.Label == apiKey || auth.Label == authCookie {
		t.Fatalf("unsafe or unexpected label %q", auth.Label)
	}
	if auth.Attributes["source"] == apiKey || auth.Attributes["source"] == authCookie {
		t.Fatalf("secret appeared in source %q", auth.Attributes["source"])
	}
	if auth.Attributes[coreauth.AttributeSourceBackend] != coreauth.AuthSourceConfig {
		t.Fatalf("source backend = %q, want config", auth.Attributes[coreauth.AttributeSourceBackend])
	}
	if auth.Attributes[coreauth.AttributeAuthKind] != coreauth.AuthKindAPIKey {
		t.Fatalf("auth kind = %q, want apikey", auth.Attributes[coreauth.AttributeAuthKind])
	}
	if auth.Attributes[coreauth.AttributeRuntimeOnly] != "true" {
		t.Fatalf("runtime_only = %q, want true", auth.Attributes[coreauth.AttributeRuntimeOnly])
	}
	if auth.Attributes[coreauth.AttributeAPIKey] != apiKey || auth.Attributes["auth_cookie"] != authCookie {
		t.Fatalf("credential attributes not propagated: %#v", auth.Attributes)
	}
	if auth.Attributes["workspace_id"] != "ws-123" || auth.ProxyURL != "http://proxy.local:8080" {
		t.Fatalf("identity/proxy metadata not propagated: %#v proxy=%q", auth.Attributes, auth.ProxyURL)
	}
	if auth.Attributes["protocols"] != "openai,anthropic" || auth.Attributes["protocol"] != "" || auth.Attributes["base_url"] != "" || auth.Prefix != "" {
		t.Fatalf("canonical key auth leaked route-specific identity: %#v prefix=%q", auth.Attributes, auth.Prefix)
	}
	aliases := coreauth.OAuthModelAliasesFromAttributes(auth.Attributes)
	if len(aliases) != 2 || aliases[0].Name != "gpt-upstream" || aliases[0].Alias != "gpt-visible" || aliases[1].Name != "claude-upstream" || aliases[1].Alias != "claude-visible" {
		t.Fatalf("combined model aliases = %#v", aliases)
	}
	priorities := map[string]int{}
	if errUnmarshal := json.Unmarshal([]byte(auth.Attributes[coreauth.AttributeModelPriorities]), &priorities); errUnmarshal != nil {
		t.Fatalf("model priorities: %v", errUnmarshal)
	}
	if priorities["gpt-visible"] != 7 || priorities["og/gpt-visible"] != 7 || priorities["claude-visible"] != 3 || priorities["oc/claude-visible"] != 3 {
		t.Fatalf("model priorities = %#v", priorities)
	}
}

func TestConfigSynthesizer_OpenCodeGoStableDiffSurface(t *testing.T) {
	base := openCodeGoTestConfig([]config.OpenCodeGoModelEntry{{Name: "gpt-upstream", Alias: "gpt-visible"}}, []config.OpenCodeGoKeyEntry{{KeyName: "acct-a", APIKey: "secret-a"}})
	modelChanged := openCodeGoTestConfig([]config.OpenCodeGoModelEntry{{Name: "gpt-upstream", Alias: "gpt-reloaded"}}, []config.OpenCodeGoKeyEntry{{KeyName: "acct-a", APIKey: "secret-a"}})
	keyChanged := openCodeGoTestConfig([]config.OpenCodeGoModelEntry{{Name: "gpt-upstream", Alias: "gpt-visible"}}, []config.OpenCodeGoKeyEntry{{KeyName: "acct-a", APIKey: "secret-b"}})

	baseAuths := synthesizeOpenCodeGoTestAuths(t, base)
	modelAuths := synthesizeOpenCodeGoTestAuths(t, modelChanged)
	keyChangedAuths := synthesizeOpenCodeGoTestAuths(t, keyChanged)

	if baseAuths[0].ID != modelAuths[0].ID {
		t.Fatalf("model-only change should keep auth ID stable: %q / %q", baseAuths[0].ID, modelAuths[0].ID)
	}
	if baseAuths[0].Attributes["models_hash"] == modelAuths[0].Attributes["models_hash"] {
		t.Fatalf("model alias change should alter diff hash: %q", baseAuths[0].Attributes["models_hash"])
	}
	if baseAuths[0].ID != keyChangedAuths[0].ID {
		t.Fatalf("API key rotation should keep safe identity stable: %q / %q", baseAuths[0].ID, keyChangedAuths[0].ID)
	}
	if baseAuths[0].Attributes[coreauth.AttributeAPIKey] == keyChangedAuths[0].Attributes[coreauth.AttributeAPIKey] {
		t.Fatal("API key rotation should alter credential attributes")
	}
}

func TestConfigSynthesizer_OpenCodeGoTenKeysRemainTenAuths(t *testing.T) {
	keys := make([]config.OpenCodeGoKeyEntry, 0, 10)
	for i := 0; i < 10; i++ {
		keys = append(keys, config.OpenCodeGoKeyEntry{
			KeyName: fmt.Sprintf("acct-%02d", i),
			APIKey:  fmt.Sprintf("secret-%02d", i),
		})
	}

	auths := synthesizeOpenCodeGoTestAuths(t, openCodeGoTestConfig(
		[]config.OpenCodeGoModelEntry{{Name: "gpt-upstream", Alias: "gpt-visible"}},
		keys,
	))
	if len(auths) != len(keys) {
		t.Fatalf("auth count = %d, want %d keys rather than one auth per protocol", len(auths), len(keys))
	}
	seen := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth == nil {
			t.Fatal("synthesized nil auth")
		}
		if _, exists := seen[auth.ID]; exists {
			t.Fatalf("duplicate auth ID %q", auth.ID)
		}
		seen[auth.ID] = struct{}{}
		if auth.Attributes["protocols"] != "openai,anthropic" {
			t.Fatalf("auth %q protocols = %q, want both routes", auth.ID, auth.Attributes["protocols"])
		}
	}
}

func openCodeGoTestConfig(models []config.OpenCodeGoModelEntry, keys []config.OpenCodeGoKeyEntry) *config.Config {
	return &config.Config{OpenCodeGo: config.OpenCodeGoConfig{KeyGroups: []config.OpenCodeGoKeyGroup{{
		NamePrefix: "opencode-go",
		OpenAI:     &config.OpenCodeGoProtocolConfig{NameSuffix: "openai", BaseURL: "https://openai.example.com/v1", Prefix: "og", Priority: 7, Models: models},
		Anthropic: &config.OpenCodeGoProtocolConfig{
			NameSuffix: "claude",
			BaseURL:    "https://anthropic.example.com",
			Prefix:     "oc",
			Priority:   3,
			Models:     []config.OpenCodeGoModelEntry{{Name: "claude-upstream", Alias: "claude-visible"}},
		},
		Keys: keys,
	}}}}
}

func synthesizeOpenCodeGoTestAuths(t *testing.T, cfg *config.Config) []*coreauth.Auth {
	t.Helper()
	auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config:      cfg,
		Now:         time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	return auths
}
