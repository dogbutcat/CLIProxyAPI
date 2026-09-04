package conformance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	codexchat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	codexresponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const t83CodexModel = "gpt-5.5"

type t83CodexManifestRow struct {
	Gap       string
	Direction string
	Fixture   string
	Evidence  string
}

var t83CodexOracleManifest = []t83CodexManifestRow{
	{"G4", "OpenAI->Codex", "overlength/collision/mcp function+custom tools", "chat-completions translator parity"},
	{"G4", "Responses/Codex/Claude/Gemini/Interactions->Codex", "request-local name shortening", "SDK semantic fixture"},
	{"G8", "Responses->Codex and Codex->Codex", "string input plus array input", "responses translator parity and SDK invariant"},
	{"G9", "public request entries->Codex", "runtime/builder/registry/plugin hooks", "finalized payload consistency"},
	{"G12", "Codex->OpenAI", "function/custom collisions and missing metadata", "chat-completions translator parity"},
	{"G12", "Codex->Responses", "namespace/custom/function restoration", "SDK semantic fixture"},
	{"G12", "Codex->Responses", "concurrent independent requests", "SDK race-safe fixture"},
	{"FD1", "Claude->Codex", "base64 PDF document source policy", "claude translator parity"},
}

func TestOracleCodexManifestCoversG4G8G9G12FD1(t *testing.T) {
	required := map[string]bool{"G4": false, "G8": false, "G9": false, "G12": false, "FD1": false}
	for _, row := range t83CodexOracleManifest {
		if row.Direction == "" || row.Fixture == "" || row.Evidence == "" {
			t.Fatalf("manifest row missing direction/fixture/evidence: %+v", row)
		}
		if _, ok := required[row.Gap]; ok {
			required[row.Gap] = true
		}
	}
	for gap, covered := range required {
		if !covered {
			t.Fatalf("manifest missing %s coverage", gap)
		}
	}
}

