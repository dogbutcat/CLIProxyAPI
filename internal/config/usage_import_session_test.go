package config

import "testing"

func TestUsageImportSessionConfigDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.UsageImportSession.ChunkSizeBytes != DefaultUsageImportSessionChunkSizeBytes ||
		cfg.UsageImportSession.MaxSessionBytes != DefaultUsageImportSessionMaxBytes ||
		cfg.UsageImportSession.MaxActive != DefaultUsageImportSessionMaxActive ||
		cfg.UsageImportSession.TTLMinutes != DefaultUsageImportSessionTTLMinutes {
		t.Fatalf("usage import defaults = %#v", cfg.UsageImportSession)
	}
}

func TestUsageImportSessionConfigRejectsInvalidValues(t *testing.T) {
	for _, yaml := range []string{
		"usage-import-session:\n  chunk-size-bytes: -1\n",
		"usage-import-session:\n  max-session-bytes: 17179869185\n",
		"usage-import-session:\n  max-active: -1\n",
		"usage-import-session:\n  ttl-minutes: 1441\n",
	} {
		if _, err := ParseConfigBytes([]byte(yaml)); err == nil {
			t.Fatalf("ParseConfigBytes(%q) error = nil, want invalid usage import session config", yaml)
		}
	}
}
