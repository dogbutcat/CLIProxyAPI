package oagmsg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPreserveXAIResponsesOutputControls(t *testing.T) {
	tests := []struct {
		name   string
		from   Format
		source string
		want   int64
	}{
		{name: "chat completion tokens", from: FormatOpenAI, source: `{"max_completion_tokens":64,"temperature":0.2,"top_p":0.8,"top_k":7}`, want: 64},
		{name: "chat legacy tokens", from: FormatOpenAI, source: `{"max_tokens":128}`, want: 128},
		{name: "responses tokens", from: FormatOpenAIResponse, source: `{"max_output_tokens":256}`, want: 256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreserveXAIResponsesOutputControls([]byte(`{"model":"grok"}`), []byte(tt.source), tt.from)
			if value := gjson.GetBytes(got, "max_output_tokens").Int(); value != tt.want {
				t.Fatalf("max_output_tokens = %d, want %d; body=%s", value, tt.want, got)
			}
		})
	}
}

func TestXAIResponsesToolsAndHistoryAdapter(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"plain","parameters":{"type":"object"}}],
		"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"lookup","namespace":"acme"}]},
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"acme","tools":[{"type":"custom","name":"lookup"}]}]},
			{"type":"custom_tool_call","call_id":"call-1","name":"lookup","input":{"query":"x"}},
			{"type":"custom_tool_call_output","call_id":"call-1","output":"done"},
			{"type":"function_call","call_id":"call-2","name":"lookup","namespace":"acme","arguments":"{}"}
		]
	}`)

	prepared, state := PrepareXAIResponsesTools(body)
	prepared = FinalizeXAIResponsesHistory(prepared)
	if gjson.GetBytes(prepared, `input.#(type=="additional_tools")`).Exists() {
		t.Fatalf("additional_tools leaked upstream: %s", prepared)
	}
	if !gjson.GetBytes(prepared, `tools.#(name=="acme__lookup")`).Exists() {
		t.Fatalf("flattened namespace tool missing: %s", prepared)
	}
	if got := gjson.GetBytes(prepared, "tool_choice.tools.0.name").String(); got != "acme__lookup" {
		t.Fatalf("tool choice name = %q, want acme__lookup", got)
	}
	if !gjson.GetBytes(prepared, `input.#(type=="function_call")#`).Exists() {
		t.Fatalf("custom history was not normalized: %s", prepared)
	}
	if gjson.GetBytes(prepared, `input.#(namespace!="")`).Exists() {
		t.Fatalf("namespace leaked in upstream history: %s", prepared)
	}

	event := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","name":"acme__lookup","call_id":"call-3"}}`)
	restored := state.RestoreResponse(event)
	if got := gjson.GetBytes(restored, "item.name").String(); got != "lookup" {
		t.Fatalf("restored name = %q, want lookup; event=%s", got, restored)
	}
	if got := gjson.GetBytes(restored, "item.namespace").String(); got != "acme" {
		t.Fatalf("restored namespace = %q, want acme; event=%s", got, restored)
	}
}

func TestPrepareXAIResponsesToolsNormalizesClaudeWebSearchToolChoice(t *testing.T) {
	body := []byte(`{
		"tool_choice":{"type":"tool","name":"web_search"},
		"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}]
	}`)

	prepared, _ := PrepareXAIResponsesTools(body)
	choice := gjson.GetBytes(prepared, "tool_choice")
	if got := choice.Get("type").String(); got != "allowed_tools" {
		t.Fatalf("tool_choice.type = %q, want allowed_tools; body=%s", got, prepared)
	}
	if got := choice.Get("mode").String(); got != "required" {
		t.Fatalf("tool_choice.mode = %q, want required; body=%s", got, prepared)
	}
	if got := choice.Get("tools.0.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.tools.0.type = %q, want web_search; body=%s", got, prepared)
	}
	if got := gjson.GetBytes(prepared, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search; body=%s", got, prepared)
	}
}

