package oagmsg

import (
	"encoding/json"
	"reflect"
	"testing"

	responses_to_claude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	responses_to_chat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

func TestResponsesAdditionalToolsToChatToolsRequestOracle(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"tools":[
			{"type":"function","name":"exec","description":"top-level exec","parameters":{"type":"object","properties":{"command":{"type":"string"}}}},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn","description":"top-level spawn","parameters":{"type":"object","properties":{}}}]}
		],
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"custom","name":"exec","description":"additional exec"},
				{"type":"function","name":"wait","parameters":{"type":"object","properties":{}}},
				{"type":"namespace","name":"collaboration","tools":[
					{"type":"function","name":"spawn","description":"additional spawn","parameters":{"type":"object","properties":{}}},
					{"type":"custom","name":"send","description":"send a message"}
				]}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)

	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&OpenAIHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	root := gjson.ParseBytes(out)
	if got := root.Get("tools.#").Int(); got != 4 {
		t.Fatalf("tools count = %d, want 4; output=%s", got, out)
	}
	if got := root.Get(`tools.#(function.name=="exec").function.description`).String(); got != "top-level exec" {
		t.Fatalf("exec description = %q, want top-level exec", got)
	}
	if got := countChatToolsNamed(root, "exec"); got != 1 {
		t.Fatalf("exec multiplicity = %d, want 1; output=%s", got, out)
	}
	if got := root.Get(`tools.#(function.name=="wait").function.name`).String(); got != "wait" {
		t.Fatalf("additional function name = %q, want wait", got)
	}
	if got := root.Get(`tools.#(function.name=="collaboration__spawn").function.description`).String(); got != "top-level spawn" {
		t.Fatalf("namespace function description = %q, want top-level spawn", got)
	}
	if got := countChatToolsNamed(root, "collaboration__spawn"); got != 1 {
		t.Fatalf("namespace spawn multiplicity = %d, want 1; output=%s", got, out)
	}
	if got := root.Get(`tools.#(function.name=="collaboration__send").function.parameters.properties.input.type`).String(); got != "string" {
		t.Fatalf("custom input schema type = %q, want string", got)
	}
}

