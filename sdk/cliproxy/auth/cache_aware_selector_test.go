package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type fixedSelector struct {
	picked int
	auth   *Auth
}

func (s *fixedSelector) Pick(context.Context, string, string, cliproxyexecutor.Options, []*Auth) (*Auth, error) {
	s.picked++
	return s.auth, nil
}

func cacheAwarePayload(user string) []byte {
	return []byte(`{"messages":[{"role":"system","content":"s"},{"role":"assistant","content":"a"},{"role":"user","content":"` + user + `"}]}`)
}

func cacheAwareNextPayload(user string) []byte {
	return []byte(`{"messages":[{"role":"system","content":"s"},{"role":"assistant","content":"a"},{"role":"user","content":"` + user + `"},{"role":"assistant","content":"ignored"},{"role":"user","content":"next"}]}`)
}

func newTestCacheAwareStore(t *testing.T, ttl time.Duration) *PrefixHashStore {
	t.Helper()
	store, err := NewPrefixHashStoreWithOptions(t.TempDir(), PrefixHashStoreOptions{TTL: ttl, Max: 16, TailK: 1})
	if err != nil {
		t.Fatalf("NewPrefixHashStoreWithOptions() error = %v", err)
	}
	return store
}

func TestCacheAwareSelectorAffinityAfterSuccessfulResponse(t *testing.T) {
	t.Parallel()

	store := newTestCacheAwareStore(t, time.Hour)
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	fallback := &fixedSelector{auth: authB}
	selector := NewCacheAwareSelector(fallback, store)

	meta := map[string]any{}
	selected, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{OriginalRequest: cacheAwarePayload("hello"), Metadata: meta}, []*Auth{authA, authB})
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if selected.ID != "auth-b" || fallback.picked != 1 {
		t.Fatalf("fallback selected %q picked=%d, want auth-b once", selected.ID, fallback.picked)
	}
	if got := store.Lookup(store.Fingerprint(oagmsg.FormatOpenAI, cacheAwareNextPayload("hello")).CacheID, "model"); got != "" {
		t.Fatalf("mapping before response callback = %q, want empty", got)
	}
	callback, ok := meta[CacheAwareResponseCallbackMetadataKey].(CacheAwareResponseCallback)
	if !ok || callback == nil {
		t.Fatal("response callback not registered")
	}
	callback()

	fallback.auth = authA
	selected, errPick = selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{OriginalRequest: cacheAwareNextPayload("hello"), Metadata: map[string]any{}}, []*Auth{authA, authB})
	if errPick != nil {
		t.Fatalf("Pick() cache hit error = %v", errPick)
	}
	if selected.ID != "auth-b" {
		t.Fatalf("cache hit selected %q, want auth-b", selected.ID)
	}
	if fallback.picked != 1 {
		t.Fatalf("fallback picked after cache hit = %d, want 1", fallback.picked)
	}
}

func TestCacheAwareSelectorUsesOAGConversationFingerprint(t *testing.T) {
	t.Parallel()

	store := newTestCacheAwareStore(t, time.Hour)
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	first := []byte(`{"messages":[{"role":"system","content":"approved instructions A"},{"role":"user","content":"Stable root"},{"role":"assistant","content":"ignored"},{"role":"user","content":"next"}]}`)
	next := []byte(`{"messages":[{"role":"system","content":"approved instructions B"},{"role":"user","content":"Stable root"},{"role":"assistant","content":"different"},{"role":"user","content":"next"}]}`)
	fp := store.Fingerprint(oagmsg.FormatOpenAI, first)
	store.Append(fp.FingerprintID, "model", authB.ID)
	fallback := &fixedSelector{auth: authA}
	selector := NewCacheAwareSelector(fallback, store)

	selected, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{OriginalRequest: next, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{}}, []*Auth{authA, authB})
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if selected.ID != "auth-b" {
		t.Fatalf("fingerprint hit selected %q, want auth-b", selected.ID)
	}
	if fallback.picked != 0 {
		t.Fatalf("fallback picked after fingerprint hit = %d, want 0", fallback.picked)
	}
}

