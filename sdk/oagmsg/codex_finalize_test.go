package oagmsg

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// =====================================================
// C1: Required Codex fields per direction
// =====================================================

func TestCodexFinalize_RequiredFields_AllDirections(t *testing.T) {
	directions := []struct {
		name   string
		from   Format
		source string
	}{
		{"OpenAI→Codex", FormatOpenAI, `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`},
		{"OpenAIResponses→Codex", FormatOpenAIResponse, `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
		{"Claude→Codex", FormatAnthropic, `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`},
		{"Gemini→Codex", FormatGemini, `{"model":"gpt-5.4","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{"Codex→Codex", FormatCodex, `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
	}

	for _, d := range directions {
		t.Run(d.name, func(t *testing.T) {
			result := TranslateRequest(d.from, FormatCodex, "gpt-5.4", []byte(d.source), false)
			root := gjson.ParseBytes(result)

			// Required fields
			if !root.Get("store").Exists() || root.Get("store").Bool() != false {
				t.Errorf("store must be false, got: %v", root.Get("store").Raw)
			}
			if !root.Get("stream").Exists() || root.Get("stream").Bool() != true {
				t.Errorf("stream must be true, got: %v", root.Get("stream").Raw)
			}
			if !root.Get("parallel_tool_calls").Exists() || root.Get("parallel_tool_calls").Bool() != true {
				t.Errorf("parallel_tool_calls must be true, got: %v", root.Get("parallel_tool_calls").Raw)
			}
			include := root.Get("include")
			if !include.IsArray() || len(include.Array()) != 1 || include.Array()[0].String() != "reasoning.encrypted_content" {
				t.Errorf("include must be [\"reasoning.encrypted_content\"], got: %s", include.Raw)
			}
		})
	}
}

// =====================================================
// C2: Rejected fields stripped
// =====================================================

func TestCodexFinalize_RejectedFieldsStripped(t *testing.T) {
	// Source with all rejected fields present
	source := `{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"max_output_tokens":1024,
		"max_completion_tokens":2048,
		"temperature":0.7,
		"top_p":0.9,
		"truncation":"auto",
		"prompt_cache_options":{"mode":"implicit"},
		"prompt_cache_retention":"24h",
		"user":"test-user",
		"context_management":{"compaction":"auto"},
		"service_tier":"default"
	}`

	result := TranslateRequest(FormatOpenAIResponse, FormatCodex, "gpt-5.4", []byte(source), false)
	root := gjson.ParseBytes(result)

	rejected := []string{
		"max_output_tokens",
		"max_completion_tokens",
		"temperature",
		"top_p",
		"truncation",
		"prompt_cache_options",
		"prompt_cache_retention",
		"user",
		"context_management",
	}

	for _, field := range rejected {
		if root.Get(field).Exists() {
			t.Errorf("rejected field %q must be stripped, but found: %s", field, root.Get(field).Raw)
		}
	}

	// service_tier with non-priority value must be stripped
	if root.Get("service_tier").Exists() {
		t.Errorf("service_tier='default' must be stripped, but found: %s", root.Get("service_tier").Raw)
	}
}

func TestCodexFinalize_ServiceTierPriorityKept(t *testing.T) {
	source := `{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"service_tier":"priority"
	}`

	result := TranslateRequest(FormatOpenAIResponse, FormatCodex, "gpt-5.4", []byte(source), false)
	root := gjson.ParseBytes(result)

	if !root.Get("service_tier").Exists() || root.Get("service_tier").String() != "priority" {
		t.Errorf("service_tier='priority' must be kept, got: %v", root.Get("service_tier").Raw)
	}
}

// =====================================================
// C2b: Rejected fields cannot survive via preservation
// =====================================================

func TestCodexFinalize_PreservationCannotReaddRejectedFields(t *testing.T) {
	// This tests the cross-protocol path where preserveUnknownFields merges
	// source fields back into the target. Codex finalization must run AFTER
	// preservation to strip them.
	source := `{
		"model":"gpt-5.4",
		"messages":[{"role":"user","content":"hi"}],
		"max_output_tokens":1024,
		"temperature":0.7,
		"top_p":0.9,
		"custom_field":"should_survive"
	}`

	result := TranslateRequest(FormatOpenAI, FormatCodex, "gpt-5.4", []byte(source), false)
	root := gjson.ParseBytes(result)

	// Rejected fields must not survive even through preservation
	for _, field := range []string{"max_output_tokens", "temperature", "top_p"} {
		if root.Get(field).Exists() {
			t.Errorf("rejected field %q survived preservation, value: %s", field, root.Get(field).Raw)
		}
	}

	// Required fields must be present
	if root.Get("store").Bool() != false {
		t.Error("store must be false after preservation path")
	}
}

// =====================================================
// C3: Preservation tests — supported fields survive
// =====================================================

func TestCodexFinalize_SupportedFieldsSurvive(t *testing.T) {
	source := `{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"prompt_cache_key":"cache123",
		"client_metadata":{"key":"value"},
		"tools":[{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object"}}],
		"reasoning":{"effort":"high"}
	}`

	result := TranslateRequest(FormatOpenAIResponse, FormatCodex, "gpt-5.4", []byte(source), false)
	root := gjson.ParseBytes(result)

	// These should survive
	if root.Get("model").String() != "gpt-5.4" {
		t.Errorf("model must survive, got: %s", root.Get("model").String())
	}
	if !root.Get("tools").IsArray() {
		t.Error("tools must survive")
	}
}

// =====================================================
// C4: Entry-point consistency
// =====================================================

func TestCodexFinalize_SameFamilyPath(t *testing.T) {
	// OpenAI Responses → Codex (same-family path)
	source := `{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply: OAGMSG_OK"}]}],
		"max_output_tokens":32,
		"stream":false
	}`

	result := TranslateRequest(FormatOpenAIResponse, FormatCodex, "gpt-5.4", []byte(source), false)
	root := gjson.ParseBytes(result)

	// This is the exact scenario from the bug report
	if root.Get("store").Bool() != false {
		t.Error("store must be false (same-family path)")
	}
	if root.Get("stream").Bool() != true {
		t.Error("stream must be true even when source requested false")
	}
	if root.Get("max_output_tokens").Exists() {
		t.Error("max_output_tokens must be stripped")
	}
}

func TestCodexFinalize_CodexToCodex(t *testing.T) {
	// Codex → Codex (same format, same-family path)
	source := `{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"temperature":0.5,
		"stream":false
	}`

	result := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.4", []byte(source), false)
	root := gjson.ParseBytes(result)

	if root.Get("store").Bool() != false {
		t.Error("store must be false (Codex→Codex)")
	}
	if root.Get("stream").Bool() != true {
		t.Error("stream must be true (Codex→Codex)")
	}
	if root.Get("temperature").Exists() {
		t.Error("temperature must be stripped (Codex→Codex)")
	}
}

// =====================================================
// C5: FinalizeCodexRequest unit tests
// =====================================================

func TestFinalizeCodexRequest_Unit(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"max_output_tokens":1024,
		"temperature":0.7,
		"top_p":0.9,
		"truncation":"auto",
		"prompt_cache_options":{"mode":"implicit"},
		"prompt_cache_retention":"24h",
		"user":"test",
		"context_management":{"compaction":"auto"},
		"service_tier":"default",
		"stream":false
	}`)

	result := FinalizeCodexRequest(body)
	root := gjson.ParseBytes(result)

	// Required fields set
	if root.Get("store").Bool() != false {
		t.Error("store must be false")
	}
	if root.Get("stream").Bool() != true {
		t.Error("stream must be true")
	}
	if root.Get("parallel_tool_calls").Bool() != true {
		t.Error("parallel_tool_calls must be true")
	}

	// Rejected fields removed
	for _, f := range []string{"max_output_tokens", "temperature", "top_p", "truncation", "prompt_cache_options", "prompt_cache_retention", "user", "context_management", "service_tier"} {
		if root.Get(f).Exists() {
			t.Errorf("field %q must be removed", f)
		}
	}

	// Include set
	inc := root.Get("include")
	if !inc.IsArray() || len(inc.Array()) != 1 || inc.Array()[0].String() != "reasoning.encrypted_content" {
		t.Errorf("include wrong: %s", inc.Raw)
	}
}

func TestFinalizeCodexRequest_ForcesNormalModeForAlternatePayloads(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
	}`)

	result := FinalizeCodexRequest(body)
	root := gjson.ParseBytes(result)

	if got := root.Get("stream"); got.Type != gjson.True {
		t.Fatalf("stream = %s, want true", got.Raw)
	}
	if got := root.Get("store"); got.Type != gjson.False {
		t.Fatalf("store = %s, want false", got.Raw)
	}
	if got := root.Get("parallel_tool_calls"); got.Type != gjson.True {
		t.Fatalf("parallel_tool_calls = %s, want true", got.Raw)
	}
	include := root.Get("include").Array()
	if len(include) != 1 || include[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want reasoning.encrypted_content only", root.Get("include").Raw)
	}
}