func TestResponsesAdditionalToolsPreservesUnnamedBuiltins(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"tools":[
			{"type":"web_search_preview"},
			{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}
		],
		"input":[
			{"type":"additional_tools","tools":[{"type":"image_generation"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}
		]
	}`)

	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 3 {
		t.Fatalf("tools count = %d, want 3", len(req.Tools))
	}
	if got := req.Tools[0]["type"]; got != "web_search_preview" {
		t.Fatalf("top-level builtin type = %v, want web_search_preview", got)
	}
	if got := req.Tools[1]["name"]; got != "lookup" {
		t.Fatalf("descriptor tool name = %v, want lookup", got)
	}
	if got := req.Tools[2]["type"]; got != "image_generation" {
		t.Fatalf("additional builtin type = %v, want image_generation", got)
	}
	chat := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-test", raw, false)
	if got := gjson.GetBytes(chat, "tools.#").Int(); got != 1 {
		t.Fatalf("chat tools count = %d, want 1; output=%s", got, chat)
	}
	if got := gjson.GetBytes(chat, "tools.0.function.name").String(); got != "lookup" {
		t.Fatalf("chat tool name = %q, want lookup; output=%s", got, chat)
	}
	claude := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", raw, false)
	if got := gjson.GetBytes(claude, "tools.#").Int(); got != 1 {
		t.Fatalf("claude tools count = %d, want 1; output=%s", got, claude)
	}
	if got := gjson.GetBytes(claude, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("claude tool name = %q, want lookup; output=%s", got, claude)
	}
}

func TestResponsesToolChoiceUsesDescriptorWinnerMap(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantToolName string
	}{
		{
			name: "direct custom beats namespace collision",
			raw: `{
				"model":"claude-test",
				"tools":[
					{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","parameters":{"type":"object","properties":{}}}]},
					{"type":"custom","name":"n__x"}
				],
				"tool_choice":{"type":"custom","name":"n__x"},
				"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
			}`,
			wantToolName: "n__x",
		},
		{
			name: "namespace choice resolves short alias",
			raw: `{
				"model":"claude-test",
				"input":[
					{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js"}]}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
				],
				"tool_choice":{"type":"function","name":"js"}
			}`,
			wantToolName: "mcp__node_repl__js",
		},
		{
			name: "top-level direct owns short alias",
			raw: `{
				"model":"claude-test",
				"tools":[{"type":"function","name":"foo"}],
				"input":[
					{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__tools","tools":[{"type":"function","name":"foo"}]}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
				],
				"tool_choice":{"type":"function","name":"foo"}
			}`,
			wantToolName: "foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := (&InteractionsHandler{}).ParseRequest([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			out, err := (&AnthropicHandler{}).SerializeRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			root := gjson.ParseBytes(out)
			if got := root.Get("tool_choice.type").String(); got != "tool" {
				t.Fatalf("tool_choice.type = %q, want tool; output=%s", got, out)
			}
			if got := root.Get("tool_choice.name").String(); got != tt.wantToolName {
				t.Fatalf("tool_choice.name = %q, want %q; output=%s", got, tt.wantToolName, out)
			}
		})
	}
}

func TestResponsesCustomToolHistoryRoundTripPreservesFreeformOutput(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"custom","name":"exec"}],
		"input":[
			{"type":"custom_tool_call","call_id":"call.custom:1","name":"exec","input":"pwd && ls"},
			{"type":"custom_tool_call_output","call_id":"call.custom:1","output":"/workspace"}
		]
	}`)

	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	use, ok := req.Messages[0].Content[0].(CustomToolUseBlock)
	if !ok {
		t.Fatalf("custom history block = %T, want CustomToolUseBlock", req.Messages[0].Content[0])
	}
	if use.Input != "pwd && ls" {
		t.Fatalf("custom input = %q, want raw freeform text", use.Input)
	}
	result, ok := req.Messages[1].Content[0].(CustomToolResultBlock)
	if !ok {
		t.Fatalf("custom result block = %T, want CustomToolResultBlock", req.Messages[1].Content[0])
	}
	if result.Output != "/workspace" {
		t.Fatalf("custom output = %q, want /workspace", result.Output)
	}
	out, err := (&InteractionsHandler{}).SerializeMessages(req.Messages)
	if err != nil {
		t.Fatal(err)
	}
	root := gjson.ParseBytes(out)
	if got := root.Get("0.type").String(); got != "custom_tool_call" {
		t.Fatalf("roundtrip call type = %q, want custom_tool_call; output=%s", got, out)
	}
	if got := root.Get("0.input").String(); got != "pwd && ls" {
		t.Fatalf("roundtrip input = %q, want raw freeform text", got)
	}
	if got := root.Get("1.type").String(); got != "custom_tool_call_output" {
		t.Fatalf("roundtrip result type = %q, want custom_tool_call_output", got)
	}
	if got := root.Get("1.output").String(); got != "/workspace" {
		t.Fatalf("roundtrip output = %q, want /workspace", got)
	}
}

func TestResponsesNamespacedFunctionHistoryRoundTripPreservesQualifiedIdentity(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js","parameters":{"type":"object","properties":{}}}]}]},
			{"type":"function_call","call_id":"call.namespace","name":"js","namespace":"mcp__node_repl","arguments":"{\"code\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call.namespace","output":"ok"}
		]
	}`)

	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Tools[0]["name"]; got != "mcp__node_repl__js" {
		t.Fatalf("tool declaration name = %v, want mcp__node_repl__js", got)
	}
	use := req.Messages[0].Content[0].(ToolUseBlock)
	if use.Name != "mcp__node_repl__js" {
		t.Fatalf("history tool name = %q, want mcp__node_repl__js", use.Name)
	}
	out, err := (&InteractionsHandler{}).SerializeMessages(req.Messages)
	if err != nil {
		t.Fatal(err)
	}
	root := gjson.ParseBytes(out)
	if got := root.Get("0.name").String(); got != "mcp__node_repl__js" {
		t.Fatalf("roundtrip tool name = %q, want mcp__node_repl__js; output=%s", got, out)
	}
}

func TestResponsesToOpenAIChatCustomHistoryEndToEnd(t *testing.T) {
	out := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-5.4", responsesCustomHistoryFixture(), false)
	root := gjson.ParseBytes(out)

	if got := root.Get(`tools.#(function.name=="exec").function.parameters.properties.input.type`).String(); got != "string" {
		t.Fatalf("custom tool input schema = %q, want string; output=%s", got, out)
	}
	if got := root.Get(`tools.#(function.name=="mcp__node_repl__js").function.name`).String(); got != "mcp__node_repl__js" {
		t.Fatalf("namespace tool name = %q, want mcp__node_repl__js; output=%s", got, out)
	}
	if root.Get("tool_choice.function").Exists() {
		t.Fatalf("tool_choice.function exists; want upstream-preserved Responses object shape; output=%s", out)
	}
	if got := root.Get("tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, out)
	}
	if got := root.Get("tool_choice.name").String(); got != "js" {
		t.Fatalf("tool_choice.name = %q, want js; output=%s", got, out)
	}
	if got := root.Get("tool_choice.namespace").String(); got != "mcp__node_repl" {
		t.Fatalf("tool_choice.namespace = %q, want mcp__node_repl; output=%s", got, out)
	}
	if got := root.Get("messages.1.tool_calls.0.function.name").String(); got != "exec" {
		t.Fatalf("custom call name = %q, want exec; output=%s", got, out)
	}
	if got := root.Get("messages.1.tool_calls.0.function.arguments").String(); got != `{"input":"pwd"}` {
		t.Fatalf("custom call arguments = %q, want wrapped freeform input", got)
	}
	if got := root.Get("messages.2.role").String(); got != "tool" {
		t.Fatalf("custom output role = %q, want tool; output=%s", got, out)
	}
	if got := root.Get("messages.2.content").String(); got != "/workspace" {
		t.Fatalf("custom output content = %q, want /workspace; output=%s", got, out)
	}
	if got := root.Get("messages.3.tool_calls.0.function.name").String(); got != "mcp__node_repl__js" {
		t.Fatalf("namespace call name = %q, want mcp__node_repl__js; output=%s", got, out)
	}
	if got := root.Get("messages.4.tool_call_id").String(); got != "call.namespace" {
		t.Fatalf("namespace output call id = %q, want call.namespace; output=%s", got, out)
	}
}

