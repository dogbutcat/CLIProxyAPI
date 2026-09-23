package helps

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCompatibilityRequestPair(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	from, to := sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI
	for _, compat := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, distinct := range []bool{false, true} {
				original := []byte(`{"model":"test","input":"hello"}`)
				request := original
				if distinct {
					request = []byte(`{"model":"test","input":"changed"}`)
				}
				wantOriginal := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, "test", original, stream, compat)
				wantWorking := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, "test", request, stream, compat)
				base, work := TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, "test", original, request, stream, compat)
				if !bytes.Equal(base, wantOriginal) || !bytes.Equal(work, wantWorking) {
					t.Fatalf("pair changed translation: compat=%v stream=%v distinct=%v", compat, stream, distinct)
				}
				work[0] = '!'
				if !bytes.Equal(base, wantOriginal) || original[0] != '{' {
					t.Fatal("working buffer aliases baseline or input")
				}
			}
		}
	}
}

func TestTranslateRequestPairWithAPIKeyModelCompatibilityBuffers(t *testing.T) {
	if oagmsg.HasPluginHooks() {
		t.Fatal("plugin hooks are installed and disable translation reuse")
	}

	ctx := context.Background()
	cfg := &config.Config{}
	from := sdktranslator.FormatOpenAIResponse
	to := sdktranslator.FormatOpenAI
	const model = "compat-count-model"
	payload := []byte(`{"model":"compat-count-model","input":"hello"}`)
	for _, stream := range []bool{false, true} {
		for _, compat := range []bool{false, true} {
			want := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, stream, compat)
			base, work := TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, payload, stream, compat)
			if !bytes.Equal(base, want) || !bytes.Equal(work, want) {
				t.Fatalf("stream=%v compat=%v: same slice translation mismatch", stream, compat)
			}
			if len(base) == 0 || &base[0] == &work[0] {
				t.Fatal("working buffer aliases the baseline")
			}
			baselineBefore := bytes.Clone(base)
			work[0] = 'X'
			if !bytes.Equal(base, baselineBefore) || payload[0] != '{' {
				t.Fatal("mutating the working buffer changed the baseline or input")
			}

			detached := bytes.Clone(payload)
			wantWork := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, detached, stream, compat)
			base, work = TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, detached, stream, compat)
			if !bytes.Equal(base, want) || !bytes.Equal(work, wantWork) {
				t.Fatalf("stream=%v compat=%v: distinct backing translation mismatch", stream, compat)
			}
			if len(base) == 0 || &base[0] == &work[0] {
				t.Fatal("distinct working buffer aliases the baseline")
			}
		}
	}
}

func TestCompatibilityRequestPairPreservesHooks(t *testing.T) {
	hooks := &pairRequestPluginHooks{}
	oagmsg.SetPluginHooks(hooks)
	t.Cleanup(func() { oagmsg.SetPluginHooks(nil) })
	request := []byte(`{"model":"test","input":"hello"}`)
	base, work := TranslateRequestPairWithAPIKeyModelCompatibility(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "test", request, request, true, true)
	if hooks.calls != 2 || bytes.Equal(base, work) {
		t.Fatalf("stateful plugin calls must remain independent, calls=%d", hooks.calls)
	}
}
