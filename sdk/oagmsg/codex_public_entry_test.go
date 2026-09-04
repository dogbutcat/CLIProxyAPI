package oagmsg

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCodexPublicEntryBuiltinToolsProduceConsistentFinalPayloads(t *testing.T) {
	const model = "gpt-5.5"
	tests := []struct {
		name                 string
		instructionsRaw      string
		wantInstructions     string
		wantInstructionsSeen bool
	}{
		{name: "instructions_absent"},
		{name: "instructions_empty", instructionsRaw: `""`, wantInstructionsSeen: true},
		{name: "instructions_non_empty", instructionsRaw: `"be exact"`, wantInstructions: "be exact", wantInstructionsSeen: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := codexPublicEntryRequestRaw(tt.instructionsRaw)
			runtime := TranslateRequest(FormatCodex, FormatCodex, model, raw, false)
			builder, err := From(FormatCodex).Request(raw).ToWithModel(FormatCodex, model)
			if err != nil {
				t.Fatalf("builder entry error = %v", err)
			}
			registry, err := DefaultRegistry().Translate(FormatCodex, FormatCodex, raw)
			if err != nil {
				t.Fatalf("registry entry error = %v", err)
			}
			registry, _ = sjson.SetBytes(registry, "model", model)
			registry = FinalizeCodexRequest(registry)

			for name, body := range map[string][]byte{
				"runtime":  runtime,
				"builder":  builder,
				"registry": registry,
			} {
				t.Run(name, func(t *testing.T) {
					assertCodexPublicEntryFinalPayload(t, body, tt.wantInstructions, tt.wantInstructionsSeen)
				})
			}
			assertJSONSemanticEqualBytes(t, runtime, builder)
			assertJSONSemanticEqualBytes(t, runtime, registry)
		})
	}
}

func codexPublicEntryRequestRaw(instructionsRaw string) []byte {
	instructions := ""
	if instructionsRaw != "" {
		instructions = `"instructions":` + instructionsRaw + `,`
	}
	return []byte(`{
		"model":"source-model",
		` + instructions + `
		"input":"entry text",
		"temperature":0.5,
		"top_p":0.9,
		"user":"bad-user",
		"context_management":{"compaction":"auto"},
		"tools":[{"type":"web_search_preview","name":"search"}],
		"custom_field":"kept"
	}`)
}

func assertCodexPublicEntryFinalPayload(t *testing.T, body []byte, wantInstructions string, wantInstructionsSeen bool) {
	t.Helper()
	root := gjson.ParseBytes(body)
	if got := root.Get("tools.0.type").String(); got != "web_search" {
		t.Fatalf("builtin tool type = %q, want web_search; body=%s", got, body)
	}
	if got := root.Get("tools.0.name").String(); got != "search" {
		t.Fatalf("builtin tool name = %q, want search; body=%s", got, body)
	}
	if root.Get("tools.0.function").Exists() {
		t.Fatalf("builtin tool became a function declaration: %s", body)
	}
	instructions := root.Get("instructions")
	if instructions.Exists() != wantInstructionsSeen {
		t.Fatalf("instructions presence = %v, want %v; body=%s", instructions.Exists(), wantInstructionsSeen, body)
	}
	if wantInstructionsSeen && instructions.String() != wantInstructions {
		t.Fatalf("instructions = %q, want %q; body=%s", instructions.String(), wantInstructions, body)
	}
	if text := root.Get("input.0.content.0.text").String(); text != "entry text" {
		t.Fatalf("input text = %q, want entry text; body=%s", text, body)
	}
	if count := countCodexInputRole(root, "developer"); count != 0 {
		t.Fatalf("synthetic instructions duplicated as developer input count=%d; body=%s", count, body)
	}
	if field := root.Get("custom_field").String(); field != "kept" {
		t.Fatalf("custom_field = %q, want kept; body=%s", field, body)
	}
	for _, path := range []string{"temperature", "top_p", "user", "context_management"} {
		if root.Get(path).Exists() {
			t.Fatalf("rejected field %q survived: %s", path, body)
		}
	}
}

func countCodexInputRole(root gjson.Result, role string) int {
	count := 0
	for _, item := range root.Get("input").Array() {
		if item.Get("role").String() == role {
			count++
		}
	}
	return count
}

func assertJSONSemanticEqualBytes(t *testing.T, want, got []byte) {
	t.Helper()
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("semantic JSON mismatch\nwant: %s\ngot:  %s", want, got)
	}
}
