package plusstore

import (
	"net/url"
	"path/filepath"
	"testing"
)

func TestDataSourceNameIncludesImmediateTxlockAndPreservesPragmas(t *testing.T) {
	dsn := dataSourceName(filepath.Join("tmp", "usage.sqlite"))
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	query := parsed.Query()
	if got := query["_txlock"]; len(got) != 1 || got[0] != "immediate" {
		t.Fatalf("_txlock = %v, want [immediate]", got)
	}
	pragmas := query["_pragma"]
	if len(pragmas) != 3 {
		t.Fatalf("_pragma values = %v, want 3 entries", pragmas)
	}
	checks := map[string]struct{}{
		"busy_timeout(5000)": {},
		"foreign_keys(1)":    {},
		"synchronous(FULL)":  {},
	}
	for _, pragma := range pragmas {
		if _, ok := checks[pragma]; !ok {
			t.Fatalf("unexpected pragma %q", pragma)
		}
		delete(checks, pragma)
	}
	if len(checks) != 0 {
		t.Fatalf("missing pragmas: %v", checks)
	}
}