func TestOracleCodexFD1ClaudePDFDocumentMatchesTranslator(t *testing.T) {
	tests := []struct {
		name            string
		documentJSON    string
		wantFiles       []map[string]string
		wantUserContent []map[string]string
	}{
		{
			name:         "source data wins and title ignored",
			documentJSON: `{"type":"document","title":"custom.pdf","source":{"type":"base64","media_type":"application/pdf","data":"DATA_FIRST","base64":"BASE64_SECOND"}}`,
			wantFiles: []map[string]string{{
				"filename":  "document.pdf",
				"file_data": "data:application/pdf;base64,DATA_FIRST",
			}},
			wantUserContent: []map[string]string{
				{"type": "input_text", "text": "before"},
				{"type": "input_file", "filename": "document.pdf", "file_data": "data:application/pdf;base64,DATA_FIRST"},
				{"type": "input_text", "text": "after"},
			},
		},
		{
			name:         "source base64 fallback",
			documentJSON: `{"type":"document","title":"fallback.pdf","source":{"type":"base64","media_type":"application/pdf","base64":"BASE64_ONLY"}}`,
			wantFiles: []map[string]string{{
				"filename":  "document.pdf",
				"file_data": "data:application/pdf;base64,BASE64_ONLY",
			}},
			wantUserContent: []map[string]string{
				{"type": "input_text", "text": "before"},
				{"type": "input_file", "filename": "document.pdf", "file_data": "data:application/pdf;base64,BASE64_ONLY"},
				{"type": "input_text", "text": "after"},
			},
		},
		{
			name:         "trimmed mixed case PDF keeps trimmed media spelling",
			documentJSON: `{"type":"document","title":"mixed.pdf","source":{"type":"base64","media_type":"  Application/PDF  ","data":"TRIMMED"}}`,
			wantFiles: []map[string]string{{
				"filename":  "document.pdf",
				"file_data": "data:Application/PDF;base64,TRIMMED",
			}},
			wantUserContent: []map[string]string{
				{"type": "input_text", "text": "before"},
				{"type": "input_file", "filename": "document.pdf", "file_data": "data:Application/PDF;base64,TRIMMED"},
				{"type": "input_text", "text": "after"},
			},
		},
		{
			name:            "wrong source type omitted",
			documentJSON:    `{"type":"document","title":"url.pdf","source":{"type":"url","media_type":"application/pdf","url":"https://example.test/doc.pdf"}}`,
			wantUserContent: []map[string]string{{"type": "input_text", "text": "before"}, {"type": "input_text", "text": "after"}},
		},
		{
			name:            "non PDF omitted",
			documentJSON:    `{"type":"document","title":"notes.txt","source":{"type":"base64","media_type":"text/plain","data":"TEXT"}}`,
			wantUserContent: []map[string]string{{"type": "input_text", "text": "before"}, {"type": "input_text", "text": "after"}},
		},
		{
			name:            "empty data and base64 omitted",
			documentJSON:    `{"type":"document","title":"empty.pdf","source":{"type":"base64","media_type":"application/pdf","data":"","base64":""}}`,
			wantUserContent: []map[string]string{{"type": "input_text", "text": "before"}, {"type": "input_text", "text": "after"}},
		},
		{
			name:            "malformed document omitted",
			documentJSON:    `{"type":"document","title":"missing.pdf"}`,
			wantUserContent: []map[string]string{{"type": "input_text", "text": "before"}, {"type": "input_text", "text": "after"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := t83ClaudeDocumentRequest(tt.documentJSON)
			oracle := oagmsg.FinalizeCodexRequest(codexclaude.ConvertClaudeRequestToCodex(t83CodexModel, raw, true))
			got := oagmsg.TranslateRequest(oagmsg.FormatAnthropic, oagmsg.FormatCodex, t83CodexModel, raw, true)
			t83AssertCodexFinalInputSemanticsEqual(t, oracle, got)
			t83AssertCodexFinalRequest(t, got)
			t83AssertCodexInputFileSemantics(t, got, tt.wantFiles)
			t83AssertCodexUserMessageContent(t, got, tt.wantUserContent)
		})
	}
}

func TestOracleCodexFD1NonCodexDocumentBehaviorStaysGeneric(t *testing.T) {
	raw := t83ClaudeDocumentRequest(`{"type":"document","title":"source-title.txt","source":{"type":"base64","media_type":"text/plain","data":"","base64":"BASE64_FALLBACK"}}`)
	got := oagmsg.TranslateRequest(oagmsg.FormatAnthropic, oagmsg.FormatOpenAIResponse, t83CodexModel, raw, true)
	file := gjson.GetBytes(got, "input.1.content.0")
	if file.Get("type").String() != "input_file" {
		t.Fatalf("non-Codex document was not serialized as generic input_file: %s", got)
	}
	if filename := file.Get("filename").String(); filename != "source-title.txt" {
		t.Fatalf("non-Codex filename = %q, want source-title.txt; body=%s", filename, got)
	}
	if fileData := file.Get("file_data").String(); fileData != "" {
		t.Fatalf("non-Codex file_data = %q, want existing generic empty data behavior; body=%s", fileData, got)
	}
}

func TestOracleCodexFD1NoDocumentAnthropicRequestUsesGenericShape(t *testing.T) {
	raw := []byte(`{
		"model":"source-model",
		"max_tokens":100,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"before"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"IMG"}},
			{"type":"text","text":"after"}
		]}]
	}`)
	got := oagmsg.TranslateRequest(oagmsg.FormatAnthropic, oagmsg.FormatCodex, t83CodexModel, raw, true)
	input := gjson.GetBytes(got, "input").Array()
	if len(input) != 3 {
		t.Fatalf("no-document input item count = %d, want existing generic 3-item shape; body=%s", len(input), got)
	}
	want := [][]map[string]string{
		{{"type": "input_text", "text": "before"}},
		{{"type": "input_image", "image_data": "IMG"}},
		{{"type": "input_text", "text": "after"}},
	}
	for i, wantContent := range want {
		if role := input[i].Get("role").String(); role != "user" {
			t.Fatalf("no-document input[%d] role = %q, want user; body=%s", i, role, got)
		}
		if content := t83CodexContentSemantics(input[i].Get("content")); !reflect.DeepEqual(wantContent, content) {
			t.Fatalf("no-document input[%d] content mismatch\nwant: %#v\ngot:  %#v\nbody: %s", i, wantContent, content, got)
		}
	}
}

func TestOracleCodexFD1ClaudeDocumentGroupingStopsAtToolBoundaries(t *testing.T) {
	raw := []byte(`{
		"model":"source-model",
		"max_tokens":100,
		"messages":[
			{"role":"assistant","content":[
				{"type":"text","text":"before tool"},
				{"type":"tool_use","id":"call_next","name":"run","input":{"x":1}},
				{"type":"text","text":"after tool"}
			]},
			{"role":"user","content":[
				{"type":"text","text":"before result"},
				{"type":"document","title":"custom.pdf","source":{"type":"base64","media_type":"application/pdf","data":"PDF"}},
				{"type":"tool_result","tool_use_id":"call_next","content":"tool output"},
				{"type":"text","text":"after result"}
			]}
		],
		"tools":[{"name":"run","input_schema":{"type":"object"}}]
	}`)
	oracle := oagmsg.FinalizeCodexRequest(codexclaude.ConvertClaudeRequestToCodex(t83CodexModel, raw, true))
	got := oagmsg.TranslateRequest(oagmsg.FormatAnthropic, oagmsg.FormatCodex, t83CodexModel, raw, true)
	t83AssertCodexFinalInputSemanticsEqual(t, oracle, got)

	input := gjson.GetBytes(got, "input").Array()
	if len(input) != 5 {
		t.Fatalf("boundary input item count = %d, want 5; body=%s", len(input), got)
	}
	wantKinds := []string{"message", "function_call", "message", "function_call_output", "message"}
	for i, wantKind := range wantKinds {
		if gotKind := input[i].Get("type").String(); gotKind != wantKind {
			t.Fatalf("input[%d] type = %q, want %q; body=%s", i, gotKind, wantKind, got)
		}
	}
}

func TestOracleCodexFD1ClaudeMessageSystemReminderPreservesToolAdjacency(t *testing.T) {
	raw := []byte(`{
		"model":"source-model",
		"max_tokens":100,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Execute tools"}]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"tool_one","input":{"a":1}},
				{"type":"tool_use","id":"call_2","name":"tool_two","input":{"b":2}}
			]},
			{"role":"system","content":"Context update between tool call and tool result"},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_2","content":"result 2"},
				{"type":"tool_result","tool_use_id":"call_1","content":"result 1"},
				{"type":"text","text":"Now summarize"}
			]}
		]
	}`)
	oracle := oagmsg.FinalizeCodexRequest(codexclaude.ConvertClaudeRequestToCodex(t83CodexModel, raw, true))
	got := oagmsg.TranslateRequest(oagmsg.FormatAnthropic, oagmsg.FormatCodex, t83CodexModel, raw, true)
	t83AssertCodexFinalInputSemanticsEqual(t, oracle, got)

	input := gjson.GetBytes(got, "input").Array()
	wantKinds := []string{"message", "function_call", "function_call", "function_call_output", "function_call_output", "message", "message"}
	if len(input) != len(wantKinds) {
		t.Fatalf("system reminder input item count = %d, want %d; body=%s", len(input), len(wantKinds), got)
	}
	for i, wantKind := range wantKinds {
		if gotKind := input[i].Get("type").String(); gotKind != wantKind {
			t.Fatalf("input[%d] type = %q, want %q; body=%s", i, gotKind, wantKind, got)
		}
	}
	if gotID := input[3].Get("call_id").String(); gotID != "call_1" {
		t.Fatalf("first function_call_output call_id = %q, want call_1; body=%s", gotID, got)
	}
	if gotID := input[4].Get("call_id").String(); gotID != "call_2" {
		t.Fatalf("second function_call_output call_id = %q, want call_2; body=%s", gotID, got)
	}
	if reminder := input[5].Get("content.0.text").String(); reminder != "<system-reminder>\nContext update between tool call and tool result\n</system-reminder>" {
		t.Fatalf("system reminder text = %q; body=%s", reminder, got)
	}
}

func TestCapabilityCodexOracleKeepsIndependent32PairGate(t *testing.T) {
	if len(runtimeProtocolPairs) != 32 {
		t.Fatalf("pair count = %d, want 32", len(runtimeProtocolPairs))
	}
	seen := make(map[string]bool, len(runtimeProtocolPairs))
	for _, pair := range runtimeProtocolPairs {
		key := pairName(pair)
		if seen[key] {
			t.Fatalf("duplicate pair %s", key)
		}
		seen[key] = true
		if _, ok := oagmsg.DefaultRegistry().Get(pair.client); !ok {
			t.Fatalf("missing client handler for %s", key)
		}
		if _, ok := oagmsg.DefaultRegistry().Get(pair.upstream); !ok {
			t.Fatalf("missing upstream handler for %s", key)
		}
	}
}

func TestOracleCodexG4OpenAIRequestToolMetadataMatchesTranslator(t *testing.T) {
	longFunction := strings.Repeat("alpha", 14) + "_function"
	longCustom := strings.Repeat("alpha", 14) + "_custom"
	mcpFunction := "mcp__" + strings.Repeat("namespace_", 7) + "__shell"
	raw := []byte(`{
		"model":"source-model",
		"messages":[
			{"role":"system","content":"be exact"},
			{"role":"user","content":"run tools"},
			{"role":"assistant","content":"","tool_calls":[
				{"id":"call_function","type":"function","function":{"name":"` + longFunction + `","arguments":"{\"path\":\"a.txt\"}"}},
				{"id":"call_custom","type":"function","function":{"name":"` + longCustom + `","arguments":"{\"input\":\"pwd\"}"}},
				{"id":"call_mcp","type":"function","function":{"name":"` + mcpFunction + `","arguments":"{\"cmd\":\"ls\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_function","content":"file"},
			{"role":"tool","tool_call_id":"call_custom","content":"done"},
			{"role":"tool","tool_call_id":"call_mcp","content":"listed"}
		],
		"tools":[
			{"type":"function","function":{"name":"` + longFunction + `","description":"first","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}},
			{"type":"custom","name":"` + longCustom + `","description":"custom raw input"},
			{"type":"function","function":{"name":"` + mcpFunction + `","description":"mcp","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}}
		],
		"tool_choice":{"type":"function","function":{"name":"` + longFunction + `"}}
	}`)

	oracle := codexchat.ConvertOpenAIRequestToCodex(t83CodexModel, raw, true)
	got := oagmsg.TranslateRequest(oagmsg.FormatOpenAI, oagmsg.FormatCodex, t83CodexModel, raw, true)
	t83AssertJSONSemanticEqual(t, oracle, got)
	t83AssertCodexFinalRequest(t, got)

	root := gjson.ParseBytes(got)
	names := []string{
		root.Get("tools.0.name").String(),
		root.Get("tools.1.name").String(),
		root.Get("tools.2.name").String(),
		root.Get("input.2.name").String(),
		root.Get("input.3.name").String(),
		root.Get("input.4.name").String(),
		root.Get("tool_choice.name").String(),
	}
	t83AssertNamesWithinCodexLimit(t, names...)
	if root.Get("tools.0.name").String() == root.Get("tools.1.name").String() {
		t.Fatalf("collision produced identical emitted names: %s", got)
	}
	if gotMCP := root.Get("tools.2.name").String(); gotMCP != "mcp__shell" {
		t.Fatalf("mcp tool name = %q, want mcp__shell; body=%s", gotMCP, got)
	}
	if root.Get("input.2.name").String() != root.Get("tools.0.name").String() ||
		root.Get("input.3.name").String() != root.Get("tools.1.name").String() ||
		root.Get("input.4.name").String() != root.Get("tools.2.name").String() {
		t.Fatalf("history names do not match declarations: %s", got)
	}
}

func TestOracleCodexG4RequestDirectionsApplyDeterministicShortNames(t *testing.T) {
	longA := strings.Repeat("route", 14) + "_a"
	longB := strings.Repeat("route", 14) + "_b"
	tests := []struct {
		name        string
		from        oagmsg.Format
		raw         []byte
		toolPaths   []string
		historyPath string
	}{
		{
			name: "OpenAIResponses",
			from: oagmsg.FormatOpenAIResponse,
			raw: []byte(`{"model":"source","tools":[` +
				`{"type":"function","name":"` + longA + `","parameters":{"type":"object"}},` +
				`{"type":"function","name":"` + longB + `","parameters":{"type":"object"}}` +
				`],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
				`{"type":"function_call","call_id":"call_a","name":"` + longA + `","arguments":"{}"}]}`),
			toolPaths:   []string{"tools.0.name", "tools.1.name"},
			historyPath: "input.1.name",
		},
		{
			name: "Codex",
			from: oagmsg.FormatCodex,
			raw: []byte(`{"model":"source","tools":[` +
				`{"type":"custom","name":"` + longA + `"},` +
				`{"type":"custom","name":"` + longB + `"}` +
				`],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
				`{"type":"custom_tool_call","call_id":"call_a","name":"` + longA + `","input":"raw"}]}`),
			toolPaths:   []string{"tools.0.name", "tools.1.name"},
			historyPath: "input.1.name",
		},
		{
			name: "Anthropic",
			from: oagmsg.FormatAnthropic,
			raw: []byte(`{"model":"source","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"` + longA + `","input":{}}]}],` +
				`"tools":[{"name":"` + longA + `","input_schema":{"type":"object"}},{"name":"` + longB + `","input_schema":{"type":"object"}}],"max_tokens":100}`),
			toolPaths:   []string{"tools.0.name", "tools.1.name"},
			historyPath: "input.1.name",
		},
		{
			name: "Gemini",
			from: oagmsg.FormatGemini,
			raw: []byte(`{"model":"source","contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"name":"` + longA + `","args":{}}}]}],` +
				`"tools":[{"functionDeclarations":[{"name":"` + longA + `","parameters":{"type":"object"}},{"name":"` + longB + `","parameters":{"type":"object"}}]}]}`),
			toolPaths:   []string{"tools.0.functionDeclarations.0.name", "tools.0.functionDeclarations.1.name"},
			historyPath: "input.1.name",
		},
		{
			name: "Interactions",
			from: oagmsg.FormatInteractions,
			raw: []byte(`{"model":"source","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_call","name":"` + longA + `","call_id":"call_a","arguments":{}}],` +
				`"tools":[{"type":"function","name":"` + longA + `","parameters":{"type":"object"}},{"type":"function","name":"` + longB + `","parameters":{"type":"object"}}]}`),
			toolPaths:   []string{"tools.0.name", "tools.1.name"},
			historyPath: "input.1.name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := oagmsg.TranslateRequest(tt.from, oagmsg.FormatCodex, t83CodexModel, tt.raw, true)
			t83AssertCodexFinalRequest(t, got)
			root := gjson.ParseBytes(got)
			var names []string
			for _, path := range tt.toolPaths {
				names = append(names, root.Get(path).String())
			}
			names = append(names, root.Get(tt.historyPath).String())
			t83AssertNamesWithinCodexLimit(t, names...)
			if root.Get(tt.toolPaths[0]).String() == root.Get(tt.toolPaths[1]).String() {
				t.Fatalf("%s collision produced identical emitted names: %s", tt.name, got)
			}
			if root.Get(tt.historyPath).String() != root.Get(tt.toolPaths[0]).String() {
				t.Fatalf("%s history name = %q, want first declaration name %q; body=%s", tt.name, root.Get(tt.historyPath).String(), root.Get(tt.toolPaths[0]).String(), got)
			}
		})
	}
}

