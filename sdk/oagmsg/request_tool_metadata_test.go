package oagmsg

import (
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCodexShortNameMapMatchesPinnedForwardBehavior(t *testing.T) {
	const limit = codexToolNameLimitBytes
	alreadyShort := "short_name"
	plainLong := strings.Repeat("a", limit+8)
	mcpLong := "mcp__" + strings.Repeat("server_", 10) + "__run"
	mcpLastLong := "mcp__server__" + strings.Repeat("z", limit+8)
	collidingLong := strings.Repeat("a", limit) + "_tail"

	forward := buildCodexShortNameMap([]string{
		alreadyShort,
		plainLong,
		mcpLong,
		mcpLastLong,
		collidingLong,
	})

	if got := forward[alreadyShort]; got != alreadyShort {
		t.Fatalf("already short = %q, want unchanged", got)
	}
	if got := forward[plainLong]; got != strings.Repeat("a", limit) {
		t.Fatalf("plain long = %q, want 64-byte truncation", got)
	}
	if got := forward[mcpLong]; got != "mcp__run" {
		t.Fatalf("mcp long = %q, want mcp__run", got)
	}
	if got := forward[mcpLastLong]; len(got) != limit || !strings.HasPrefix(got, "mcp__") {
		t.Fatalf("mcp last long = %q, want mcp__ 64-byte candidate", got)
	}
	if got := forward[collidingLong]; got != strings.Repeat("a", limit-2)+"_1" {
		t.Fatalf("collision = %q, want suffixed re-truncation", got)
	}
	assertForwardReverseBijection(t, forward, reverseStringMap(forward))
}

func TestRequestToolMetadataUsesFirstValidRequestAndWinnerTypes(t *testing.T) {
	original := []byte(`{
		"tools":[
			{"type":"custom","name":"dupe","description":"top custom"},
			{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js"}]}
		],
		"input":[{"type":"additional_tools","tools":[
			{"type":"function","name":"dupe","description":"additional function"},
			{"type":"custom","name":"extra"}
		]}]
	}`)
	translated := []byte(`{"tools":[{"type":"custom","name":"translated_only"}]}`)

	metadata := buildRequestToolMetadataFromRequests([]byte(`{`), original, translated)

	if _, ok := metadata.customToolNames["translated_only"]; ok {
		t.Fatal("metadata used translated request despite earlier valid original request")
	}
	if _, ok := metadata.customToolNames["dupe"]; !ok {
		t.Fatal("top direct custom winner should own dupe")
	}
	if _, ok := metadata.functionToolNames["dupe"]; ok {
		t.Fatal("losing additional function must not own dupe")
	}
	if _, ok := metadata.functionToolNames["mcp__node_repl__js"]; !ok {
		t.Fatal("namespace function child was not qualified into function set")
	}
	if _, ok := metadata.customToolNames["extra"]; !ok {
		t.Fatal("additional custom tool was not included")
	}
}

func TestRequestToolMetadataDoesNotSkipValidOriginalWithoutTools(t *testing.T) {
	metadata := buildRequestToolMetadataFromRequests(
		[]byte(`{"model":"gpt-5.4","input":[]}`),
		[]byte(`{"tools":[{"type":"custom","name":"translated_only"}]}`),
	)

	if len(metadata.toolNameForward) != 0 || len(metadata.customToolNames) != 0 || len(metadata.functionToolNames) != 0 {
		t.Fatalf("metadata should come from first valid request without tools, got %+v", metadata)
	}
}

func TestApplyCodexRequestToolMetadata_SkipsBodyWithoutToolMetadataSignals(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
	}`)

	metadata := buildRequestToolMetadataFromRequests(
		[]byte(`{"tools":[{"type":"function","name":"f"}]}`),
	)

	out, err := applyCodexRequestToolMetadata(body, metadata)
	if err != nil {
		t.Fatalf("apply metadata error = %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("metadata should be skipped, got: %s", out)
	}
}

func TestRequestToolMetadataRemovesCustomWhenFunctionWinnerOwnsName(t *testing.T) {
	raw := []byte(`{
		"tools":[{"type":"function","name":"same"}],
		"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"same"}]}]
	}`)

	metadata := buildRequestToolMetadataFromRequests(raw)

	if _, ok := metadata.functionToolNames["same"]; !ok {
		t.Fatal("function winner should own same")
	}
	if _, ok := metadata.customToolNames["same"]; ok {
		t.Fatal("custom set retained a function-owned name")
	}
}

func TestRequestToolMetadataIgnoresBuiltinNamesForShortNameAllocation(t *testing.T) {
	const limit = codexToolNameLimitBytes
	ignored := strings.Repeat("b", limit) + "_builtin"
	function := strings.Repeat("b", limit) + "_function"
	raw := []byte(`{"tools":[
		{"type":"web_search_preview","name":"` + ignored + `"},
		{"type":"function","name":"` + function + `","parameters":{"type":"object"}}
	]}`)

	metadata := buildRequestToolMetadataFromRequests(raw)
	if _, ok := metadata.toolNameForward[ignored]; ok {
		t.Fatal("ignored builtin reserved a short-name allocation")
	}
	if got := metadata.toolNameForward[function]; got != strings.Repeat("b", limit) {
		t.Fatalf("function short name = %q, want unsuffixed base", got)
	}
	assertForwardReverseBijection(t, metadata.toolNameForward, metadata.toolNameReverse)

	body := []byte(`{"tools":[
		{"type":"web_search_preview","name":"` + ignored + `"},
		{"type":"function","name":"` + function + `","parameters":{"type":"object"}}
	]}`)
	out, err := applyCodexRequestToolMetadata(body, metadata)
	if err != nil {
		t.Fatalf("apply metadata error = %v", err)
	}
	root := gjson.ParseBytes(out)
	if got := root.Get("tools.0.name").String(); got != ignored {
		t.Fatalf("ignored declaration name = %q, want unchanged %q; body=%s", got, ignored, out)
	}
	if got := root.Get("tools.1.name").String(); got != strings.Repeat("b", limit) {
		t.Fatalf("function declaration name = %q, want unsuffixed base; body=%s", got, out)
	}
}

func TestCodexTranslateAppliesShortNamesAfterPreservation(t *testing.T) {
	const limit = codexToolNameLimitBytes
	long := strings.Repeat("mcp_prefix_", 8)
	raw := []byte(`{
		"model":"gpt-5.4",
		"custom_field":"kept",
		"tools":[{"type":"function","name":"` + long + `","parameters":{"type":"object"}}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"call_1","name":"` + long + `","arguments":"{}"}
		]
	}`)

	out, err := DefaultRegistry().Translate(FormatOpenAIResponse, FormatCodex, raw)
	if err != nil {
		t.Fatalf("Translate error = %v", err)
	}
	root := gjson.ParseBytes(out)
	short := root.Get("tools.0.name").String()
	if len(short) != limit {
		t.Fatalf("tool name length = %d, want %d; body=%s", len(short), limit, out)
	}
	if got := root.Get("input.1.name").String(); got != short {
		t.Fatalf("history name = %q, want %q; body=%s", got, short, out)
	}
	if got := root.Get("custom_field").String(); got != "kept" {
		t.Fatalf("preserved custom_field = %q, want kept; body=%s", got, out)
	}
}

func TestCodexSerializeAppliesShortNamesToDeclarationsChoiceAndHistory(t *testing.T) {
	const limit = codexToolNameLimitBytes
	long := strings.Repeat("a", limit) + "_first"
	colliding := strings.Repeat("a", limit) + "_second"
	if len(long) <= limit || shortenCodexToolNameBase(long) != shortenCodexToolNameBase(colliding) {
		t.Fatal("test fixture must produce a truncation collision")
	}
	req := &UnifiedRequest{
		Model:        "gpt-5.4",
		SourceFormat: FormatOpenAIResponse,
		Tools: []map[string]any{
			{"type": "function", "name": long, "parameters": map[string]any{"type": "object"}},
			{"type": "custom", "name": colliding},
		},
		ToolChoice: map[string]any{"type": "function", "name": long},
		Messages: []OagMessage{
			UserTextMsg("run tools"),
			{
				Role: "assistant",
				Content: []ContentBlock{
					ToolUseBlock{ID: "call_function", Name: long, Input: map[string]any{"x": "y"}},
					CustomToolUseBlock{ID: "call_custom", Name: colliding, Input: "raw input"},
				},
			},
		},
	}

	out, err := (&CodexHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error = %v", err)
	}
	root := gjson.ParseBytes(out)
	functionName := root.Get("tools.0.name").String()
	customName := root.Get("tools.1.name").String()
	if len(functionName) > limit || len(customName) > limit {
		t.Fatalf("short names exceed limit: function=%q custom=%q", functionName, customName)
	}
	if functionName == customName {
		t.Fatalf("short names collided: %q", functionName)
	}
	if got := root.Get("tool_choice.name").String(); got != functionName {
		t.Fatalf("tool_choice name = %q, want %q; body=%s", got, functionName, out)
	}
	if got := root.Get("input.1.name").String(); got != functionName {
		t.Fatalf("function history name = %q, want %q; body=%s", got, functionName, out)
	}
	if got := root.Get("input.2.name").String(); got != customName {
		t.Fatalf("custom history name = %q, want %q; body=%s", got, customName, out)
	}
	assertForwardReverseBijection(t, buildCodexShortNameMap([]string{long, colliding}), reverseStringMap(buildCodexShortNameMap([]string{long, colliding})))
}

func TestCodexSerializeUsesOriginalResponsesToolOrderForAdditionalCollisions(t *testing.T) {
	const limit = codexToolNameLimitBytes
	first := strings.Repeat("x", limit) + "_first"
	second := strings.Repeat("x", limit) + "_second"
	raw := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"function","name":"` + first + `"},
				{"type":"function","name":"` + second + `"}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"call_1","name":"` + second + `","arguments":"{}"}
		]
	}`)

	req, err := (&CodexHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest error = %v", err)
	}
	out, err := (&CodexHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error = %v", err)
	}
	root := gjson.ParseBytes(out)
	if got := root.Get("tools.0.name").String(); got != strings.Repeat("x", limit) {
		t.Fatalf("first additional tool = %q, want unsuffixed; body=%s", got, out)
	}
	secondShort := strings.Repeat("x", limit-2) + "_1"
	if got := root.Get("tools.1.name").String(); got != secondShort {
		t.Fatalf("second additional tool = %q, want %q; body=%s", got, secondShort, out)
	}
	if got := root.Get("input.1.name").String(); got != secondShort {
		t.Fatalf("history tool = %q, want %q; body=%s", got, secondShort, out)
	}
}