func TestCacheAwareSelectorUnavailableAuthFallsBack(t *testing.T) {
	t.Parallel()

	store := newTestCacheAwareStore(t, time.Hour)
	fp := store.Fingerprint(oagmsg.FormatOpenAI, cacheAwareNextPayload("hello"))
	store.Append(fp.CacheID, "model", "missing-auth")
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	fallback := &fixedSelector{auth: authA}
	selector := NewCacheAwareSelector(fallback, store)

	selected, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{OriginalRequest: cacheAwareNextPayload("hello"), Metadata: map[string]any{}}, []*Auth{authA})
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if selected.ID != "auth-a" || fallback.picked != 1 {
		t.Fatalf("selected=%q fallback=%d, want auth-a fallback once", selected.ID, fallback.picked)
	}
}

func TestCacheAwareSelectorTruncationCallbackRecordsPrefixAndTail(t *testing.T) {
	t.Parallel()

	store := newTestCacheAwareStore(t, time.Hour)
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	selector := NewCacheAwareSelector(&fixedSelector{auth: authA}, store)
	meta := map[string]any{}
	_, errPick := selector.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{OriginalRequest: cacheAwarePayload("hello"), Metadata: meta}, []*Auth{authA})
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	callback, ok := meta[CacheAwareTruncationCallbackMetadataKey].(CacheAwareTruncationCallback)
	if !ok || callback == nil {
		t.Fatal("truncation callback not registered")
	}
	truncated := cacheAwarePayload("truncated")
	callback(truncated)
	fp := store.Fingerprint(oagmsg.FormatOpenAI, truncated)
	if got := store.Lookup(fp.CacheID, "model"); got != "auth-a" {
		t.Fatalf("truncation cache lookup = %q, want auth-a", got)
	}
	if got := store.Lookup(fp.TailID, "model"); got != "auth-a" {
		t.Fatalf("truncation tail lookup = %q, want auth-a", got)
	}
}

func TestCacheAwareSelectorStopClosesStore(t *testing.T) {
	t.Parallel()

	store := newTestCacheAwareStore(t, time.Hour)
	selector := NewCacheAwareSelector(&fixedSelector{auth: &Auth{ID: "auth-a"}}, store)
	selector.Stop()
	store.Append("cache", "model", "auth-a")
	if got := store.Lookup("cache", "model"); got != "" {
		t.Fatalf("lookup after Stop = %q, want empty", got)
	}
}

type managerCacheAwareExecutor struct {
	ids []string
}

func (e *managerCacheAwareExecutor) Identifier() string { return "cache-manager" }

func (e *managerCacheAwareExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.ids = append(e.ids, auth.ID)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *managerCacheAwareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{Message: "stream not implemented"}
}

func (e *managerCacheAwareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *managerCacheAwareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Message: "count not implemented"}
}

func (e *managerCacheAwareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Message: "http not implemented"}
}

func TestManagerExecuteSuccessUpdatesCacheAwareAffinity(t *testing.T) {
	store := newTestCacheAwareStore(t, time.Hour)
	authA := &Auth{ID: "auth-a", Provider: "cache-manager"}
	authB := &Auth{ID: "auth-b", Provider: "cache-manager"}
	fallback := &fixedSelector{auth: authB}
	manager := NewManager(nil, NewCacheAwareSelector(fallback, store), nil)
	executor := &managerCacheAwareExecutor{}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), authA); errRegister != nil {
		t.Fatalf("register auth-a: %v", errRegister)
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), authB); errRegister != nil {
		t.Fatalf("register auth-b: %v", errRegister)
	}

	firstPayload := cacheAwarePayload("hello")
	if _, errExecute := manager.Execute(context.Background(), []string{"cache-manager"}, cliproxyexecutor.Request{Payload: firstPayload}, cliproxyexecutor.Options{OriginalRequest: firstPayload, Metadata: map[string]any{}}); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}
	if len(executor.ids) != 1 || executor.ids[0] != "auth-b" {
		t.Fatalf("first executor ids = %#v, want [auth-b]", executor.ids)
	}

	fallback.auth = authA
	nextPayload := cacheAwareNextPayload("hello")
	if _, errExecute := manager.Execute(context.Background(), []string{"cache-manager"}, cliproxyexecutor.Request{Payload: nextPayload}, cliproxyexecutor.Options{OriginalRequest: nextPayload, Metadata: map[string]any{}}); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}
	if len(executor.ids) != 2 || executor.ids[1] != "auth-b" {
		t.Fatalf("executor ids after cache hit = %#v, want second auth-b", executor.ids)
	}
	if fallback.picked != 1 {
		t.Fatalf("fallback picked = %d, want 1", fallback.picked)
	}
}
