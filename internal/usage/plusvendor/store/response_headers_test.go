package plusstore

import (
	"strings"
	"testing"
	"time"
)

func TestResponseHeaderMetadataSanitizesSecrets(t *testing.T) {
	base := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"x-codex-plan-type":            "pro",
		"x-codex-primary-used-percent": "78.5%",
		"retry-after":                  "30",
		"x-oai-request-id":             "req-public",
		"authorization":                "Bearer sk-secret",
		"set-cookie":                   "session=secret",
		"x-secret-debug":               "secret",
	}, base)
	if metadata == nil || metadata.Quota == nil || metadata.Trace == nil {
		t.Fatalf("metadata = %+v, want quota and trace", metadata)
	}
	derived := DeriveResponseHeaderMetadata(metadata)
	if derived.QuotaPlanType != "pro" || derived.TraceID != "req-public" {
		t.Fatalf("derived metadata = %+v", derived)
	}
	if derived.QuotaRecoverAtMS != base.Add(30*time.Second).UnixMilli() {
		t.Fatalf("recover_at_ms = %d, want retry-after base", derived.QuotaRecoverAtMS)
	}
	if strings.Contains(strings.ToLower(derived.MetadataJSON), "secret") || strings.Contains(derived.MetadataJSON, "sk-secret") {
		t.Fatalf("metadata leaked secret: %s", derived.MetadataJSON)
	}
}