func TestFinalizeCodexRequest_BuiltinToolNormalization(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}],
		"tools":[
			{"type":"web_search_preview","name":"ws"},
			{"type":"function","name":"get_info"}
		]
	}`)

	result := FinalizeCodexRequest(body)
	root := gjson.ParseBytes(result)

	tools := root.Get("tools").Array()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Get("type").String() != "web_search" {
		t.Errorf("web_search_preview must be normalized to web_search, got: %s", tools[0].Get("type").String())
	}
	if tools[1].Get("type").String() != "function" {
		t.Errorf("function type must be preserved, got: %s", tools[1].Get("type").String())
	}
}

func TestFinalizeCodexRequest_SystemToDeveloper(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"You are helpful"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
		]
	}`)

	result := FinalizeCodexRequest(body)
	root := gjson.ParseBytes(result)

	items := root.Get("input").Array()
	if len(items) != 2 {
		t.Fatalf("expected 2 input items, got %d", len(items))
	}
	if items[0].Get("role").String() != "developer" {
		t.Errorf("system role must become developer, got: %s", items[0].Get("role").String())
	}
	if items[1].Get("role").String() != "user" {
		t.Errorf("user role must be preserved, got: %s", items[1].Get("role").String())
	}
}

func TestFinalizeCodexRequest_Idempotent(t *testing.T) {
	// Applying finalization twice should produce the same result
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
	}`)

	first := FinalizeCodexRequest(body)
	second := FinalizeCodexRequest(first)

	if string(first) != string(second) {
		t.Errorf("FinalizeCodexRequest is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// =====================================================
// C6: Bug-report exact scenario
// =====================================================

func TestCodexFinalize_ExactBugReportScenario(t *testing.T) {
	// Exact request from task document that returned "Store must be set to false"
	source := `{
		"model":"gpt-5.4",
		"input":[{
			"type":"message",
			"role":"user",
			"content":[{"type":"input_text","text":"Reply with exactly: OAGMSG_OK"}]
		}],
		"max_output_tokens":32,
		"stream":false
	}`

	result := TranslateRequest(FormatOpenAIResponse, FormatCodex, "gpt-5.4", []byte(source), false)
	root := gjson.ParseBytes(result)

	// The exact assertion: no "Store must be set to false" error
	if root.Get("store").Bool() != false {
		t.Fatal("REGRESSION: store not set to false — would cause 'Store must be set to false' error")
	}
	if root.Get("max_output_tokens").Exists() {
		t.Fatal("REGRESSION: max_output_tokens not stripped — would cause unsupported parameter error")
	}
	if root.Get("stream").Bool() != true {
		t.Fatal("stream must be forced to true for Codex upstream")
	}
}

func TestFinalizeCodexRequest_StringInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello codex","stream":false}`)

	result := FinalizeCodexRequest(body)

	assertCodexFinalizedRequest(t, result, "hello codex")
}

