package oagmsg

import (
	"encoding/json"
	"reflect"
	"testing"
)

func testOpenAITool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "bash",
			"description": "Run a command",
			"parameters":  map[string]any{"type": "object"},
		},
	}
}

func testAnthropicTool() map[string]any {
	return map[string]any{
		"name":         "bash",
		"description":  "Run a command",
		"input_schema": map[string]any{"type": "object"},
	}
}

func testInteractionsTool() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "bash",
		"description": "Run a command",
		"parameters":  map[string]any{"type": "object"},
	}
}

func TestNormalizeToolDefinitionsAcrossFormats(t *testing.T) {
	openAI := NormalizeToolToOpenAI(testAnthropicTool())
	fn, ok := openAI["function"].(map[string]any)
	if !ok || openAI["type"] != "function" || fn["name"] != "bash" || fn["parameters"] == nil {
		t.Fatalf("Anthropic to OpenAI tool = %#v", openAI)
	}

	anthropic := NormalizeToolToAnthropic(testOpenAITool())
	if anthropic["name"] != "bash" || anthropic["input_schema"] == nil || anthropic["function"] != nil {
		t.Fatalf("OpenAI to Anthropic tool = %#v", anthropic)
	}

	interactions := NormalizeToolToInteractions(testAnthropicTool())
	if interactions["type"] != "function" || interactions["name"] != "bash" || interactions["parameters"] == nil || interactions["input_schema"] != nil {
		t.Fatalf("Anthropic to Interactions tool = %#v", interactions)
	}

	gemini := NormalizeToolToGemini(testInteractionsTool())
	decls, ok := gemini["functionDeclarations"].([]any)
	if !ok || len(decls) != 1 {
		t.Fatalf("Interactions to Gemini tool = %#v", gemini)
	}
}

func TestNormalizeToolChoiceAcrossFormats(t *testing.T) {
	anthropicChoice := map[string]any{"type": "tool", "name": "bash"}
	wantOpenAI := map[string]any{"type": "function", "function": map[string]any{"name": "bash"}}
	if got := NormalizeToolChoiceToOpenAI(anthropicChoice); !reflect.DeepEqual(got, wantOpenAI) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(wantOpenAI)
		t.Fatalf("OpenAI choice = %s, want %s", gotJSON, wantJSON)
	}

	openAIChoice := map[string]any{"type": "function", "function": map[string]any{"name": "bash"}}
	wantAnthropic := map[string]any{"type": "tool", "name": "bash"}
	if got := NormalizeToolChoiceToAnthropic(openAIChoice); !reflect.DeepEqual(got, wantAnthropic) {
		t.Fatalf("Anthropic choice = %#v, want %#v", got, wantAnthropic)
	}

	if got := NormalizeToolChoiceToInteractions(openAIChoice); !reflect.DeepEqual(got, map[string]any{"type": "function", "name": "bash"}) {
		t.Fatalf("Interactions choice = %#v", got)
	}
	webSearchChoice := map[string]any{"type": "tool", "name": "web_search"}
	if got := NormalizeToolChoiceToInteractions(webSearchChoice); !reflect.DeepEqual(got, map[string]any{"type": "web_search"}) {
		t.Fatalf("Interactions web_search choice = %#v", got)
	}
	if got := NormalizeToolChoiceToAnthropic("none"); got != nil {
		t.Fatalf("Anthropic none choice = %#v, want nil", got)
	}
}

func TestRequestToolRoundTripNormalizesToolsAndResults(t *testing.T) {
	raw := []byte(`{"model":"claude-sonnet","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"bash","description":"Run","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"bash"}}`)
	req, err := (&AnthropicHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&OpenAIHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	tool := decoded["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["function"].(map[string]any)["parameters"] == nil {
		t.Fatalf("OpenAI tool was not normalized: %s", out)
	}
	choice := decoded["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "bash" {
		t.Fatalf("OpenAI tool_choice was not normalized: %s", out)
	}
}