func TestRequestToolMetadataDeterministicUnderConcurrency(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"function","name":"` + strings.Repeat("a", 80) + `"},
			{"type":"function","name":"` + strings.Repeat("a", 64) + `_tail"}
		],
		"input":[{"type":"additional_tools","tools":[
			{"type":"namespace","name":"mcp__server","tools":[{"type":"custom","name":"` + strings.Repeat("z", 80) + `"}]}
		]}]
	}`)
	want := buildRequestToolMetadataFromRequests(raw).toolNameForward
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := buildRequestToolMetadataFromRequests(raw).toolNameForward
			if len(got) != len(want) {
				errs <- "map length changed"
				return
			}
			for name, wantShort := range want {
				if got[name] != wantShort {
					errs <- "map value changed"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for errText := range errs {
		t.Fatal(errText)
	}
}

func assertForwardReverseBijection(t *testing.T, forward, reverse map[string]string) {
	t.Helper()
	if len(forward) != len(reverse) {
		t.Fatalf("forward/reverse size mismatch: forward=%d reverse=%d", len(forward), len(reverse))
	}
	seenEmitted := make(map[string]string, len(forward))
	for original, emitted := range forward {
		if previous, exists := seenEmitted[emitted]; exists {
			t.Fatalf("emitted name %q maps both %q and %q", emitted, previous, original)
		}
		seenEmitted[emitted] = original
		if got := reverse[emitted]; got != original {
			t.Fatalf("reverse[%q] = %q, want %q", emitted, got, original)
		}
	}
}
