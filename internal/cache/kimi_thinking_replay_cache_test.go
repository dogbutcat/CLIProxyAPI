package cache

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
)

type fakeKimiThinkingReplayKVClient struct {
	mu            sync.Mutex
	values        map[string][]byte
	lastSetTTL    time.Duration
	lastCASTTL    time.Duration
	lastExpireTTL time.Duration
}

func newFakeKimiThinkingReplayKVClient() *fakeKimiThinkingReplayKVClient {
	return &fakeKimiThinkingReplayKVClient{values: make(map[string][]byte)}
}

func (c *fakeKimiThinkingReplayKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, found := c.values[key]
	return append([]byte(nil), value...), found, nil
}

func (c *fakeKimiThinkingReplayKVClient) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	c.mu.Lock()
	c.values[key] = append([]byte(nil), value...)
	c.lastSetTTL = opts.EX
	c.mu.Unlock()
	return true, nil
}

func (c *fakeKimiThinkingReplayKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var deleted int64
	for _, key := range keys {
		if _, found := c.values[key]; found {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

func (c *fakeKimiThinkingReplayKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, found := c.values[key]
	if found != expectedExists || (found && !bytes.Equal(current, expected)) {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	c.lastCASTTL = ttl
	return true, nil
}

func (c *fakeKimiThinkingReplayKVClient) KVExpire(_ context.Context, key string, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastExpireTTL = ttl
	_, found := c.values[key]
	return found, nil
}

func useFakeKimiThinkingReplayKVClient(t *testing.T, client *fakeKimiThinkingReplayKVClient) {
	t.Helper()
	previous := currentKimiThinkingReplayKVClient
	currentKimiThinkingReplayKVClient = func() (kimiThinkingReplayKVClient, bool, error) {
		return client, true, nil
	}
	t.Cleanup(func() {
		currentKimiThinkingReplayKVClient = previous
	})
}

func TestKimiThinkingReplayConditionalDeleteKeepsNewerContent(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "k3"
	const sessionKey = "execution:conditional-delete"
	oldContent := []byte(`[{"type":"thinking","signature":"old"}]`)
	newContent := []byte(`[{"type":"thinking","signature":"new"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, oldContent) {
		t.Fatal("failed to seed old content")
	}
	_, snapshot, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found {
		t.Fatalf("GetKimiThinkingReplayWithSnapshotRequired() = found %v, error %v", found, errGet)
	}
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, newContent) {
		t.Fatal("failed to write newer content")
	}
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, oldContent) {
		t.Fatal("failed to write latest content with repeated bytes")
	}

	deleted, errDelete := DeleteKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshot)
	if errDelete != nil {
		t.Fatalf("DeleteKimiThinkingReplayIfUnchanged() error = %v", errDelete)
	}
	if deleted {
		t.Fatal("stale snapshot deleted newer content")
	}
	got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || !bytes.Equal(got, oldContent) {
		t.Fatalf("cached content = %s, found %v, error %v; want latest repeated content", got, found, errGet)
	}
}

func TestKimiThinkingReplayConditionalReplaceKeepsConcurrentContent(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "k3"
	const sessionKey = "execution:conditional-replace"
	_, snapshot, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || found {
		t.Fatalf("initial cache read = found %v, error %v; want miss", found, errGet)
	}
	newContent := []byte(`[{"type":"thinking","signature":"new"}]`)
	staleContent := []byte(`[{"type":"thinking","signature":"stale"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, newContent) {
		t.Fatal("failed to write concurrent content")
	}

	replaced, errReplace := ReplaceKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshot, staleContent)
	if errReplace != nil {
		t.Fatalf("ReplaceKimiThinkingReplayIfUnchanged() error = %v", errReplace)
	}
	if replaced {
		t.Fatal("stale snapshot replaced concurrent content")
	}
	got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || !bytes.Equal(got, newContent) {
		t.Fatalf("cached content = %s, found %v, error %v; want concurrent content", got, found, errGet)
	}
}

func TestKimiThinkingReplayTombstoneFencesConcurrentMiss(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "k3"
	const sessionKey = "execution:tombstone-fence"
	_, firstSnapshot, firstFound, errFirst := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	_, secondSnapshot, secondFound, errSecond := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errFirst != nil || errSecond != nil || firstFound || secondFound {
		t.Fatalf("concurrent misses = %v/%v, errors %v/%v", firstFound, secondFound, errFirst, errSecond)
	}
	deleted, errDelete := DeleteKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, firstSnapshot)
	if errDelete != nil || !deleted {
		t.Fatalf("first miss delete = %v, error %v", deleted, errDelete)
	}
	staleContent := []byte(`[{"type":"thinking","signature":"stale"}]`)
	replaced, errReplace := ReplaceKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, secondSnapshot, staleContent)
	if errReplace != nil {
		t.Fatalf("stale miss replace error = %v", errReplace)
	}
	if replaced {
		t.Fatal("stale miss snapshot crossed a newer tombstone")
	}
}

func TestKimiThinkingReplayExpiredLocalReadFencesStaleWriter(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "k3"
	const sessionKey = "execution:ttl-fence"
	oldContent := []byte(`[{"type":"thinking","signature":"old"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, oldContent) {
		t.Fatal("failed to seed old content")
	}
	_, oldSnapshot, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found {
		t.Fatalf("initial cache read = found %v, error %v", found, errGet)
	}

	key := kimiThinkingReplayCacheKey(modelFamily, sessionKey)
	kimiThinkingReplayMu.Lock()
	entry := kimiThinkingReplayEntries[key]
	entry.Timestamp = time.Now().Add(-KimiThinkingReplayCacheTTL - time.Second)
	kimiThinkingReplayEntries[key] = entry
	kimiThinkingReplayMu.Unlock()

	_, freshSnapshot, freshFound, errFresh := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errFresh != nil || freshFound {
		t.Fatalf("expired cache read = found %v, error %v; want fenced miss", freshFound, errFresh)
	}
	staleContent := []byte(`[{"type":"thinking","signature":"stale"}]`)
	replaced, errReplace := ReplaceKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, oldSnapshot, staleContent)
	if errReplace != nil {
		t.Fatalf("stale replace error = %v", errReplace)
	}
	if replaced {
		t.Fatal("stale pre-expiry snapshot replaced fenced miss")
	}
	newContent := []byte(`[{"type":"thinking","signature":"new"}]`)
	replaced, errReplace = ReplaceKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, freshSnapshot, newContent)
	if errReplace != nil || !replaced {
		t.Fatalf("fresh fenced replace = %v, error %v", replaced, errReplace)
	}
	got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || !bytes.Equal(got, newContent) {
		t.Fatalf("cached content = %s, found %v, error %v; want fresh content", got, found, errGet)
	}
}

func TestKimiThinkingReplayHomeGenerationPreventsABADelete(t *testing.T) {
	client := newFakeKimiThinkingReplayKVClient()
	useFakeKimiThinkingReplayKVClient(t, client)

	const modelFamily = "k3"
	const sessionKey = "execution:home-aba"
	contentA := []byte(`[{"type":"thinking","signature":"A"}]`)
	contentB := []byte(`[{"type":"thinking","signature":"B"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, contentA) {
		t.Fatal("failed to seed Home content A")
	}
	_, snapshotA, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found {
		t.Fatalf("Home snapshot A = found %v, error %v", found, errGet)
	}
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, contentB) ||
		!CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, contentA) {
		t.Fatal("failed to complete Home A-B-A sequence")
	}
	deleted, errDelete := DeleteKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshotA)
	if errDelete != nil {
		t.Fatalf("Home stale delete error = %v", errDelete)
	}
	if deleted {
		t.Fatal("Home stale snapshot deleted a newer generation with repeated content")
	}
	got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || !bytes.Equal(got, contentA) {
		t.Fatalf("Home cached content = %s, found %v, error %v; want latest A", got, found, errGet)
	}
}

func TestKimiThinkingReplayHomeMissReservesAndReplacesWithTTL(t *testing.T) {
	client := newFakeKimiThinkingReplayKVClient()
	useFakeKimiThinkingReplayKVClient(t, client)

	const modelFamily = "k3"
	const sessionKey = "execution:home-miss"
	_, snapshot, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || found {
		t.Fatalf("Home missing read = found %v, error %v; want fenced miss", found, errGet)
	}
	if client.lastCASTTL != KimiThinkingReplayCacheTTL || client.lastExpireTTL != KimiThinkingReplayCacheTTL {
		t.Fatalf("Home miss TTLs = CAS %v expire %v, want %v", client.lastCASTTL, client.lastExpireTTL, KimiThinkingReplayCacheTTL)
	}
	content := []byte(`[{"type":"thinking","signature":"new"}]`)
	replaced, errReplace := ReplaceKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshot, content)
	if errReplace != nil || !replaced {
		t.Fatalf("Home replace = %v, error %v", replaced, errReplace)
	}
	got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || !bytes.Equal(got, content) {
		t.Fatalf("Home cached content = %s, found %v, error %v; want replaced content", got, found, errGet)
	}
}

func TestKimiThinkingReplayTracksAggregateLocalBytes(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	first := []byte(`[{"type":"thinking","signature":"first"}]`)
	second := []byte(`[{"type":"thinking","signature":"second"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:bytes-1", first) ||
		!CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:bytes-2", second) {
		t.Fatal("failed to seed aggregate byte accounting")
	}
	if got, want := kimiThinkingReplayTotalBytes, len(first)+len(second); got != want {
		t.Fatalf("aggregate bytes = %d, want %d", got, want)
	}
	if errDelete := DeleteKimiThinkingReplayRequired(context.Background(), "k3", "execution:bytes-1"); errDelete != nil {
		t.Fatalf("DeleteKimiThinkingReplayRequired() error = %v", errDelete)
	}
	if got, want := kimiThinkingReplayTotalBytes, len(second); got != want {
		t.Fatalf("aggregate bytes after delete = %d, want %d", got, want)
	}
	ClearKimiThinkingReplayCache()
	if kimiThinkingReplayTotalBytes != 0 {
		t.Fatalf("aggregate bytes after clear = %d, want 0", kimiThinkingReplayTotalBytes)
	}
}

func TestKimiThinkingReplayRejectsOversizedContent(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	content := make([]byte, KimiThinkingReplayCacheMaxBytesPerEntry+1)
	content[0] = '['
	for i := 1; i < len(content)-1; i++ {
		content[i] = ' '
	}
	content[len(content)-1] = ']'
	if CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:oversized", content) {
		t.Fatal("oversized content was cached")
	}
}

func TestKimiThinkingReplayRejectsInvalidContentBlocks(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	cases := map[string][]byte{
		"scalar-array": []byte(`[1]`),
		"missing-type": []byte(`[{"thinking":"reasoning","signature":"sig"}]`),
		"empty-type":   []byte(`[{"type":"  ","signature":"sig"}]`),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:"+name, content) {
				t.Fatalf("%s content was cached", name)
			}
			if got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), "k3", "execution:"+name); errGet != nil || found || got != nil {
				t.Fatalf("%s cache read = %s, found %v, error %v; want miss", name, got, found, errGet)
			}
		})
	}
}
