package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenCodeGoCanonicalAndLegacyNormalizeSame(t *testing.T) {
	canonical, errCanonical := ParseConfigBytes([]byte(`opencode-go:
  quota:
    poll-interval: "10m"
    threshold: 5
  key-groups:
    - name-prefix: " team "
      headers:
        X-Test: " value "
      openai:
        base-url: " https://api.opencode.ai/v1 "
        prefix: " team "
        priority: 3
        models:
          - " gpt-5 "
          - name: " o3 "
            alias: "reasoner"
      anthropic:
        base-url: " https://api.opencode.ai "
        models:
          - name: " claude-sonnet "
            alias: "sonnet"
      keys:
        - key-name: " account-a "
          api-key: " key-a "
          proxy-url: " socks5://proxy "
          workspace-id: " workspace "
          auth-cookie: " cookie "
`))
	if errCanonical != nil {
		t.Fatalf("canonical parse error = %v", errCanonical)
	}
	legacy, errLegacy := ParseConfigBytes([]byte(`routing:
  opencode-go-poll-interval: "10m"
  opencode-go-poll-threshold: 5
key-groups:
  - name-prefix: " team "
    headers:
      X-Test: " value "
    openai:
      base-url: " https://api.opencode.ai/v1 "
      prefix: " team "
      priority: 3
      models:
        - " gpt-5 "
        - name: " o3 "
          alias: "reasoner"
    anthropic:
      base-url: " https://api.opencode.ai "
      models:
        - name: " claude-sonnet "
          alias: "sonnet"
    keys:
      - key-name: " account-a "
        api-key: " key-a "
        proxy-url: " socks5://proxy "
        workspace-id: " workspace "
        auth-cookie: " cookie "
`))
	if errLegacy != nil {
		t.Fatalf("legacy parse error = %v", errLegacy)
	}
	if !reflect.DeepEqual(canonical.OpenCodeGo, legacy.OpenCodeGo) {
		t.Fatalf("legacy OpenCodeGo = %#v, want %#v", legacy.OpenCodeGo, canonical.OpenCodeGo)
	}
	if len(legacy.LegacyOpenCodeGoKeyGroups) != 0 {
		t.Fatalf("LegacyOpenCodeGoKeyGroups len = %d, want 0", len(legacy.LegacyOpenCodeGoKeyGroups))
	}
	if legacy.Routing.OpenCodeGoPollInterval != "" || legacy.Routing.OpenCodeGoPollThreshold != nil {
		t.Fatalf("legacy routing quota fields were not cleared: %#v", legacy.Routing)
	}
}

func TestOpenCodeGoLegacyFlatListNormalizes(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`opencode-go:
  - name: " account-claude "
    api-key: " key "
    base-url: " https://api.opencode.ai "
    protocol: "anthropic"
    prefix: " oc "
    priority: 7
    disabled: true
    disable-cooling: true
    workspace-id: " ws "
    auth-cookie: " cookie "
    models:
      - name: " claude "
        alias: "sonnet"
`))
	if errParse != nil {
		t.Fatalf("parse error = %v", errParse)
	}
	if len(cfg.OpenCodeGo.KeyGroups) != 1 {
		t.Fatalf("key group count = %d, want 1", len(cfg.OpenCodeGo.KeyGroups))
	}
	group := cfg.OpenCodeGo.KeyGroups[0]
	if group.NamePrefix != "opencode-go" || !group.Disabled || !group.DisableCooling || len(group.Keys) != 1 || group.Anthropic == nil {
		t.Fatalf("legacy group = %#v", group)
	}
	if group.Keys[0].KeyName != "account-claude" || group.Keys[0].APIKey != "key" || group.Keys[0].WorkspaceID != "ws" || group.Keys[0].AuthCookie != "cookie" {
		t.Fatalf("legacy key = %#v", group.Keys[0])
	}
	if group.Anthropic.NameSuffix != "claude" || group.Anthropic.Prefix != "oc" || group.Anthropic.Models[0].Alias != "sonnet" {
		t.Fatalf("legacy protocol = %#v", group.Anthropic)
	}
}

func TestOpenCodeGoModelEntryStringAndObjectFormsRoundTrip(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`opencode-go:
  key-groups:
    - name-prefix: oc
      openai:
        base-url: https://api.opencode.ai/v1
        models:
          - gpt-5
          - name: o3
            alias: reasoner
      keys:
        - key-name: a
          api-key: key-a
`))
	if errParse != nil {
		t.Fatalf("parse error = %v", errParse)
	}
	models := cfg.OpenCodeGo.KeyGroups[0].OpenAI.Models
	if len(models) != 2 || models[0].Name != "gpt-5" || models[1].Alias != "reasoner" {
		t.Fatalf("models = %#v", models)
	}
	rendered, errMarshal := yaml.Marshal(cfg.OpenCodeGo)
	if errMarshal != nil {
		t.Fatalf("yaml.Marshal(OpenCodeGo) error = %v", errMarshal)
	}
	out := string(rendered)
	if !strings.Contains(out, "- gpt-5") || !strings.Contains(out, "alias: reasoner") {
		t.Fatalf("rendered OpenCodeGo missing expected string/object forms:\n%s", out)
	}
}

