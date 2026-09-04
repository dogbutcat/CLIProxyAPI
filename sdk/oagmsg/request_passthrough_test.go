package oagmsg

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/tidwall/gjson"
)

func TestCodexRequestAlreadyFinalized(t *testing.T) {
	finalized := codexFinalizedRequestForTest()
	tests := []struct {
		name string
		raw  []byte
		want bool
	}{
		{name: "finalized", raw: finalized, want: true},
		{name: "stream false", raw: []byte(`{"model":"gpt-5.4","store":false,"stream":false,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"input":[]}`)},
		{name: "missing include", raw: []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"input":[]}`)},
		{name: "rejected field", raw: []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"temperature":0.2,"input":[]}`)},
		{name: "non priority service tier", raw: []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"service_tier":"default","input":[]}`)},
		{name: "string input", raw: []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"input":"hi"}`)},
		{name: "system role", raw: []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"hi"}]}]}`)},
		{name: "builtin tool alias", raw: []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"tools":[{"type":"web_search_preview","name":"search"}],"input":[]}`)},
		{name: "malformed", raw: []byte(`{"model":`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexRequestAlreadyFinalized(tt.raw); got != tt.want {
				t.Fatalf("codexRequestAlreadyFinalized() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexToCodexFinalizedRequestUsesIdentityPath(t *testing.T) {
	raw := codexFinalizedRequestForTest()
	if path := selectRequestTranslationPath(FormatCodex, FormatCodex, "gpt-5.4", raw, nil); path != requestTranslationPathIdentity {
		t.Fatalf("path = %v, want identity", path)
	}
	got := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.4", raw, false)
	if string(got) != string(raw) {
		t.Fatalf("finalized request changed\ngot:  %s\nwant: %s", got, raw)
	}
	if !sameSliceForTest(raw, got) {
		t.Fatalf("finalized request was copied")
	}
}

func TestCodexToCodexPassthroughGuards(t *testing.T) {
	t.Run("model override finalizes", func(t *testing.T) {
		raw := codexFinalizedRequestForTest()
		got := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.5", raw, false)
		if sameSliceForTest(raw, got) {
			t.Fatal("model override used identity passthrough")
		}
		if model := gjson.GetBytes(got, "model").String(); model != "gpt-5.5" {
			t.Fatalf("model = %q, want gpt-5.5; body=%s", model, got)
		}
	})

	t.Run("unfinalized finalizes", func(t *testing.T) {
		raw := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false,"temperature":0.2}`)
		got := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.4", raw, false)
		if sameSliceForTest(raw, got) {
			t.Fatal("unfinalized request used identity passthrough")
		}
		assertCodexFinalizedRequest(t, got, "hi")
		if gjson.GetBytes(got, "temperature").Exists() {
			t.Fatalf("temperature survived: %s", got)
		}
	})

	t.Run("tool metadata finalizes", func(t *testing.T) {
		longName := strings.Repeat("tool_name_", 10)
		raw := []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"tools":[{"type":"function","name":"` + longName + `","parameters":{"type":"object"}}],"input":[{"type":"function_call","name":"` + longName + `","call_id":"call_1","arguments":"{}"}]}`)
		got := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.4", raw, false)
		if sameSliceForTest(raw, got) {
			t.Fatal("tool metadata request used identity passthrough")
		}
		if name := gjson.GetBytes(got, "tools.0.name").String(); len(name) > codexToolNameLimitBytes {
			t.Fatalf("tool name was not shortened: %q; body=%s", name, got)
		}
		if name := gjson.GetBytes(got, "input.0.name").String(); len(name) > codexToolNameLimitBytes {
			t.Fatalf("history tool name was not shortened: %q; body=%s", name, got)
		}
	})
}

func BenchmarkTranslateRequestCodexToCodexFinalizedNoCopy(b *testing.B) {
	raw := []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("payload ", 16384) + `"}]}]}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.4", raw, false)
		if len(out) != len(raw) {
			b.Fatalf("len(out) = %d, want %d", len(out), len(raw))
		}
	}
}

func BenchmarkTranslateRequestCodexToCodexFinalizeRequired(b *testing.B) {
	raw := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("payload ", 16384) + `"}]}]}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.4", raw, false)
		if len(out) <= len(raw) {
			b.Fatalf("len(out) = %d, want > %d", len(out), len(raw))
		}
	}
}

func codexFinalizedRequestForTest() []byte {
	return []byte(`{"model":"gpt-5.4","store":false,"stream":true,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
}

func sameSliceForTest(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	return unsafe.SliceData(a) == unsafe.SliceData(b)
}
