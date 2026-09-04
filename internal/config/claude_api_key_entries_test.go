package config

import "testing"

func TestParseConfigBytesClaudeAPIKeyEntriesNormalize(t *testing.T) {
	childWeight := 7
	cfg, errParse := ParseConfigBytes([]byte(`claude-api-key:
  - name: " parent "
    api-key: " legacy "
    weight: 3
    base-url: " https://api.anthropic.com "
    proxy-url: " http://parent-proxy "
    prefix: " team/ "
    headers:
      X-Test: " value "
    api-key-entries:
      - name: " child "
        api-key: " child-key "
        weight: 7
        base-url: " https://child.example.com "
        proxy-url: " http://child-proxy "
      - api-key: " disabled-key "
        disabled: true
      - api-key: "   "
      - name: " child "
        api-key: " child-key "
        weight: 7
        base-url: " https://child.example.com "
        proxy-url: " http://child-proxy "
  - disabled: true
    api-key: " skipped-parent "
  - name: empty
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.ClaudeKey) != 1 {
		t.Fatalf("ClaudeKey len = %d, want 1", len(cfg.ClaudeKey))
	}
	entry := cfg.ClaudeKey[0]
	if entry.Name != "parent" {
		t.Fatalf("Name = %q, want parent", entry.Name)
	}
	if entry.APIKey != "legacy" {
		t.Fatalf("APIKey = %q, want legacy", entry.APIKey)
	}
	if entry.Weight == nil || *entry.Weight != 3 {
		t.Fatalf("Weight = %v, want 3", entry.Weight)
	}
	if entry.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("BaseURL = %q", entry.BaseURL)
	}
	if entry.ProxyURL != "http://parent-proxy" {
		t.Fatalf("ProxyURL = %q", entry.ProxyURL)
	}
	if entry.Prefix != "team" {
		t.Fatalf("Prefix = %q, want team", entry.Prefix)
	}
	if got := entry.Headers["X-Test"]; got != "value" {
		t.Fatalf("header X-Test = %q, want value", got)
	}
	if len(entry.APIKeyEntries) != 1 {
		t.Fatalf("APIKeyEntries len = %d, want 1", len(entry.APIKeyEntries))
	}
	child := entry.APIKeyEntries[0]
	if child.Name != "child" {
		t.Fatalf("child Name = %q, want child", child.Name)
	}
	if child.APIKey != "child-key" {
		t.Fatalf("child APIKey = %q, want child-key", child.APIKey)
	}
	if child.Weight == nil || *child.Weight != childWeight {
		t.Fatalf("child Weight = %v, want %d", child.Weight, childWeight)
	}
	if child.BaseURL != "https://child.example.com" {
		t.Fatalf("child BaseURL = %q", child.BaseURL)
	}
	if child.ProxyURL != "http://child-proxy" {
		t.Fatalf("child ProxyURL = %q", child.ProxyURL)
	}
}

func TestParseConfigBytesClaudeAPIKeyEntryWeightValidation(t *testing.T) {
	_, errParse := ParseConfigBytes([]byte(`claude-api-key:
  - api-key: parent
    api-key-entries:
      - api-key: child
        weight: 1.5
`))
	if errParse == nil {
		t.Fatal("ParseConfigBytes() error = nil, want invalid child weight")
	}
}