func TestOpenCodeGoModelEntryJSONStringAndObjectFormsRoundTrip(t *testing.T) {
	data := []byte(`["gpt-5",{"name":"o3","alias":"reasoner"}]`)
	var models []OpenCodeGoModelEntry
	if errUnmarshal := json.Unmarshal(data, &models); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if len(models) != 2 || models[0].Name != "gpt-5" || models[1].Alias != "reasoner" {
		t.Fatalf("models = %#v", models)
	}
	rendered, errMarshal := json.Marshal(models)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	if !strings.Contains(string(rendered), `"gpt-5"`) || !strings.Contains(string(rendered), `"alias":"reasoner"`) {
		t.Fatalf("rendered JSON = %s", rendered)
	}
}

func TestOpenCodeGoSavePrunesLegacyRepresentations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := []byte(`routing:
  strategy: round-robin
  opencode-go-poll-interval: 10m
  opencode-go-poll-threshold: 5
key-groups:
  - name-prefix: legacy
    openai:
      base-url: https://api.opencode.ai/v1
      models: [gpt-5]
    keys:
      - key-name: a
        api-key: key-a
`)
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	saved, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	out := string(saved)
	if strings.Contains(out, "\nkey-groups:") || strings.Contains(out, "opencode-go-poll-") {
		t.Fatalf("saved config still contains legacy OpenCodeGo keys:\n%s", out)
	}
	if !strings.Contains(out, "opencode-go:") || !strings.Contains(out, "poll-interval: 10m") || !strings.Contains(out, "threshold: 5") {
		t.Fatalf("saved config missing canonical OpenCodeGo block:\n%s", out)
	}
}

func TestOpenCodeGoValidationRejectsInvalidQuota(t *testing.T) {
	_, errParse := ParseConfigBytes([]byte(`opencode-go:
  quota:
    poll-interval: "0s"
`))
	if errParse == nil {
		t.Fatal("parse error = nil, want invalid quota")
	}
	if !strings.Contains(errParse.Error(), "opencode-go.quota.poll-interval") {
		t.Fatalf("parse error = %v", errParse)
	}
}

func TestOpenCodeGoValidationRejectsDuplicateClientVisibleModels(t *testing.T) {
	_, errParse := ParseConfigBytes([]byte(`opencode-go:
  key-groups:
    - name-prefix: oc
      openai:
        base-url: https://api.opencode.ai/v1
        models:
          - name: gpt-5
            alias: shared
      anthropic:
        base-url: https://api.opencode.ai
        models:
          - name: claude
            alias: shared
      keys:
        - key-name: a
          api-key: key-a
`))
	if errParse == nil {
		t.Fatal("parse error = nil, want duplicate model alias")
	}
	if !strings.Contains(errParse.Error(), "duplicate client-visible model name") {
		t.Fatalf("parse error = %v", errParse)
	}
}

func TestOpenCodeGoValidationAllowsDuplicateClientVisibleModelForSameProtocolAndUpstream(t *testing.T) {
	_, errParse := ParseConfigBytes([]byte(`opencode-go:
  key-groups:
    - name-prefix: oc-a
      openai:
        base-url: https://api-a.opencode.ai/v1
        models:
          - name: gpt-5
            alias: shared
      keys:
        - key-name: a
          api-key: key-a
    - name-prefix: oc-b
      openai:
        base-url: https://api-b.opencode.ai/v1
        models:
          - name: gpt-5
            alias: shared
      keys:
        - key-name: b
          api-key: key-b
`))
	if errParse != nil {
		t.Fatalf("parse error = %v", errParse)
	}
}

func TestOpenCodeGoValidationRejectsDuplicateAliasWithDifferentUpstream(t *testing.T) {
	_, errParse := ParseConfigBytes([]byte(`opencode-go:
  key-groups:
    - name-prefix: oc
      openai:
        base-url: https://api.opencode.ai/v1
        models:
          - name: gpt-5
            alias: shared
          - name: o3
            alias: shared
      keys:
        - key-name: a
          api-key: key-a
`))
	if errParse == nil {
		t.Fatal("parse error = nil, want duplicate alias")
	}
	if !strings.Contains(errParse.Error(), "duplicate client-visible model name") {
		t.Fatalf("parse error = %v", errParse)
	}
}

func TestOpenCodeGoValidationErrorsRedactSecrets(t *testing.T) {
	_, errParse := ParseConfigBytes([]byte(`opencode-go:
  key-groups:
    - name-prefix: oc
      openai:
        base-url: https://api.opencode.ai/v1
      keys:
        - key-name: a
          api-key: "super-secret-key"
        - key-name: a
          api-key: "second-secret-key"
`))
	if errParse == nil {
		t.Fatal("parse error = nil, want duplicate identity")
	}
	if strings.Contains(errParse.Error(), "super-secret-key") || strings.Contains(errParse.Error(), "second-secret-key") {
		t.Fatalf("validation error leaked secret: %v", errParse)
	}
}
