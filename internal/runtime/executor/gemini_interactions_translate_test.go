package executor

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type interactionsTranslateHooks struct {
	calls int64
}

func (h *interactionsTranslateHooks) NormalizeRequest(_ context.Context, _, _ oagmsg.Format, _ string, body []byte, _ bool) []byte {
	h.calls++
	updated, errSet := sjson.SetBytes(body, "plugin_call", h.calls)
	if errSet != nil {
		return body
	}
	return updated
}

func (*interactionsTranslateHooks) TranslateRequest(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*interactionsTranslateHooks) NormalizeResponseBefore(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func (*interactionsTranslateHooks) TranslateResponse(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*interactionsTranslateHooks) NormalizeResponseAfter(context.Context, oagmsg.Format, oagmsg.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func TestTranslateGeminiInteractionsRequestPairReusesSameSlice(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	const model = "gemini-3.1-flash-lite"
	payloads := [][]byte{
		[]byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"gemini-3.1-flash-lite","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
	}
	formats := []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatClaude}

	for i, payload := range payloads {
		for _, stream := range []bool{false, true} {
			for _, compat := range []bool{false, true} {
				opts := cliproxyexecutor.Options{SourceFormat: formats[i]}
				base, work := translateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, stream, compat)
				want := translateGeminiInteractionsRequestBody(ctx, cfg, model, payload, opts, stream, compat)
				if !bytes.Equal(base, want) || !bytes.Equal(work, want) {
					t.Fatalf("format=%s stream=%v compat=%v: reused translation differs", formats[i], stream, compat)
				}
				assertIndependentGeminiInteractionsBuffers(t, payload, base, work)
			}
		}
	}
}

func TestTranslateGeminiInteractionsRequestPairBuffers(t *testing.T) {
	if oagmsg.HasPluginHooks() {
		t.Fatal("plugin hooks are installed and disable translation reuse")
	}

	const model = "gemini-interactions-count"
	ctx := context.Background()
	cfg := &config.Config{}
	payload := []byte(`{"model":"gemini-interactions-count","messages":[{"role":"user","content":"hi"}]}`)
	for _, stream := range []bool{false, true} {
		for _, compat := range []bool{false, true} {
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
			want := translateGeminiInteractionsRequestBody(ctx, cfg, model, payload, opts, stream, compat)
			base, work := translateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, stream, compat)
			if !bytes.Equal(base, want) || !bytes.Equal(work, want) {
				t.Fatalf("stream=%v compat=%v: same slice translation mismatch", stream, compat)
			}
			assertIndependentGeminiInteractionsBuffers(t, payload, base, work)

			opts.OriginalRequest = payload
			base, work = translateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, stream, compat)
			if !bytes.Equal(base, want) || !bytes.Equal(work, want) {
				t.Fatalf("stream=%v compat=%v: identical backing translation mismatch", stream, compat)
			}
			assertIndependentGeminiInteractionsBuffers(t, payload, base, work)

			detached := bytes.Clone(payload)
			opts.OriginalRequest = detached
			wantBase := geminiInteractionsPayloadConfigSource(ctx, cfg, model, payload, opts, stream, compat)
			base, work = translateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, stream, compat)
			if !bytes.Equal(base, wantBase) || !bytes.Equal(work, want) {
				t.Fatalf("stream=%v compat=%v: distinct backing translation mismatch", stream, compat)
			}
			assertIndependentGeminiInteractionsBuffers(t, payload, base, work)
		}
	}
}

func TestTranslateGeminiInteractionsRequestPairTranslatesDistinctInputs(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	const model = "gemini-3.1-flash-lite"
	payload := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"working"}]}`)
	originalReq := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"baseline"}]}`)
	// Same bytes in another array must still be translated separately.
	detached := bytes.Clone(payload)

	for _, original := range [][]byte{originalReq, detached} {
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: original}
		base, work := translateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, true, false)
		wantWork := translateGeminiInteractionsRequestBody(ctx, cfg, model, payload, opts, true, false)
		wantBase := geminiInteractionsPayloadConfigSource(ctx, cfg, model, payload, opts, true, false)
		if !bytes.Equal(work, wantWork) || !bytes.Equal(base, wantBase) {
			t.Fatal("distinct inputs did not keep separate translations")
		}
		assertIndependentGeminiInteractionsBuffers(t, payload, base, work)
	}
}