func TestOracleCodexG8StringInputNormalizationMatchesTranslator(t *testing.T) {
	raw := []byte(`{
		"model":"source-model",
		"input":"normalize me",
		"temperature":0.7,
		"stream":false,
		"user":"bad-user"
	}`)
	oracle := codexresponses.ConvertOpenAIResponsesRequestToCodex(t83CodexModel, raw, true)
	got := oagmsg.TranslateRequest(oagmsg.FormatOpenAIResponse, oagmsg.FormatCodex, t83CodexModel, raw, true)
	t83AssertJSONResultSemanticEqual(t, "G8 input projection", gjson.GetBytes(oracle, "input"), gjson.GetBytes(got, "input"))
	t83AssertCodexFinalRequest(t, got)
	if text := gjson.GetBytes(got, "input.0.content.0.text").String(); text != "normalize me" {
		t.Fatalf("normalized text = %q, want normalize me; body=%s", text, got)
	}
}

func TestOracleCodexG8ArrayInputBehaviorIsPreserved(t *testing.T) {
	raw := []byte(`{
		"model":"source-model",
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"rules"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"},{"type":"input_text","text":"second"}]},
			{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"ok"}]}
		]
	}`)
	got := oagmsg.TranslateRequest(oagmsg.FormatCodex, oagmsg.FormatCodex, t83CodexModel, raw, true)
	t83AssertCodexFinalRequest(t, got)
	root := gjson.ParseBytes(got)
	if count := len(root.Get("input").Array()); count != 3 {
		t.Fatalf("input item count = %d, want 3; body=%s", count, got)
	}
	if role := root.Get("input.0.role").String(); role != "developer" {
		t.Fatalf("system input role = %q, want developer; body=%s", role, got)
	}
	if first, second := root.Get("input.1.content.0.text").String(), root.Get("input.1.content.1.text").String(); first != "first" || second != "second" {
		t.Fatalf("array content order changed: first=%q second=%q body=%s", first, second, got)
	}
	if output := root.Get("input.2.output"); !output.IsArray() || output.Get("0.text").String() != "ok" {
		t.Fatalf("array tool output was lost: %s", got)
	}
}

