package cliproxy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type pluginHookEvent struct {
	name     string
	from     string
	to       string
	model    string
	body     string
	original string
	request  string
	stream   bool
}

type recordingTranslatorHooks struct {
	events                  *[]pluginHookEvent
	normalizeRequest        func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) []byte
	translateRequest        func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool)
	normalizeResponseBefore func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte
	translateResponse       func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool)
	normalizeResponseAfter  func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte
}

func (h *recordingTranslatorHooks) record(name string, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) {
	if h.events == nil {
		return
	}
	*h.events = append(*h.events, pluginHookEvent{
		name:     name,
		from:     string(from),
		to:       string(to),
		model:    model,
		body:     string(body),
		original: string(originalRequestRawJSON),
		request:  string(requestRawJSON),
		stream:   stream,
	})
}

func (h *recordingTranslatorHooks) NormalizeRequest(ctx context.Context, from, to sdktranslator.Format, model string, body []byte, stream bool) []byte {
	h.record("NormalizeRequest", from, to, model, nil, nil, body, stream)
	if h.normalizeRequest != nil {
		return h.normalizeRequest(ctx, from, to, model, body, stream)
	}
	return body
}

func (h *recordingTranslatorHooks) TranslateRequest(ctx context.Context, from, to sdktranslator.Format, model string, body []byte, stream bool) ([]byte, bool) {
	h.record("TranslateRequest", from, to, model, nil, nil, body, stream)
	if h.translateRequest != nil {
		return h.translateRequest(ctx, from, to, model, body, stream)
	}
	return nil, false
}

func (h *recordingTranslatorHooks) NormalizeResponseBefore(ctx context.Context, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	h.record("NormalizeResponseBefore", from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	if h.normalizeResponseBefore != nil {
		return h.normalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	}
	return body
}

func (h *recordingTranslatorHooks) TranslateResponse(ctx context.Context, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool) {
	h.record("TranslateResponse", from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	if h.translateResponse != nil {
		return h.translateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	}
	return nil, false
}

func (h *recordingTranslatorHooks) NormalizeResponseAfter(ctx context.Context, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	h.record("NormalizeResponseAfter", from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	if h.normalizeResponseAfter != nil {
		return h.normalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	}
	return body
}

type recordingOAGMsgHooks struct {
	events                  *[]pluginHookEvent
	normalizeRequest        func(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, bool) []byte
	translateRequest        func(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, bool) ([]byte, bool)
	normalizeResponseBefore func(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, []byte, []byte, bool) []byte
	translateResponse       func(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, []byte, []byte, bool) ([]byte, bool)
	normalizeResponseAfter  func(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, []byte, []byte, bool) []byte
}

func (h *recordingOAGMsgHooks) record(name string, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) {
	if h.events == nil {
		return
	}
	*h.events = append(*h.events, pluginHookEvent{
		name:     name,
		from:     string(from),
		to:       string(to),
		model:    model,
		body:     string(body),
		original: string(originalRequestRawJSON),
		request:  string(requestRawJSON),
		stream:   stream,
	})
}

func (h *recordingOAGMsgHooks) NormalizeRequest(ctx context.Context, from, to oagmsg.Format, model string, body []byte, stream bool) []byte {
	h.record("NormalizeRequest", from, to, model, nil, nil, body, stream)
	if h.normalizeRequest != nil {
		return h.normalizeRequest(ctx, from, to, model, body, stream)
	}
	return body
}

func (h *recordingOAGMsgHooks) TranslateRequest(ctx context.Context, from, to oagmsg.Format, model string, body []byte, stream bool) ([]byte, bool) {
	h.record("TranslateRequest", from, to, model, nil, nil, body, stream)
	if h.translateRequest != nil {
		return h.translateRequest(ctx, from, to, model, body, stream)
	}
	return nil, false
}

func (h *recordingOAGMsgHooks) NormalizeResponseBefore(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	h.record("NormalizeResponseBefore", from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	if h.normalizeResponseBefore != nil {
		return h.normalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	}
	return body
}

func (h *recordingOAGMsgHooks) TranslateResponse(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool) {
	h.record("TranslateResponse", from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	if h.translateResponse != nil {
		return h.translateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	}
	return nil, false
}

func (h *recordingOAGMsgHooks) NormalizeResponseAfter(ctx context.Context, from, to oagmsg.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	h.record("NormalizeResponseAfter", from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	if h.normalizeResponseAfter != nil {
		return h.normalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, stream)
	}
	return body
}

type directOAGMsgPluginProvider struct {
	t      *testing.T
	native oagmsg.PluginHooks
}

func (h *directOAGMsgPluginProvider) OAGMsgHooks() oagmsg.PluginHooks {
	return h.native
}

func (h *directOAGMsgPluginProvider) NormalizeRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) []byte {
	h.t.Fatal("legacy NormalizeRequest should not be called when OAGMsgHooks is available")
	return nil
}

func (h *directOAGMsgPluginProvider) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	h.t.Fatal("legacy TranslateRequest should not be called when OAGMsgHooks is available")
	return nil, false
}

func (h *directOAGMsgPluginProvider) NormalizeResponseBefore(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	h.t.Fatal("legacy NormalizeResponseBefore should not be called when OAGMsgHooks is available")
	return nil
}

func (h *directOAGMsgPluginProvider) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	h.t.Fatal("legacy TranslateResponse should not be called when OAGMsgHooks is available")
	return nil, false
}

func (h *directOAGMsgPluginProvider) NormalizeResponseAfter(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	h.t.Fatal("legacy NormalizeResponseAfter should not be called when OAGMsgHooks is available")
	return nil
}

func cleanupOAGMsgPluginHooks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		setTranslationPluginHooks(nil)
	})
}

