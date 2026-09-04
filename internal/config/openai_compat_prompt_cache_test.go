package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAICompatPromptCacheKeyConfigRoundTrip(t *testing.T) {
	rawYAML := []byte(`openai-compatibility:
  - name: disabled
    base-url: https://disabled.example.com/v1
  - name: enabled
    base-url: https://enabled.example.com/v1
    support-prompt-cache-key: true
  - name: explicit-false
    base-url: https://explicit-false.example.com/v1
    support-prompt-cache-key: false
`)
	var cfg Config
	if errUnmarshal := yaml.Unmarshal(rawYAML, &cfg); errUnmarshal != nil {
		t.Fatalf("unmarshal YAML: %v", errUnmarshal)
	}
	if len(cfg.OpenAICompatibility) != 3 {
		t.Fatalf("openai-compatibility count = %d, want 3", len(cfg.OpenAICompatibility))
	}
	if cfg.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatal("missing support-prompt-cache-key should default to false")
	}
	if !cfg.OpenAICompatibility[1].SupportPromptCacheKey {
		t.Fatal("explicit support-prompt-cache-key true was not decoded")
	}
	if cfg.OpenAICompatibility[2].SupportPromptCacheKey {
		t.Fatal("explicit support-prompt-cache-key false should decode as false")
	}

	renderedYAML, errMarshalYAML := yaml.Marshal(&cfg)
	if errMarshalYAML != nil {
		t.Fatalf("marshal YAML: %v", errMarshalYAML)
	}
	if gotYAML := string(renderedYAML); !strings.Contains(gotYAML, "support-prompt-cache-key: true") {
		t.Fatalf("marshaled YAML missing support-prompt-cache-key: true:\n%s", gotYAML)
	}

	renderedJSON, errMarshalJSON := json.Marshal(&cfg)
	if errMarshalJSON != nil {
		t.Fatalf("marshal JSON: %v", errMarshalJSON)
	}
	var decoded struct {
		OpenAICompatibility []struct {
			SupportPromptCacheKey bool `json:"support-prompt-cache-key"`
		} `json:"openai-compatibility"`
	}
	if errUnmarshalJSON := json.Unmarshal(renderedJSON, &decoded); errUnmarshalJSON != nil {
		t.Fatalf("unmarshal JSON: %v", errUnmarshalJSON)
	}
	if decoded.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatal("missing support-prompt-cache-key should encode as false in JSON")
	}
	if !decoded.OpenAICompatibility[1].SupportPromptCacheKey {
		t.Fatal("JSON round-trip lost support-prompt-cache-key true")
	}
	if decoded.OpenAICompatibility[2].SupportPromptCacheKey {
		t.Fatal("JSON round-trip changed explicit support-prompt-cache-key false")
	}
}

func TestOpenAICompatPromptCacheKeySavePreservesTrue(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	initial := []byte(`openai-compatibility:
  - name: enabled
    base-url: https://enabled.example.com/v1
    support-prompt-cache-key: true
`)
	if errWrite := os.WriteFile(path, initial, 0600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	cfg.OpenAICompatibility[0].Headers = map[string]string{"X-Test": "1"}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	if !strings.Contains(string(data), "support-prompt-cache-key: true") {
		t.Fatalf("saved YAML lost support-prompt-cache-key: true:\n%s", string(data))
	}
	reloaded, errReload := LoadConfig(path)
	if errReload != nil {
		t.Fatalf("LoadConfig(reloaded) error = %v", errReload)
	}
	if !reloaded.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatal("reload lost support-prompt-cache-key=true")
	}
}