func TestOracleCodexG9PublicRequestEntriesProduceConsistentFinalPayloads(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.5",
		"input":"entry text",
		"temperature":0.5,
		"top_p":0.9,
		"user":"bad-user",
		"context_management":{"compaction":"auto"},
		"tools":[{"type":"web_search_preview","name":"search"}],
		"custom_field":"kept"
	}`)

	runtime := oagmsg.TranslateRequest(oagmsg.FormatCodex, oagmsg.FormatCodex, t83CodexModel, raw, false)
	builder, err := oagmsg.From(oagmsg.FormatCodex).Request(raw).ToWithModel(oagmsg.FormatCodex, t83CodexModel)
	if err != nil {
		t.Fatalf("builder entry error = %v", err)
	}
	registry, err := oagmsg.DefaultRegistry().Translate(oagmsg.FormatCodex, oagmsg.FormatCodex, raw)
	if err != nil {
		t.Fatalf("registry entry error = %v", err)
	}
	registry, _ = sjson.SetBytes(registry, "model", t83CodexModel)
	registry = oagmsg.FinalizeCodexRequest(registry)

	for name, body := range map[string][]byte{"runtime": runtime, "builder": builder, "registry": registry} {
		t.Run(name, func(t *testing.T) {
			t83AssertCodexFinalRequest(t, body)
			if text := gjson.GetBytes(body, "input.0.content.0.text").String(); text != "entry text" {
				t.Fatalf("%s text = %q, want entry text; body=%s", name, text, body)
			}
			if field := gjson.GetBytes(body, "custom_field").String(); field != "kept" {
				t.Fatalf("%s custom_field = %q, want kept; body=%s", name, field, body)
			}
			if toolType := gjson.GetBytes(body, "tools.0.type").String(); toolType != "web_search" {
				t.Fatalf("%s builtin tool type = %q, want web_search; body=%s", name, toolType, body)
			}
		})
	}
	t83AssertJSONSemanticEqual(t, runtime, builder)
	t83AssertJSONSemanticEqual(t, runtime, registry)
}

func TestOracleCodexG9PluginHooksCannotOwnHiddenUnfinalizedCodexPath(t *testing.T) {
	oagmsg.SetPluginHooks(t83CodexPluginHook{})
	defer oagmsg.SetPluginHooks(nil)

	normalized := oagmsg.TranslateRequest(oagmsg.FormatOpenAI, oagmsg.FormatCodex, t83CodexModel, []byte(`{
		"model":"source",
		"messages":[{"role":"user","content":"hi"}]
	}`), false)
	t83AssertCodexFinalRequest(t, normalized)
	if text := gjson.GetBytes(normalized, "input.0.content.0.text").String(); text != "hook text" {
		t.Fatalf("normalize hook text = %q, want hook text; body=%s", text, normalized)
	}

	fallback := oagmsg.TranslateRequest(oagmsg.Format("plugin-source"), oagmsg.FormatCodex, t83CodexModel, []byte(`{"input":"raw"}`), false)
	t83AssertCodexFinalRequest(t, fallback)
	if text := gjson.GetBytes(fallback, "input.0.content.0.text").String(); text != "fallback text" {
		t.Fatalf("fallback hook text = %q, want fallback text; body=%s", text, fallback)
	}
}

func TestOracleCodexG12ResponseMetadataRestorationMatchesTranslator(t *testing.T) {
	longFunction := strings.Repeat("omega", 14) + "_function"
	longCustom := strings.Repeat("omega", 14) + "_custom"
	original := []byte(`{"model":"source","messages":[{"role":"user","content":"run"}],"tools":[` +
		`{"type":"function","function":{"name":"` + longFunction + `","parameters":{"type":"object"}}},` +
		`{"type":"custom","name":"` + longCustom + `"}` +
		`]}`)
	translated := codexchat.ConvertOpenAIRequestToCodex(t83CodexModel, original, true)
	shortFunction := gjson.GetBytes(translated, "tools.0.name").String()
	shortCustom := gjson.GetBytes(translated, "tools.1.name").String()
	if shortFunction == "" || shortCustom == "" || shortFunction == shortCustom {
		t.Fatalf("fixture did not allocate distinct short names: %s", translated)
	}
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_g12","object":"response","created_at":123,"status":"completed","model":"` + t83CodexModel + `","output":[` +
		`{"type":"function_call","call_id":"call_function","name":"` + shortFunction + `","arguments":"{\"path\":\"a.txt\"}"},` +
		`{"type":"function_call","call_id":"call_custom","name":"` + shortCustom + `","arguments":"{\"input\":\"pwd\"}"}` +
		`],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`)

	oracle := codexchat.ConvertCodexResponseToOpenAINonStream(context.Background(), t83CodexModel, original, translated, raw, nil)
	got := oagmsg.TranslateNonStream(context.Background(), oagmsg.FormatCodex, oagmsg.FormatOpenAI, t83CodexModel, original, translated, raw, nil)
	t83AssertOpenAIChatResponseToolSemanticsEqual(t, oracle, got)
	if name := gjson.GetBytes(got, "choices.0.message.tool_calls.0.function.name").String(); name != longFunction {
		t.Fatalf("function name = %q, want %q; body=%s", name, longFunction, got)
	}
	if name := gjson.GetBytes(got, "choices.0.message.tool_calls.1.function.name").String(); name != longCustom {
		t.Fatalf("custom name = %q, want %q; body=%s", name, longCustom, got)
	}
}