func TestCodexFinalize_PluginNormalizeCannotReaddRejectedFields(t *testing.T) {
	SetPluginHooks(codexFinalizePluginHook{addRejectedInNormalize: true})
	defer SetPluginHooks(nil)

	result := TranslateRequest(FormatOpenAI, FormatCodex, "gpt-5.4", []byte(`{
		"model":"gpt-5.4",
		"messages":[{"role":"user","content":"hi"}],
		"custom_field":"kept"
	}`), false)

	assertCodexFinalizedRequest(t, result, "hook text")
	root := gjson.ParseBytes(result)
	if root.Get("custom_field").String() != "kept" {
		t.Fatalf("custom_field must survive preservation, got: %s", result)
	}
}

func TestCodexFinalize_PluginFallbackCannotReaddRejectedFields(t *testing.T) {
	SetPluginHooks(codexFinalizePluginHook{fallbackTranslate: true})
	defer SetPluginHooks(nil)

	result := TranslateRequest(Format("plugin-source"), FormatCodex, "gpt-5.4", []byte(`{
		"model":"gpt-5.4",
		"input":"raw text"
	}`), false)

	assertCodexFinalizedRequest(t, result, "fallback text")
}

func TestCodexFinalize_StringInput_AllPublicRequestEntries(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"input":"entry text",
		"temperature":0.5,
		"user":"bad-user",
		"custom_field":"kept"
	}`)

	t.Run("runtime", func(t *testing.T) {
		result := TranslateRequest(FormatCodex, FormatCodex, "gpt-5.4", raw, false)
		assertCodexFinalizedRequest(t, result, "entry text")
		assertCustomField(t, result)
	})

	t.Run("builder", func(t *testing.T) {
		result, err := From(FormatCodex).Request(raw).ToWithModel(FormatCodex, "gpt-5.4")
		if err != nil {
			t.Fatalf("builder error = %v", err)
		}
		assertCodexFinalizedRequest(t, result, "entry text")
		assertCustomField(t, result)
	})

	t.Run("registry translate", func(t *testing.T) {
		result, err := DefaultRegistry().Translate(FormatCodex, FormatCodex, raw)
		if err != nil {
			t.Fatalf("registry translate error = %v", err)
		}
		assertCodexFinalizedRequest(t, result, "entry text")
		assertCustomField(t, result)
	})

	t.Run("registry serialize preserving", func(t *testing.T) {
		req := &UnifiedRequest{
			Model:        "gpt-5.4",
			Messages:     []OagMessage{UserTextMsg("entry text")},
			SourceFormat: FormatCodex,
		}
		result, err := DefaultRegistry().SerializeRequestPreserving(FormatCodex, req, raw)
		if err != nil {
			t.Fatalf("serialize preserving error = %v", err)
		}
		assertCodexFinalizedRequest(t, result, "entry text")
		assertCustomField(t, result)
	})

	t.Run("handler", func(t *testing.T) {
		h := &CodexHandler{}
		req, err := h.ParseRequest(raw)
		if err != nil {
			t.Fatalf("handler parse error = %v", err)
		}
		result, err := h.SerializeRequest(req)
		if err != nil {
			t.Fatalf("handler serialize error = %v", err)
		}
		assertCodexFinalizedRequest(t, result, "entry text")
	})
}

func assertCodexFinalizedRequest(t *testing.T, body []byte, wantText string) {
	t.Helper()
	root := gjson.ParseBytes(body)
	if root.Get("store").Bool() != false {
		t.Fatalf("store must be false, got: %s", body)
	}
	if root.Get("stream").Bool() != true {
		t.Fatalf("stream must be true, got: %s", body)
	}
	if root.Get("parallel_tool_calls").Bool() != true {
		t.Fatalf("parallel_tool_calls must be true, got: %s", body)
	}
	include := root.Get("include")
	if !include.IsArray() || len(include.Array()) != 1 || include.Array()[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include must be reasoning.encrypted_content, got: %s", body)
	}
	for _, field := range codexRejectedFields {
		if root.Get(field).Exists() {
			t.Fatalf("rejected field %q survived: %s", field, body)
		}
	}
	if root.Get("service_tier").Exists() && root.Get("service_tier").String() != "priority" {
		t.Fatalf("non-priority service_tier survived: %s", body)
	}
	input := root.Get("input")
	if !input.IsArray() || len(input.Array()) != 1 {
		t.Fatalf("input must be a single-item array, got: %s", body)
	}
	item := input.Array()[0]
	if item.Get("type").String() != "message" || item.Get("role").String() != "user" {
		t.Fatalf("input item must be user message, got: %s", item.Raw)
	}
	if gotText := item.Get("content.0.text").String(); gotText != wantText {
		t.Fatalf("input text = %q, want %q; body=%s", gotText, wantText, body)
	}
}

func assertCustomField(t *testing.T, body []byte) {
	t.Helper()
	if gjson.GetBytes(body, "custom_field").String() != "kept" {
		t.Fatalf("custom_field must survive, got: %s", body)
	}
}

type codexFinalizePluginHook struct {
	addRejectedInNormalize bool
	fallbackTranslate      bool
}

func (h codexFinalizePluginHook) NormalizeRequest(_ context.Context, _, _ Format, _ string, body []byte, _ bool) []byte {
	if !h.addRejectedInNormalize {
		return body
	}
	body, _ = sjson.SetBytes(body, "input", "hook text")
	body, _ = sjson.SetBytes(body, "temperature", 0.9)
	body, _ = sjson.SetBytes(body, "top_p", 0.8)
	body, _ = sjson.SetBytes(body, "stream", false)
	body, _ = sjson.SetBytes(body, "service_tier", "default")
	body, _ = sjson.SetRawBytes(body, "context_management", []byte(`{"compaction":"auto"}`))
	return body
}

func (h codexFinalizePluginHook) TranslateRequest(_ context.Context, _, _ Format, model string, _ []byte, _ bool) ([]byte, bool) {
	if !h.fallbackTranslate {
		return nil, false
	}
	body := []byte(`{"input":"fallback text","temperature":0.9,"stream":false,"service_tier":"default"}`)
	body, _ = sjson.SetBytes(body, "model", model)
	return body, true
}

func (codexFinalizePluginHook) NormalizeResponseBefore(_ context.Context, _, _ Format, _ string, _, _, body []byte, _ bool) []byte {
	return body
}

func (codexFinalizePluginHook) TranslateResponse(_ context.Context, _, _ Format, _ string, _, _, _ []byte, _ bool) ([]byte, bool) {
	return nil, false
}

func (codexFinalizePluginHook) NormalizeResponseAfter(_ context.Context, _, _ Format, _ string, _, _, body []byte, _ bool) []byte {
	return body
}