func TestPrepareXAIResponsesToolsKeepsImageGenerationForSupportedGrok(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.6",
		"tool_choice":{"type":"image_generation"},
		"tools":[{"type":"image_generation"}]
	}`)

	prepared, _ := PrepareXAIResponsesTools(body)
	if got := gjson.GetBytes(prepared, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, prepared)
	}
	choice := gjson.GetBytes(prepared, "tool_choice")
	if choice.Type != gjson.String || choice.String() != "required" {
		t.Fatalf("tool_choice = %s, want string required; body=%s", choice.Raw, prepared)
	}
}

func TestPrepareXAIResponsesToolsDropsImageGenerationForLegacyGrok(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.20",
		"tool_choice":{"type":"image_generation"},
		"tools":[{"type":"image_generation"}]
	}`)

	prepared, _ := PrepareXAIResponsesTools(body)
	if gjson.GetBytes(prepared, "tools").Exists() {
		t.Fatalf("legacy image_generation tools leaked: %s", prepared)
	}
	if gjson.GetBytes(prepared, "tool_choice").Exists() {
		t.Fatalf("legacy image_generation tool_choice leaked: %s", prepared)
	}
}

func TestXAIResponsesInternalSearchFilter(t *testing.T) {
	request := []byte(`{"tools":[{"type":"function","name":"x_keyword_search"}]}`)
	_, state := PrepareXAIResponsesTools(request)
	filter := state.NewResponseFilter(true)

	internal := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"item-1","type":"custom_tool_call","call_id":"xs_call-1","name":"x_keyword_search"}}`)
	if got := filter.Apply(internal); got != nil {
		t.Fatalf("internal x_search event was not filtered: %s", got)
	}

	client := []byte(`{"type":"response.output_item.done","output_index":1,"item":{"id":"item-2","type":"function_call","call_id":"call-2","name":"x_keyword_search"}}`)
	got := filter.Apply(client)
	if got == nil {
		t.Fatal("client-declared function call was filtered")
	}
	if index := gjson.GetBytes(got, "output_index").Int(); index != 0 {
		t.Fatalf("compacted output_index = %d, want 0; event=%s", index, got)
	}
}

func TestPrepareXAIResponsesToolsFoldsNamespacesForInjectedXSearch(t *testing.T) {
	var namespaces []string
	for i := 0; i < 20; i++ {
		var children []string
		for j := 0; j < 10; j++ {
			children = append(children, fmt.Sprintf(`{"type":"function","name":"tool_%d","parameters":{"type":"object"}}`, j))
		}
		namespaces = append(namespaces, fmt.Sprintf(`{"type":"namespace","name":"mcp__app_%d","tools":[%s]}`, i, strings.Join(children, ",")))
	}
	body := []byte(fmt.Sprintf(`{"model":"grok-4.6","tools":[%s],"input":"hi"}`, strings.Join(namespaces, ",")))

	prepared, state := PrepareXAIResponsesTools(body, XAIResponsesToolOptions{
		WillInjectXSearch: true,
		MaxTools:          200,
	})
	prepared = EnsureXAIResponsesNativeXSearchTool(prepared)
	prepared = state.ClampToolsLimit(prepared, 200)

	tools := gjson.GetBytes(prepared, "tools").Array()
	if len(tools) != 21 {
		t.Fatalf("tools length = %d, want 21; body=%s", len(tools), prepared)
	}
	if got := tools[0].Get("name").String(); got != "mcp__app_0" {
		t.Fatalf("tools.0.name = %q, want dispatcher name; body=%s", got, prepared)
	}
	if !gjson.GetBytes(prepared, `tools.#(type=="x_search")`).Exists() {
		t.Fatalf("x_search missing after injection; body=%s", prepared)
	}
}

func TestXAIResponsesToolStateRestoresDispatcherEvents(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"namespace","name":"mcp__app_0","tools":[{"type":"function","name":"tool_x","parameters":{"type":"object"}}]}]
	}`)
	_, state := PrepareXAIResponsesTools(body, XAIResponsesToolOptions{
		WillInjectXSearch: true,
		MaxTools:          1,
	})

	added := []byte(`{"type":"response.output_item.added","item":{"id":"item_1","type":"function_call","name":"mcp__app_0"}}`)
	restoredAdded := state.RestoreResponse(added)
	if got := gjson.GetBytes(restoredAdded, "item.namespace").String(); got != "mcp__app_0" {
		t.Fatalf("added namespace = %q, want mcp__app_0; event=%s", got, restoredAdded)
	}

	argsDone := []byte(`{"type":"response.function_call_arguments.done","item_id":"item_1","arguments":"{\"name\":\"tool_x\",\"arguments\":{\"count\":42}}"}`)
	restoredArgs := state.RestoreResponse(argsDone)
	if got := gjson.GetBytes(restoredArgs, "arguments").String(); got != `{"count":42}` {
		t.Fatalf("arguments = %q, want {\"count\":42}; event=%s", got, restoredArgs)
	}
}