func TestOracleCodexG12MissingMetadataLeavesNamesUnchanged(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_missing","object":"response","created_at":123,"status":"completed","model":"` + t83CodexModel + `","output":[` +
		`{"type":"function_call","call_id":"call_unknown","name":"unknown_short","arguments":"{}"}` +
		`]}}`)
	oracle := codexchat.ConvertCodexResponseToOpenAINonStream(context.Background(), t83CodexModel, nil, nil, raw, nil)
	got := oagmsg.TranslateNonStream(context.Background(), oagmsg.FormatCodex, oagmsg.FormatOpenAI, t83CodexModel, nil, nil, raw, nil)
	t83AssertOpenAIChatResponseToolSemanticsEqual(t, oracle, got)
	if name := gjson.GetBytes(got, "choices.0.message.tool_calls.0.function.name").String(); name != "unknown_short" {
		t.Fatalf("missing metadata name = %q, want unknown_short; body=%s", name, got)
	}
}

func TestOracleCodexG12NamespaceFunctionCustomRestoration(t *testing.T) {
	rawRequest := []byte(`{"model":"source","tools":[` +
		`{"type":"namespace","name":"shell","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]},` +
		`{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"custom","name":"js"}]},` +
		`{"type":"custom","name":"direct_custom"}` +
		`]}`)
	response := []byte(`{"id":"resp_namespace","object":"response","status":"completed","model":"` + t83CodexModel + `","output":[` +
		`{"type":"function_call","call_id":"call_run","name":"shell__run","arguments":"{\"cmd\":\"pwd\"}"},` +
		`{"type":"function_call","call_id":"call_js","name":"mcp__node_repl__js","arguments":"{\"input\":\"1+1\"}"},` +
		`{"type":"function_call","call_id":"call_custom","name":"direct_custom","arguments":"{\"input\":\"raw\"}"}` +
		`]}`)

	got := oagmsg.TranslateNonStream(context.Background(), oagmsg.FormatCodex, oagmsg.FormatOpenAIResponse, t83CodexModel, rawRequest, nil, response, nil)
	t83AssertResponseTool(t, gjson.GetBytes(got, "output.0"), "function_call", "run", "shell", "call_run", `{"cmd":"pwd"}`, "")
	t83AssertResponseTool(t, gjson.GetBytes(got, "output.1"), "custom_tool_call", "mcp__node_repl__js", "", "call_js", "", "1+1")
	t83AssertResponseTool(t, gjson.GetBytes(got, "output.2"), "custom_tool_call", "direct_custom", "", "call_custom", "", "raw")
}