func TestOAGMsgPluginHooksNilCleanup(t *testing.T) {
	cleanupOAGMsgPluginHooks(t)

	setTranslationPluginHooks(&recordingTranslatorHooks{
		translateRequest: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
			return []byte(`{"leaked":true}`), true
		},
	})
	withHooks := oagmsg.TranslateRequest(oagmsg.Format("nil-cleanup-from"), oagmsg.Format("nil-cleanup-to"), "cleanup-model", []byte(`{"model":"cleanup-model"}`), false)
	if !bytes.Contains(withHooks, []byte(`"leaked":true`)) {
		t.Fatalf("TranslateRequest() with hooks = %s, want plugin output", withHooks)
	}

	setTranslationPluginHooks(nil)
	withoutHooks := oagmsg.TranslateRequest(oagmsg.Format("nil-cleanup-from"), oagmsg.Format("nil-cleanup-to"), "cleanup-model", []byte(`{"model":"cleanup-model"}`), false)
	if bytes.Contains(withoutHooks, []byte(`"leaked":true`)) {
		t.Fatalf("TranslateRequest() after nil cleanup = %s, still used previous plugin hooks", withoutHooks)
	}
	if string(withoutHooks) != `{"model":"cleanup-model"}` {
		t.Fatalf("TranslateRequest() after nil cleanup = %s, want original request with runtime model", withoutHooks)
	}
}

func TestOAGMsgPluginHooksDirectProviderSelection(t *testing.T) {
	cleanupOAGMsgPluginHooks(t)

	var events []pluginHookEvent
	native := &recordingOAGMsgHooks{
		events: &events,
		translateRequest: func(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, bool) ([]byte, bool) {
			return []byte(`{"provider":"native"}`), true
		},
	}
	setTranslationPluginHooks(&directOAGMsgPluginProvider{t: t, native: native})

	got := oagmsg.TranslateRequest(oagmsg.Format("direct-from"), oagmsg.Format("direct-to"), "direct-model", []byte(`{"model":"direct-model"}`), false)
	if string(got) != `{"provider":"native"}` {
		t.Fatalf("TranslateRequest() = %s, want native hook output", got)
	}

	want := []pluginHookEvent{
		{name: "NormalizeRequest", from: "direct-from", to: "direct-to", model: "direct-model", body: `{"model":"direct-model"}`},
		{name: "TranslateRequest", from: "direct-from", to: "direct-to", model: "direct-model", body: `{"model":"direct-model"}`},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("native hook events = %#v, want %#v", events, want)
	}
}

func TestOAGMsgPluginHooksLegacyAdapterRequestFallback(t *testing.T) {
	cleanupOAGMsgPluginHooks(t)

	var events []pluginHookEvent
	setTranslationPluginHooks(&recordingTranslatorHooks{
		events: &events,
		normalizeRequest: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) []byte {
			return []byte(`{"stage":"normalized","model":"legacy-model"}`)
		},
		translateRequest: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
			return []byte(`{"stage":"translated","model":"legacy-model"}`), true
		},
	})

	got := oagmsg.TranslateRequest(oagmsg.Format("legacy-request-from"), oagmsg.Format("legacy-request-to"), "legacy-model", []byte(`{"stage":"raw","model":"legacy-model"}`), true)
	if string(got) != `{"stage":"translated","model":"legacy-model"}` {
		t.Fatalf("TranslateRequest() = %s, want legacy fallback translation", got)
	}

	want := []pluginHookEvent{
		{name: "NormalizeRequest", from: "legacy-request-from", to: "legacy-request-to", model: "legacy-model", body: `{"stage":"raw","model":"legacy-model"}`, stream: true},
		{name: "TranslateRequest", from: "legacy-request-from", to: "legacy-request-to", model: "legacy-model", body: `{"stage":"normalized","model":"legacy-model"}`, stream: true},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("legacy request hook events = %#v, want %#v", events, want)
	}
}

