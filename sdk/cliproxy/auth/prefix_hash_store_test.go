package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

func TestPrefixHashStorePersistsBoundsAndExpires(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewPrefixHashStoreWithOptions(dir, PrefixHashStoreOptions{TTL: time.Hour, Max: 2})
	if err != nil {
		t.Fatalf("NewPrefixHashStoreWithOptions() error = %v", err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.Append("a", "model", "auth-a")
	store.Append("b", "model", "auth-b")
	store.Append("c", "model", "auth-c")
	if got := store.Lookup("a", "model"); got != "" {
		t.Fatalf("bounded lookup for a = %q, want empty", got)
	}
	if got := store.Lookup("c", "model"); got != "auth-c" {
		t.Fatalf("lookup c = %q, want auth-c", got)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	reopened, err := NewPrefixHashStoreWithOptions(dir, PrefixHashStoreOptions{TTL: time.Hour, Max: 2})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	reopened.now = func() time.Time { return now.Add(2 * time.Hour) }
	if got := reopened.Lookup("c", "model"); got != "" {
		t.Fatalf("expired lookup c = %q, want empty", got)
	}
}

func TestPrefixHashStoreCorruptionRecovery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	storePath := filepath.Join(dataDir, prefixHashFileName)
	if err := os.WriteFile(storePath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := NewPrefixHashStoreWithOptions(dir, PrefixHashStoreOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewPrefixHashStoreWithOptions() error = %v", err)
	}
	if got := store.Lookup("cache", "model"); got != "" {
		t.Fatalf("lookup from corrupt store = %q, want empty", got)
	}
	matches, errGlob := filepath.Glob(storePath + ".corrupt-*")
	if errGlob != nil {
		t.Fatalf("Glob() error = %v", errGlob)
	}
	if len(matches) != 1 {
		t.Fatalf("corrupt backups = %d, want 1", len(matches))
	}
}

func TestPrefixHashStoreOAGFingerprint(t *testing.T) {
	t.Parallel()

	store, err := NewPrefixHashStoreWithOptions(t.TempDir(), PrefixHashStoreOptions{TailK: 1})
	if err != nil {
		t.Fatalf("NewPrefixHashStoreWithOptions() error = %v", err)
	}
	payload := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"assistant","content":"unstable"},{"role":"user","content":[{"type":"thinking","text":"drop"},{"type":"text","text":"hello"}]}]}`)
	fp := store.Fingerprint(oagmsg.FormatOpenAI, payload)
	if fp.CacheID == "" {
		t.Fatal("CacheID is empty")
	}
	if fp.TailID == "" {
		t.Fatal("TailID is empty")
	}
	if fp.CacheIDAfterSuccess == "" {
		t.Fatal("CacheIDAfterSuccess is empty")
	}
}