func TestOracleCodexG12ConcurrentIndependentRequests(t *testing.T) {
	type fixture struct {
		original []byte
		short    string
		want     string
	}
	makeFixture := func(prefix string) fixture {
		want := strings.Repeat(prefix, 14) + "_tool"
		original := []byte(`{"model":"source","tools":[{"type":"custom","name":"` + want + `"}]}`)
		translated, err := oagmsg.DefaultRegistry().Translate(oagmsg.FormatOpenAIResponse, oagmsg.FormatCodex, original)
		if err != nil {
			t.Fatalf("fixture %s request translation error = %v", prefix, err)
		}
		short := gjson.GetBytes(translated, "tools.0.name").String()
		if short == "" || short == want {
			t.Fatalf("fixture %s did not allocate short name: %s", prefix, translated)
		}
		return fixture{original: original, short: short, want: want}
	}
	fixtures := []fixture{makeFixture("alpha"), makeFixture("bravo")}

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 64; i++ {
		current := fixtures[i%len(fixtures)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw := []byte(`{"id":"resp_concurrent","object":"response","status":"completed","model":"` + t83CodexModel + `","output":[` +
				`{"type":"function_call","call_id":"call_1","name":"` + current.short + `","arguments":"{\"input\":\"ok\"}"}` +
				`]}`)
			got := oagmsg.TranslateNonStream(context.Background(), oagmsg.FormatCodex, oagmsg.FormatOpenAIResponse, t83CodexModel, current.original, nil, raw, nil)
			if name := gjson.GetBytes(got, "output.0.name").String(); name != current.want {
				errs <- fmt.Sprintf("restored name = %q, want %q; body=%s", name, current.want, got)
			}
			if item := gjson.GetBytes(got, "output.0"); item.Get("type").String() != "custom_tool_call" || item.Get("call_id").String() != "call_1" || item.Get("input").String() != "ok" {
				errs <- fmt.Sprintf("restored item semantics mismatch: %s", item.Raw)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for errText := range errs {
		t.Fatal(errText)
	}
}

type t83CodexPluginHook struct{}

func (t83CodexPluginHook) NormalizeRequest(_ context.Context, _, _ oagmsg.Format, _ string, body []byte, _ bool) []byte {
	body, _ = sjson.SetBytes(body, "input", "hook text")
	body, _ = sjson.SetBytes(body, "temperature", 0.9)
	body, _ = sjson.SetBytes(body, "top_p", 0.8)
	body, _ = sjson.SetBytes(body, "stream", false)
	body, _ = sjson.SetBytes(body, "service_tier", "default")
	body, _ = sjson.SetRawBytes(body, "context_management", []byte(`{"compaction":"auto"}`))
	return body
}

func (t83CodexPluginHook) TranslateRequest(_ context.Context, _, _ oagmsg.Format, model string, _ []byte, _ bool) ([]byte, bool) {
	body := []byte(`{"input":"fallback text","temperature":0.9,"stream":false,"service_tier":"default"}`)
	body, _ = sjson.SetBytes(body, "model", model)
	return body, true
}

func (t83CodexPluginHook) NormalizeResponseBefore(_ context.Context, _, _ oagmsg.Format, _ string, _, _, body []byte, _ bool) []byte {
	return body
}

func (t83CodexPluginHook) TranslateResponse(_ context.Context, _, _ oagmsg.Format, _ string, _, _, _ []byte, _ bool) ([]byte, bool) {
	return nil, false
}

func (t83CodexPluginHook) NormalizeResponseAfter(_ context.Context, _, _ oagmsg.Format, _ string, _, _, body []byte, _ bool) []byte {
	return body
}

func t83AssertJSONSemanticEqual(t *testing.T, want, got []byte) {
	t.Helper()
	wantValue := t83DecodeJSON(t, want)
	gotValue := t83DecodeJSON(t, got)
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("semantic JSON mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func t83AssertJSONResultSemanticEqual(t *testing.T, label string, want, got gjson.Result) {
	t.Helper()
	wantValue := t83DecodeJSON(t, []byte(want.Raw))
	gotValue := t83DecodeJSON(t, []byte(got.Raw))
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("%s mismatch\nwant: %s\ngot:  %s", label, want.Raw, got.Raw)
	}
}

func t83AssertOpenAIChatResponseToolSemanticsEqual(t *testing.T, want, got []byte) {
	t.Helper()
	// Exact exclusions: created, message.content null-vs-empty,
	// reasoning_content presence, and native_finish_reason.
	wantTools := t83OpenAIChatResponseToolSemantics(t, want)
	gotTools := t83OpenAIChatResponseToolSemantics(t, got)
	if !reflect.DeepEqual(wantTools, gotTools) {
		t.Fatalf("OpenAI chat response tool semantics mismatch\nwant: %#v\ngot:  %#v\nwant body: %s\ngot body:  %s", wantTools, gotTools, want, got)
	}
}

type t83OpenAIChatResponseToolSemantic struct {
	Type      string
	Namespace string
	CallID    string
	Name      string
	Args      string
	Input     string
}

func t83OpenAIChatResponseToolSemantics(t *testing.T, body []byte) []t83OpenAIChatResponseToolSemantic {
	t.Helper()
	root := gjson.ParseBytes(body)
	calls := root.Get("choices.0.message.tool_calls").Array()
	out := make([]t83OpenAIChatResponseToolSemantic, 0, len(calls))
	for _, call := range calls {
		out = append(out, t83OpenAIChatResponseToolSemantic{
			Type:      call.Get("type").String(),
			Namespace: call.Get("namespace").String(),
			CallID:    call.Get("id").String(),
			Name:      call.Get("function.name").String(),
			Args:      call.Get("function.arguments").String(),
			Input:     call.Get("input").String(),
		})
	}
	return out
}

func t83DecodeJSON(t *testing.T, body []byte) any {
	t.Helper()
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("invalid JSON %q: %v", body, err)
	}
	return value
}

func t83AssertCodexFinalRequest(t *testing.T, body []byte) {
	t.Helper()
	root := gjson.ParseBytes(body)
	if got := root.Get("store"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("store = %s, want explicit false; body=%s", got.Raw, body)
	}
	if got := root.Get("stream"); !got.Exists() || got.Type != gjson.True {
		t.Fatalf("stream = %s, want explicit true; body=%s", got.Raw, body)
	}
	if got := root.Get("parallel_tool_calls"); !got.Exists() || got.Type != gjson.True {
		t.Fatalf("parallel_tool_calls = %s, want explicit true; body=%s", got.Raw, body)
	}
	include := root.Get("include")
	if !include.IsArray() || len(include.Array()) != 1 || include.Array()[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want reasoning.encrypted_content only; body=%s", include.Raw, body)
	}
	for _, path := range []string{"max_output_tokens", "max_completion_tokens", "temperature", "top_p", "truncation", "user", "context_management"} {
		if root.Get(path).Exists() {
			t.Fatalf("rejected field %q survived: %s", path, body)
		}
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.String() != "priority" {
		t.Fatalf("non-priority service_tier survived: %s", body)
	}
	if input := root.Get("input"); !input.IsArray() {
		t.Fatalf("input must be array after finalization: %s", body)
	}
}

func t83ClaudeDocumentRequest(documentJSON string) []byte {
	return []byte(`{"model":"source-model","max_tokens":100,"system":"system rules","messages":[{"role":"user","content":[{"type":"text","text":"before"},` + documentJSON + `,{"type":"text","text":"after"}]}]}`)
}

func t83AssertCodexInputFileSemantics(t *testing.T, body []byte, want []map[string]string) {
	t.Helper()
	var got []map[string]string
	root := gjson.ParseBytes(body)
	for _, input := range root.Get("input").Array() {
		for _, content := range input.Get("content").Array() {
			if content.Get("type").String() != "input_file" {
				continue
			}
			got = append(got, map[string]string{
				"filename":  content.Get("filename").String(),
				"file_data": content.Get("file_data").String(),
			})
		}
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Codex input_file semantics mismatch\nwant: %#v\ngot:  %#v\nbody: %s", want, got, body)
	}
}

func t83AssertCodexFinalInputSemanticsEqual(t *testing.T, want, got []byte) {
	t.Helper()
	wantSemantics := t83CodexFinalInputSemantics(want)
	gotSemantics := t83CodexFinalInputSemantics(got)
	if !reflect.DeepEqual(wantSemantics, gotSemantics) {
		t.Fatalf("Codex final input semantics mismatch\nwant: %#v\ngot:  %#v\nwant body: %s\ngot body:  %s", wantSemantics, gotSemantics, want, got)
	}
}

func t83CodexFinalInputSemantics(body []byte) map[string]any {
	root := gjson.ParseBytes(body)
	return map[string]any{
		"model":        root.Get("model").String(),
		"instructions": root.Get("instructions").String(),
		"input":        t83CodexInputItemSemantics(root.Get("input")),
	}
}

func t83CodexInputItemSemantics(input gjson.Result) []map[string]any {
	var out []map[string]any
	for _, item := range input.Array() {
		if item.Get("type").String() != "message" {
			out = append(out, t83CodexNonMessageItemSemantics(item))
			continue
		}
		entry := map[string]any{
			"item_type": "message",
			"role":      item.Get("role").String(),
			"content":   t83CodexContentSemantics(item.Get("content")),
		}
		out = append(out, entry)
	}
	return out
}

func t83CodexNonMessageItemSemantics(item gjson.Result) map[string]any {
	entry := map[string]any{
		"item_type": item.Get("type").String(),
	}
	for _, path := range []string{"call_id", "name", "arguments", "output", "encrypted_content"} {
		if value := item.Get(path); value.Exists() {
			entry[path] = value.String()
		}
	}
	if content := item.Get("content"); content.IsArray() {
		entry["content"] = t83CodexContentSemantics(content)
	}
	return entry
}

func t83CodexContentSemantics(content gjson.Result) []map[string]string {
	var out []map[string]string
	for _, content := range content.Array() {
		entry := map[string]string{
			"type": content.Get("type").String(),
		}
		for _, path := range []string{"text", "filename", "file_data", "image_url", "image_data"} {
			if value := content.Get(path); value.Exists() {
				entry[path] = value.String()
			}
		}
		out = append(out, entry)
	}
	return out
}

func t83AssertCodexUserMessageContent(t *testing.T, body []byte, want []map[string]string) {
	t.Helper()
	input := gjson.GetBytes(body, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input item count = %d, want 2; body=%s", len(input), body)
	}
	if role := input[0].Get("role").String(); role != "developer" {
		t.Fatalf("input[0] role = %q, want developer; body=%s", role, body)
	}
	if role := input[1].Get("role").String(); role != "user" {
		t.Fatalf("input[1] role = %q, want user; body=%s", role, body)
	}
	got := t83CodexContentSemantics(input[1].Get("content"))
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("user message content mismatch\nwant: %#v\ngot:  %#v\nbody: %s", want, got, body)
	}
}

func t83AssertNamesWithinCodexLimit(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if name == "" {
			t.Fatalf("empty emitted tool name in %q", names)
		}
		if len(name) > 64 {
			t.Fatalf("tool name %q length = %d, want <=64", name, len(name))
		}
	}
}

func t83AssertResponseTool(t *testing.T, item gjson.Result, wantType, wantName, wantNamespace, wantCallID, wantArgs, wantInput string) {
	t.Helper()
	if got := item.Get("type").String(); got != wantType {
		t.Fatalf("tool type = %q, want %q; item=%s", got, wantType, item.Raw)
	}
	if got := item.Get("name").String(); got != wantName {
		t.Fatalf("tool name = %q, want %q; item=%s", got, wantName, item.Raw)
	}
	if got := item.Get("namespace").String(); got != wantNamespace {
		t.Fatalf("tool namespace = %q, want %q; item=%s", got, wantNamespace, item.Raw)
	}
	if got := item.Get("call_id").String(); got != wantCallID {
		t.Fatalf("tool call_id = %q, want %q; item=%s", got, wantCallID, item.Raw)
	}
	if got := item.Get("arguments").String(); got != wantArgs {
		t.Fatalf("tool arguments = %q, want %q; item=%s", got, wantArgs, item.Raw)
	}
	if got := item.Get("input").String(); got != wantInput {
		t.Fatalf("tool input = %q, want %q; item=%s", got, wantInput, item.Raw)
	}
}