func TestResponsesToAnthropicCustomHistoryEndToEnd(t *testing.T) {
	out := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", responsesCustomHistoryFixture(), false)
	root := gjson.ParseBytes(out)

	if got := root.Get(`tools.#(name=="exec").input_schema.properties.input.type`).String(); got != "string" {
		t.Fatalf("custom tool input schema = %q, want string; output=%s", got, out)
	}
	if got := root.Get(`tools.#(name=="mcp__node_repl__js").name`).String(); got != "mcp__node_repl__js" {
		t.Fatalf("namespace tool name = %q, want mcp__node_repl__js; output=%s", got, out)
	}
	if got := root.Get("tool_choice.name").String(); got != "mcp__node_repl__js" {
		t.Fatalf("tool_choice name = %q, want mcp__node_repl__js; output=%s", got, out)
	}
	if got := root.Get("messages.1.content.0.name").String(); got != "exec" {
		t.Fatalf("custom tool_use name = %q, want exec; output=%s", got, out)
	}
	if got := root.Get("messages.1.content.0.id").String(); got != "call_custom_1" {
		t.Fatalf("custom tool_use id = %q, want sanitized id; output=%s", got, out)
	}
	if got := root.Get("messages.1.content.0.input.input").String(); got != "pwd" {
		t.Fatalf("custom tool_use input = %q, want pwd; output=%s", got, out)
	}
	if got := root.Get("messages.2.content.0.tool_use_id").String(); got != "call_custom_1" {
		t.Fatalf("custom tool_result id = %q, want sanitized id; output=%s", got, out)
	}
	if got := root.Get("messages.2.content.0.content").String(); got != "/workspace" {
		t.Fatalf("custom tool_result content = %q, want /workspace; output=%s", got, out)
	}
	if got := root.Get("messages.3.content.0.name").String(); got != "mcp__node_repl__js" {
		t.Fatalf("namespace tool_use name = %q, want mcp__node_repl__js; output=%s", got, out)
	}
	if got := root.Get("messages.4.content.0.tool_use_id").String(); got != "call_namespace" {
		t.Fatalf("namespace tool_result id = %q, want sanitized id; output=%s", got, out)
	}
}