func TestOAGMsgPluginHooksLegacyAdapterNonStreamResponseFallbackOrder(t *testing.T) {
	cleanupOAGMsgPluginHooks(t)

	var events []pluginHookEvent
	original := []byte(`{"request":"original"}`)
	transformed := []byte(`{"request":"transformed"}`)
	setTranslationPluginHooks(&recordingTranslatorHooks{
		events: &events,
		normalizeResponseBefore: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
			return []byte(`{"stage":"before"}`)
		},
		translateResponse: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
			return []byte(`{"stage":"translated"}`), true
		},
		normalizeResponseAfter: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
			return []byte(`{"stage":"after"}`)
		},
	})

	got := oagmsg.TranslateNonStream(context.Background(), oagmsg.Format("legacy-response-from"), oagmsg.Format("legacy-response-to"), "response-model", original, transformed, []byte(`{"stage":"raw"}`), nil)
	if string(got) != `{"stage":"after"}` {
		t.Fatalf("TranslateNonStream() = %s, want NormalizeAfter output", got)
	}

	want := []pluginHookEvent{
		{name: "NormalizeResponseBefore", from: "legacy-response-from", to: "legacy-response-to", model: "response-model", body: `{"stage":"raw"}`, original: string(original), request: string(transformed)},
		{name: "TranslateResponse", from: "legacy-response-from", to: "legacy-response-to", model: "response-model", body: `{"stage":"before"}`, original: string(original), request: string(transformed)},
		{name: "NormalizeResponseAfter", from: "legacy-response-from", to: "legacy-response-to", model: "response-model", body: `{"stage":"translated"}`, original: string(original), request: string(transformed)},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("legacy non-stream response hook events = %#v, want %#v", events, want)
	}
}

func TestOAGMsgPluginHooksLegacyAdapterStreamResponseFallback(t *testing.T) {
	cleanupOAGMsgPluginHooks(t)

	var events []pluginHookEvent
	original := []byte(`{"stream_request":"original"}`)
	transformed := []byte(`{"stream_request":"transformed"}`)
	setTranslationPluginHooks(&recordingTranslatorHooks{
		events: &events,
		normalizeResponseBefore: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
			return []byte(`{"chunk":"before"}`)
		},
		translateResponse: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
			return []byte(`{"chunk":"translated"}`), true
		},
		normalizeResponseAfter: func(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
			return []byte(`{"chunk":"after"}`)
		},
	})

	var param any
	got := oagmsg.TranslateStream(context.Background(), oagmsg.Format("legacy-stream-from"), oagmsg.Format("legacy-stream-to"), "stream-model", original, transformed, []byte(`{"chunk":"raw"}`), &param)
	if len(got) != 1 || string(got[0]) != `{"chunk":"after"}` {
		t.Fatalf("TranslateStream() = %q, want single NormalizeAfter output", got)
	}

	want := []pluginHookEvent{
		{name: "NormalizeResponseBefore", from: "legacy-stream-from", to: "legacy-stream-to", model: "stream-model", body: `{"chunk":"raw"}`, original: string(original), request: string(transformed), stream: true},
		{name: "TranslateResponse", from: "legacy-stream-from", to: "legacy-stream-to", model: "stream-model", body: `{"chunk":"before"}`, original: string(original), request: string(transformed), stream: true},
		{name: "NormalizeResponseAfter", from: "legacy-stream-from", to: "legacy-stream-to", model: "stream-model", body: `{"chunk":"translated"}`, original: string(original), request: string(transformed), stream: true},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("legacy stream response hook events = %#v, want %#v", events, want)
	}
}

func TestProductionGoFilesDoNotCallTranslatorPluginHooks(t *testing.T) {
	cleanupOAGMsgPluginHooks(t)

	root := repositoryRoot(t)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte("sdktranslator.SetPluginHooks(")) {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				relative = path
			}
			offenders = append(offenders, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go files: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("production sdktranslator.SetPluginHooks calls found in: %s", strings.Join(offenders, ", "))
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root with go.mod not found")
		}
		dir = parent
	}
}
