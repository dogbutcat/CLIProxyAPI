package config

import "testing"

func TestParseConfigBytesVisionCanonicalNormalizes(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`vision:
  enabled: true
  model: " gemini-vision "
  fallback: " claude-vision "
  scope: " ALL "
  include:
    - " custom-text "
    - "CUSTOM-TEXT"
    - ""
  exclude:
    - " native-vision "
  provider:
    name: " cpa-self "
    base-url: " http://127.0.0.1:8317/v1/ "
    api-key: " sk-test "
    protocol: " OpenAI "
    headers:
      " X-Test ": " value "
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.Vision.Enabled {
		t.Fatal("Vision.Enabled = false, want true")
	}
	if cfg.Vision.Model != "gemini-vision" || cfg.Vision.Fallback != "claude-vision" {
		t.Fatalf("Vision models = %q/%q", cfg.Vision.Model, cfg.Vision.Fallback)
	}
	if cfg.Vision.Scope != "all" {
		t.Fatalf("Vision.Scope = %q, want all", cfg.Vision.Scope)
	}
	if len(cfg.Vision.Include) != 1 || cfg.Vision.Include[0] != "custom-text" {
		t.Fatalf("Vision.Include = %#v", cfg.Vision.Include)
	}
	if len(cfg.Vision.Exclude) != 1 || cfg.Vision.Exclude[0] != "native-vision" {
		t.Fatalf("Vision.Exclude = %#v", cfg.Vision.Exclude)
	}
	if cfg.Vision.Provider.BaseURL != "http://127.0.0.1:8317/v1" {
		t.Fatalf("Vision.Provider.BaseURL = %q", cfg.Vision.Provider.BaseURL)
	}
	if cfg.Vision.Provider.Protocol != "openai" {
		t.Fatalf("Vision.Provider.Protocol = %q", cfg.Vision.Provider.Protocol)
	}
	if cfg.Vision.Provider.Headers["X-Test"] != "value" {
		t.Fatalf("Vision.Provider.Headers = %#v", cfg.Vision.Provider.Headers)
	}
}

func TestParseConfigBytesVisionLegacyTimeoutAcceptedButIgnored(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`vision:
  enabled: true
  model: "gemini-vision"
  fallback-model: "claude-vision"
  image-scope: "invalid"
  include-models:
    - "text-only"
  exclude-models:
    - "native"
  timeout-ms: 50
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() with legacy timeout error = %v", err)
	}
	if cfg.Vision.Fallback != "claude-vision" {
		t.Fatalf("Vision.Fallback = %q", cfg.Vision.Fallback)
	}
	if cfg.Vision.Scope != "latest" {
		t.Fatalf("Vision.Scope = %q, want latest", cfg.Vision.Scope)
	}
	if len(cfg.Vision.Include) != 1 || cfg.Vision.Include[0] != "text-only" {
		t.Fatalf("Vision.Include = %#v", cfg.Vision.Include)
	}
	if len(cfg.Vision.Exclude) != 1 || cfg.Vision.Exclude[0] != "native" {
		t.Fatalf("Vision.Exclude = %#v", cfg.Vision.Exclude)
	}
}

func TestParseConfigBytesVisionProxyLegacyBlockAccepted(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`vision-proxy:
  enabled: true
  model: " gemini-vision "
  fallback-model: " claude-vision "
  image-scope: " all "
  provider:
    base-url: " http://127.0.0.1:8317/v1/ "
    api-key: " sk-test "
    protocol: " OpenAI "
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() with vision-proxy error = %v", err)
	}
	if !cfg.Vision.Enabled {
		t.Fatal("Vision.Enabled = false, want true")
	}
	if cfg.Vision.Model != "gemini-vision" || cfg.Vision.Fallback != "claude-vision" {
		t.Fatalf("Vision models = %q/%q", cfg.Vision.Model, cfg.Vision.Fallback)
	}
	if cfg.Vision.Scope != "all" {
		t.Fatalf("Vision.Scope = %q, want all", cfg.Vision.Scope)
	}
	if cfg.Vision.Provider.BaseURL != "http://127.0.0.1:8317/v1" {
		t.Fatalf("Vision.Provider.BaseURL = %q", cfg.Vision.Provider.BaseURL)
	}
	if cfg.Vision.Provider.Protocol != "openai" {
		t.Fatalf("Vision.Provider.Protocol = %q", cfg.Vision.Provider.Protocol)
	}
}

func TestParseConfigBytesVisionCanonicalOverridesLegacyBlock(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`vision-proxy:
  enabled: true
  model: "legacy-vision"
vision:
  enabled: false
  model: "canonical-vision"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() with vision and vision-proxy error = %v", err)
	}
	if cfg.Vision.Enabled {
		t.Fatal("Vision.Enabled = true, want canonical false")
	}
	if cfg.Vision.Model != "canonical-vision" {
		t.Fatalf("Vision.Model = %q, want canonical-vision", cfg.Vision.Model)
	}
}
