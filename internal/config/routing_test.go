package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingStrategyAliasesCanonicalizeOnParse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "default", raw: "", want: "round-robin"},
		{name: "round robin short", raw: "rr", want: "round-robin"},
		{name: "weighted round robin short", raw: "wrr", want: "weighted-round-robin"},
		{name: "fill first short", raw: "ff", want: "fill-first"},
		{name: "seq random short", raw: "sr", want: "seq-random"},
		{name: "seq random long", raw: "sequential-random", want: "seq-random"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := "routing:\n"
			if test.raw != "" {
				payload += "  strategy: " + test.raw + "\n"
			}
			cfg, errParse := ParseConfigBytes([]byte(payload))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if cfg.Routing.Strategy != test.want {
				t.Fatalf("strategy = %q, want %q", cfg.Routing.Strategy, test.want)
			}
		})
	}
}

func TestRoutingStrategySaveWritesCanonicalSeqRandom(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("routing:\n  strategy: sr\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("routing:\n  strategy: sr\n"), 0644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	saved, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if !strings.Contains(string(saved), "strategy: seq-random") {
		t.Fatalf("saved config did not canonicalize seq-random:\n%s", saved)
	}
}

func TestRoutingStrategyInvalidValueIsPreservedForUpstreamBehavior(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("routing:\n  strategy: custom-experimental\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.Routing.Strategy != "custom-experimental" {
		t.Fatalf("strategy = %q, want invalid value preserved", cfg.Routing.Strategy)
	}
}