func TestTranslateGeminiInteractionsRequestPairPreservesHookOrder(t *testing.T) {
	hooks := &interactionsTranslateHooks{}
	oagmsg.SetPluginHooks(hooks)
	t.Cleanup(func() { oagmsg.SetPluginHooks(nil) })

	ctx := context.Background()
	cfg := &config.Config{}
	const model = "gemini-3.1-flash-lite"
	payload := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"same"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload}
	base, work := translateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, false, true)
	if hooks.calls != 2 {
		t.Fatalf("plugin hook calls = %d, want 2", hooks.calls)
	}
	if got := gjson.GetBytes(work, "plugin_call").Int(); got != 1 {
		t.Fatalf("working plugin_call = %d, want 1", got)
	}
	if got := gjson.GetBytes(base, "plugin_call").Int(); got != 2 {
		t.Fatalf("baseline plugin_call = %d, want 2", got)
	}
	assertIndependentGeminiInteractionsBuffers(t, payload, base, work)

	detached := bytes.Clone(payload)
	opts.OriginalRequest = detached
	before := hooks.calls
	base, work = translateGeminiInteractionsRequestPair(ctx, cfg, model, payload, opts, true, false)
	if hooks.calls != before+2 {
		t.Fatalf("distinct plugin hook calls = %d, want %d", hooks.calls-before, 2)
	}
	if got := gjson.GetBytes(work, "plugin_call").Int(); got != before+1 {
		t.Fatalf("distinct working plugin_call = %d, want %d", got, before+1)
	}
	if got := gjson.GetBytes(base, "plugin_call").Int(); got != before+2 {
		t.Fatalf("distinct baseline plugin_call = %d, want %d", got, before+2)
	}
}

func TestTranslateGeminiInteractionsRequestPairNativeCopy(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	payload := []byte(`{"model":"gemini-3.1-flash-lite","input":"hi"}`)
	for _, format := range []sdktranslator.Format{"", sdktranslator.FormatInteractions} {
		opts := cliproxyexecutor.Options{SourceFormat: format}
		base, work := translateGeminiInteractionsRequestPair(ctx, cfg, "gemini-3.1-flash-lite", payload, opts, false, false)
		if !bytes.Equal(base, payload) || !bytes.Equal(work, payload) {
			t.Fatalf("format=%q: native interactions payload was translated", format)
		}
		assertIndependentGeminiInteractionsBuffers(t, payload, base, work)

		originalReq := []byte(`{"model":"gemini-3.1-flash-lite","input":"original"}`)
		opts.OriginalRequest = originalReq
		base, work = translateGeminiInteractionsRequestPair(ctx, cfg, "gemini-3.1-flash-lite", payload, opts, true, true)
		if !bytes.Equal(work, payload) || !bytes.Equal(base, originalReq) {
			t.Fatalf("format=%q: distinct native inputs were not copied independently", format)
		}
		assertIndependentGeminiInteractionsBuffers(t, payload, base, work)
	}
}

func TestTranslateGeminiInteractionsRequestPairNativeCopyIgnoresHooks(t *testing.T) {
	hooks := &interactionsTranslateHooks{}
	oagmsg.SetPluginHooks(hooks)
	t.Cleanup(func() { oagmsg.SetPluginHooks(nil) })

	payload := []byte(`{"model":"gemini-3.1-flash-lite","input":"hi"}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, OriginalRequest: payload}
	base, work := translateGeminiInteractionsRequestPair(context.Background(), &config.Config{}, "gemini-3.1-flash-lite", payload, opts, true, false)
	if hooks.calls != 0 {
		t.Fatalf("native copy invoked plugin hooks %d times", hooks.calls)
	}
	if !bytes.Equal(base, payload) || !bytes.Equal(work, payload) {
		t.Fatal("native interactions payload changed")
	}
	assertIndependentGeminiInteractionsBuffers(t, payload, base, work)
}

func assertIndependentGeminiInteractionsBuffers(t *testing.T, input, base, work []byte) {
	t.Helper()
	if len(base) > 0 && len(work) > 0 && &base[0] == &work[0] {
		t.Fatal("working buffer aliases the baseline")
	}
	if len(work) > 0 && len(input) > 0 && &work[0] == &input[0] {
		t.Fatal("working buffer aliases the input")
	}
	if len(work) == 0 {
		return
	}
	baselineBefore := bytes.Clone(base)
	inputBefore := bytes.Clone(input)
	work[0] = 'X'
	if !bytes.Equal(baselineBefore, base) {
		t.Fatal("mutating the working buffer changed the baseline")
	}
	if !bytes.Equal(inputBefore, input) {
		t.Fatal("mutating the working buffer changed the input")
	}
}
