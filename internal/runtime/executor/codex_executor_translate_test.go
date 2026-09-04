package executor

import (
	"bytes"
	"sync/atomic"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestTranslateCodexRequestPairReusesEqualPayload(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-equal")
	to := sdktranslator.Format("codex-test-to-equal")
	var calls int32
	originalTranslate := translateCodexRequest
	translateCodexRequest = func(gotFrom, gotTo sdktranslator.Format, model string, rawJSON []byte, stream bool) []byte {
		atomic.AddInt32(&calls, 1)
		if gotFrom != from || gotTo != to {
			t.Errorf("formats = %s -> %s, want %s -> %s", gotFrom, gotTo, from, to)
		}
		if model != "test-model" {
			t.Errorf("model = %q, want test-model", model)
		}
		if !stream {
			t.Error("stream = false, want true")
		}
		return append([]byte(nil), rawJSON...)
	}
	t.Cleanup(func() { translateCodexRequest = originalTranslate })

	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body := translateCodexRequestPair(from, to, "test-model", payload, bytes.Clone(payload), true)

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 1 {
		t.Fatalf("TranslateRequest calls = %d, want 1", gotCalls)
	}
	if !bytes.Equal(originalTranslated, body) {
		t.Fatalf("translated payloads differ: original=%s body=%s", originalTranslated, body)
	}
}

func TestTranslateCodexRequestPairTranslatesDifferentPayloads(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-different")
	to := sdktranslator.Format("codex-test-to-different")
	var calls int32
	originalTranslate := translateCodexRequest
	translateCodexRequest = func(_, _ sdktranslator.Format, _ string, rawJSON []byte, _ bool) []byte {
		atomic.AddInt32(&calls, 1)
		return append([]byte(nil), rawJSON...)
	}
	t.Cleanup(func() { translateCodexRequest = originalTranslate })

	originalPayload := []byte(`{"model":"test-model","input":[{"role":"system"}]}`)
	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body := translateCodexRequestPair(from, to, "test-model", originalPayload, payload, false)

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 2 {
		t.Fatalf("TranslateRequest calls = %d, want 2", gotCalls)
	}
	if !bytes.Equal(originalTranslated, originalPayload) {
		t.Fatalf("original translated = %s, want %s", originalTranslated, originalPayload)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body = %s, want %s", body, payload)
	}
}