func TestResponsesToOpenAIChatCustomHistoryUpstreamOracle(t *testing.T) {
	upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", responsesCustomHistoryFixture(), false)
	oag := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-5.4", responsesCustomHistoryFixture(), false)

	if got, want := normalizedChatToolSemantics(t, oag), normalizedChatToolSemantics(t, upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("chat semantic mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
	}
}

func TestResponsesToAnthropicCustomHistoryUpstreamOracle(t *testing.T) {
	upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", responsesCustomHistoryFixture(), false)
	oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", responsesCustomHistoryFixture(), false)

	if got, want := normalizedClaudeToolSemantics(t, oag), normalizedClaudeToolSemantics(t, upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("claude semantic mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
	}
}

func TestResponsesInterleavedToolsUpstreamOracle(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "interleaved duplicate and filtered tools",
			raw:  responsesInterleavedToolsFixture(`{"type":"custom","name":"apply_patch"}`),
		},
		{
			name: "zero survivor omits openai tool settings",
			raw:  responsesZeroSurvivorToolsFixture(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/chat", func(t *testing.T) {
			upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", tt.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-5.4", tt.raw, false)
			if got, want := normalizedChatToolSurface(t, oag), normalizedChatToolSurface(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("chat tool surface mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
		t.Run(tt.name+"/claude", func(t *testing.T) {
			upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", tt.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", tt.raw, false)
			if got, want := normalizedClaudeToolSurface(t, oag), normalizedClaudeToolSurface(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("claude tool surface mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
	}
}

func TestResponsesZeroSurvivorToolSettingsInputShapesUpstreamOracle(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "array input", raw: responsesZeroSurvivorToolsFixture()},
		{name: "string input", raw: responsesZeroSurvivorStringInputFixture()},
		{name: "absent input", raw: responsesZeroSurvivorAbsentInputFixture()},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/chat", func(t *testing.T) {
			upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", tt.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-5.4", tt.raw, false)
			if got, want := normalizedChatToolSurface(t, oag), normalizedChatToolSurface(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("chat zero-survivor surface mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
		t.Run(tt.name+"/claude", func(t *testing.T) {
			upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", tt.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", tt.raw, false)
			if got, want := normalizedClaudeToolSurface(t, oag), normalizedClaudeToolSurface(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("claude zero-survivor surface mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
	}
}

func TestResponsesToolChoiceEligibilityBeforeWinnerUpstreamOracle(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "unsupported named before function", raw: responsesChoiceEligibilityCollisionFixture("image_generation", "chosen_tool")},
		{name: "disabled web search before function", raw: responsesChoiceEligibilityCollisionFixture("web_search", "search_tool")},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/chat", func(t *testing.T) {
			upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", tt.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-5.4", tt.raw, false)
			if got, want := normalizedChatToolSurface(t, oag), normalizedChatToolSurface(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("chat eligibility surface mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
		t.Run(tt.name+"/claude", func(t *testing.T) {
			upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", tt.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", tt.raw, false)
			if got, want := normalizedClaudeToolSurface(t, oag), normalizedClaudeToolSurface(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("claude eligibility surface mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
	}
}

func TestResponsesCustomToolStructuredOutputUpstreamOracle(t *testing.T) {
	fixtures := []struct {
		name string
		raw  []byte
	}{
		{name: "text array", raw: responsesCustomStructuredOutputFixture()},
		{name: "text image file array", raw: responsesCustomMediaOutputFixture()},
		{name: "stringified image array", raw: responsesCustomStringifiedImageOutputFixture()},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name+"/chat", func(t *testing.T) {
			upstream := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", fixture.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-5.4", fixture.raw, false)
			if got, want := normalizedChatToolSemantics(t, oag), normalizedChatToolSemantics(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("chat structured output mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
		t.Run(fixture.name+"/claude", func(t *testing.T) {
			upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", fixture.raw, false)
			oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", fixture.raw, false)
			if got, want := normalizedClaudeToolSemantics(t, oag), normalizedClaudeToolSemantics(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("claude structured output mismatch\noag:      %#v\nupstream: %#v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
			}
		})
	}
}

func TestResponsesCustomToolStructuredOutputRoundTripPreservesRawOutput(t *testing.T) {
	raw := responsesCustomMediaOutputFixture()
	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&InteractionsHandler{}).SerializeMessages(req.Messages)
	if err != nil {
		t.Fatal(err)
	}
	got := gjson.GetBytes(out, `#(type=="custom_tool_call_output").output`)
	want := gjson.GetBytes(raw, `input.#(type=="custom_tool_call_output").output`)
	if !reflect.DeepEqual(normalizeJSONResult(t, got), normalizeJSONResult(t, want)) {
		t.Fatalf("roundtrip raw output mismatch\ngot:  %s\nwant: %s\nout:  %s", got.Raw, want.Raw, out)
	}
}

func responsesCustomHistoryFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"custom","name":"exec","description":"Run a command"}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run tools"}]},
			{"type":"custom_tool_call","call_id":"call.custom:1","name":"exec","input":"pwd"},
			{"type":"custom_tool_call_output","call_id":"call.custom:1","output":"/workspace"},
			{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js","description":"Run JS","parameters":{"type":"object","properties":{"code":{"type":"string"}}}}]}]},
			{"type":"function_call","call_id":"call.namespace","name":"js","namespace":"mcp__node_repl","arguments":"{\"code\":\"1+1\"}"},
			{"type":"function_call_output","call_id":"call.namespace","output":"2"}
		],
		"tool_choice":{"type":"function","name":"js","namespace":"mcp__node_repl"}
	}`)
}

func responsesCustomStructuredOutputFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"custom","name":"exec","description":"Run a command"}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run tools"}]},
			{"type":"custom_tool_call","call_id":"call.custom:structured","name":"exec","input":"printf"},
			{"type":"custom_tool_call_output","call_id":"call.custom:structured","output":[
				{"type":"output_text","text":"alpha"},
				{"type":"output_text","text":"beta"}
			]}
		]
	}`)
}

func responsesCustomMediaOutputFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"custom","name":"exec","description":"Run a command"}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run tools"}]},
			{"type":"custom_tool_call","call_id":"call.custom:media","name":"exec","input":"inspect"},
			{"type":"custom_tool_call_output","call_id":"call.custom:media","output":[
				{"type":"output_text","text":"see attached"},
				{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="},
				{"type":"input_file","file_data":"data:application/pdf;base64,ZmlsZQ=="}
			]}
		]
	}`)
}

func responsesCustomStringifiedImageOutputFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"custom","name":"view_image","description":"View an image"}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]},
			{"type":"custom_tool_call","call_id":"call.custom:image","name":"view_image","input":"{}"},
			{"type":"custom_tool_call_output","call_id":"call.custom:image","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,AA==\",\"detail\":\"original\"}]"}
		]
	}`)
}

func responsesInterleavedToolsFixture(toolChoice string) []byte {
	return []byte(`{
		"model":"gpt-test",
		"parallel_tool_calls":false,
		"tools":[
			{"type":"namespace","name":"terminal","tools":[
				{"type":"function","name":"exec","description":"namespace exec","parameters":{"type":"object","properties":{"cwd":{"type":"string"}}}},
				{"type":"custom","name":"freeform","description":"namespace custom"}
			]},
			{"type":"function","name":"terminal__exec","description":"direct exec","parameters":{
				"oneOf":[
					{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]},
					{"type":"object","properties":{"script":{"type":"string"}}}
				]
			}},
			{"type":"function","name":"no_schema","description":"missing schema"},
			{"type":"custom","name":"apply_patch","description":"patch"},
			{"type":"custom","name":"freeform","description":"direct custom"},
			{"type":"web_search","name":"web_search","external_web_access":false,"max_uses":9},
			{"type":"web_search_preview"},
			{"type":"web_search_preview","name":"preview_named","description":"named preview raw"},
			{"type":"image_generation"},
			{"type":"unknown_passthrough","name":"mystery","description":"kept by claude"},
			{"type":"unknown_unnamed"}
		],
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"web_search","max_uses":2,"filters":{"allowed_domains":["example.com","docs.example"]},"user_location":{"type":"approximate","country":"US","region":"CA","city":"San Francisco"}},
				{"type":"function","name":"later","parameters":{"type":"object","properties":{"value":{"oneOf":[{"type":"string"},{"type":"number"}]}}}}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		],
		"tool_choice":` + toolChoice + `
	}`)
}

func responsesZeroSurvivorToolsFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"parallel_tool_calls":true,
		"tools":[
			{"type":"web_search_preview"},
			{"type":"image_generation"}
		],
		"input":[
			{"type":"additional_tools","tools":[{"type":"web_search","external_web_access":false}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		],
		"tool_choice":{"type":"function","name":"missing"}
	}`)
}

func responsesZeroSurvivorStringInputFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"parallel_tool_calls":true,
		"tools":[
			{"type":"image_generation"},
			{"type":"web_search","external_web_access":false}
		],
		"input":"hello",
		"tool_choice":{"type":"function","name":"missing"}
	}`)
}

func responsesZeroSurvivorAbsentInputFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"parallel_tool_calls":true,
		"tools":[
			{"type":"image_generation"},
			{"type":"web_search","external_web_access":false}
		],
		"tool_choice":{"type":"function","name":"missing"}
	}`)
}

func responsesChoiceEligibilityCollisionFixture(ineligibleType string, name string) []byte {
	ineligible := `{"type":"` + ineligibleType + `","name":"` + name + `"}`
	if ineligibleType == "web_search" {
		ineligible = `{"type":"web_search","name":"` + name + `","external_web_access":false}`
	}
	return []byte(`{
		"model":"gpt-test",
		"parallel_tool_calls":false,
		"tools":[` + ineligible + `],
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"function","name":"` + name + `","description":"eligible function","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		],
		"tool_choice":{"type":"function","name":"` + name + `"}
	}`)
}

type chatToolSemantics struct {
	chatToolSurface
	Messages []chatMessageSemantics
}

type chatMessageSemantics struct {
	Role      string
	ToolID    string
	Content   any
	CallNames []string
	CallArgs  []string
}

type chatToolSurface struct {
	Tools          []any
	ToolChoice     any
	ParallelExists bool
	ParallelValue  bool
}

func normalizedChatToolSemantics(t *testing.T, raw []byte) chatToolSemantics {
	t.Helper()
	root := gjson.ParseBytes(raw)
	sem := chatToolSemantics{chatToolSurface: normalizedChatToolSurface(t, raw)}
	for _, message := range root.Get("messages").Array() {
		if !message.Get("tool_call_id").Exists() && !message.Get("tool_calls").Exists() {
			continue
		}
		msg := chatMessageSemantics{
			Role:    message.Get("role").String(),
			ToolID:  message.Get("tool_call_id").String(),
			Content: normalizeChatContent(t, message.Get("content")),
		}
		for _, call := range message.Get("tool_calls").Array() {
			msg.CallNames = append(msg.CallNames, call.Get("function.name").String())
			msg.CallArgs = append(msg.CallArgs, normalizeJSONString(t, call.Get("function.arguments").String()))
		}
		sem.Messages = append(sem.Messages, msg)
	}
	return sem
}

func normalizedChatToolSurface(t *testing.T, raw []byte) chatToolSurface {
	t.Helper()
	root := gjson.ParseBytes(raw)
	var sem chatToolSurface
	for _, tool := range root.Get("tools").Array() {
		sem.Tools = append(sem.Tools, normalizeJSONResult(t, tool))
	}
	sem.ToolChoice = normalizedOpenAIChoiceSemantic(t, root.Get("tool_choice"))
	if parallel := root.Get("parallel_tool_calls"); parallel.Exists() {
		sem.ParallelExists = true
		sem.ParallelValue = parallel.Bool()
	}
	return sem
}

func countChatToolsNamed(root gjson.Result, name string) int {
	count := 0
	for _, tool := range root.Get("tools").Array() {
		if tool.Get("function.name").String() == name {
			count++
		}
	}
	return count
}

func normalizedOpenAIChoiceSemantic(t *testing.T, choice gjson.Result) any {
	t.Helper()
	if !choice.Exists() {
		return nil
	}
	if choice.Type == gjson.String {
		return choice.String()
	}
	name := normalizedChatChoiceName(choice)
	choiceType := choice.Get("type").String()
	if name != "" && (choiceType == "function" || choiceType == "custom" || choiceType == "tool") {
		return map[string]any{"type": "tool", "name": name}
	}
	return normalizeJSONResult(t, choice)
}

func normalizedChatChoiceName(choice gjson.Result) string {
	name := choice.Get("function.name").String()
	if name == "" {
		name = choice.Get("name").String()
	}
	namespace := choice.Get("namespace").String()
	if namespace == "" {
		namespace = choice.Get("function.namespace").String()
	}
	if namespace != "" {
		name = qualifyToolDescriptorName(namespace, name)
	}
	return name
}

func normalizeChatContent(t *testing.T, content gjson.Result) string {
	t.Helper()
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	return normalizeJSONValue(t, content.Raw)
}

type claudeToolSemantics struct {
	claudeToolSurface
	Messages []claudeMessageSemantics
}

type claudeMessageSemantics struct {
	Role      string
	BlockType string
	ID        string
	Name      string
	Input     any
	Content   any
}

type claudeToolSurface struct {
	Tools          []any
	ToolChoice     any
	ParallelExists bool
}

func normalizedClaudeToolSemantics(t *testing.T, raw []byte) claudeToolSemantics {
	t.Helper()
	root := gjson.ParseBytes(raw)
	sem := claudeToolSemantics{claudeToolSurface: normalizedClaudeToolSurface(t, raw)}
	for _, message := range root.Get("messages").Array() {
		for _, block := range message.Get("content").Array() {
			blockType := block.Get("type").String()
			if blockType != "tool_use" && blockType != "tool_result" {
				continue
			}
			sem.Messages = append(sem.Messages, claudeMessageSemantics{
				Role:      message.Get("role").String(),
				BlockType: blockType,
				ID:        firstNonEmpty(block.Get("id").String(), block.Get("tool_use_id").String()),
				Name:      block.Get("name").String(),
				Input:     normalizeJSONResult(t, block.Get("input")),
				Content:   normalizeJSONResult(t, block.Get("content")),
			})
		}
	}
	return sem
}

func normalizedClaudeToolSurface(t *testing.T, raw []byte) claudeToolSurface {
	t.Helper()
	root := gjson.ParseBytes(raw)
	var sem claudeToolSurface
	for _, tool := range root.Get("tools").Array() {
		sem.Tools = append(sem.Tools, normalizeJSONResult(t, tool))
	}
	if choice := root.Get("tool_choice"); choice.Exists() {
		sem.ToolChoice = normalizeJSONResult(t, choice)
	}
	sem.ParallelExists = root.Get("parallel_tool_calls").Exists()
	return sem
}

func normalizeJSONString(t *testing.T, raw string) string {
	t.Helper()
	return normalizeJSONValue(t, raw)
}

func normalizeJSONResult(t *testing.T, result gjson.Result) any {
	t.Helper()
	if !result.Exists() {
		return nil
	}
	if result.Type == gjson.String {
		return result.String()
	}
	var value any
	if err := json.Unmarshal([]byte(result.Raw), &value); err != nil {
		return result.Value()
	}
	return value
}

func normalizeJSONValue(t *testing.T, raw string) string {
	t.Helper()
	if raw == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(normalized)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
