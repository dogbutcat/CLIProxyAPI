package management

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestBuildAuthFileEntry_OpenCodeGoSafeIdentity(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "opencode-go:abc123",
		Provider: "opencode-go",
		Label:    "legacy-opencode-label",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "opencode-secret",
			coreauth.AttributeAuthKind:    coreauth.AuthKindAPIKey,
			coreauth.AttributeRuntimeOnly: "true",
			"auth_cookie":                 "cookie-secret",
			"provider_key":                "opencode-go",
			"key_name":                    "acct-a",
			"generated_name":              "opencode-go-acct-a",
			"workspace_id":                "workspace-a",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	entry := firstAuthFileEntry(t, h)
	if got := entry["label"]; got != "acct-a" {
		t.Fatalf("label = %#v, want acct-a", got)
	}
	if got := entry["account"]; got != "acct-a" {
		t.Fatalf("account = %#v, want acct-a", got)
	}
	if _, exists := entry["protocol"]; exists {
		t.Fatalf("canonical key entry contains scalar protocol: %#v", entry)
	}
	if _, exists := entry["protocols"]; exists {
		t.Fatalf("auth-files entry contains route metadata: %#v", entry)
	}
	if got := entry["project_id"]; got != "workspace-a" {
		t.Fatalf("project_id = %#v, want workspace-a", got)
	}
	if got := entry["opencode_go_entry_name"]; got != "opencode-go:abc123" {
		t.Fatalf("opencode_go_entry_name = %#v, want auth ID", got)
	}
	for _, secretKey := range []string{"api_key", "auth_cookie"} {
		if _, exists := entry[secretKey]; exists {
			t.Fatalf("secret field %q leaked in auth file entry: %#v", secretKey, entry)
		}
	}
}
