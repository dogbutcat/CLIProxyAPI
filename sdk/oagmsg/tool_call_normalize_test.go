package oagmsg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeToolCallsAcrossFormats(t *testing.T) {
	openAI := map[string]any{
		"id":   "call_1",
		"type": "function",
		"function": map[string]any{
			"name":      "calc",
			"arguments": `{"expr":"1+1"}`,
		},
	}
	anthropic := NormalizeToolCallToAnthropic(openAI)
	if anthropic["type"] != "tool_use" || anthropic["id"] != "call_1" || anthropic["name"] != "calc" {
		t.Fatalf("OpenAI to Anthropic call = %#v", anthropic)
	}
	if input := anthropic["input"].(map[string]any); input["expr"] != "1+1" {
		t.Fatalf("Anthropic input = %#v", input)
	}

	interactions := NormalizeToolCallToInteractions(anthropic)
	if interactions["type"] != "function_call" || interactions["call_id"] != "call_1" {
		t.Fatalf("Anthropic to Interactions call = %#v", interactions)
	}
	if _, ok := interactions["arguments"].(string); !ok {
		t.Fatalf("Interactions arguments are not a string: %#v", interactions)
	}

	gemini := NormalizeToolCallToGemini(interactions)
	fc := gemini["functionCall"].(map[string]any)
	if fc["name"] != "calc" || fc["id"] != "call_1" {
		t.Fatalf("Interactions to Gemini call = %#v", gemini)
	}

	back := NormalizeToolCallToOpenAI(gemini)
	fn := back["function"].(map[string]any)
	if back["id"] != "call_1" || fn["name"] != "calc" {
		t.Fatalf("Gemini to OpenAI call = %#v", back)
	}
}

func TestNormalizeToolCallGeneratesMissingID(t *testing.T) {
	gemini := map[string]any{"functionCall": map[string]any{"name": "get weather", "args": map[string]any{"city": "Tokyo"}}}
	got := NormalizeToolCallToOpenAI(gemini)
	id, _ := got["id"].(string)
	if !strings.HasPrefix(id, "call_get_weather_") {
		t.Fatalf("generated id = %q", id)
	}
	fn := got["function"].(map[string]any)
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatal(err)
	}
	if args["city"] != "Tokyo" {
		t.Fatalf("args = %#v", args)
	}
}
