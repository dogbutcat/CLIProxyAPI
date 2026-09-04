package oagmsg

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestResponsesAPISerializer_TextLifecycle verifies the full text content lifecycle:
// EventStart → EventTextDelta × 2 → EventUsage → EventDone → Flush
func TestResponsesAPISerializer_TextLifecycle(t *testing.T) {
	s := newResponsesAPISerializer("gpt-4o")

	var allLines [][]byte

	// EventStart
	lines := s.Serialize(StreamDelta{Type: EventStart, ID: "resp_001", Model: "gpt-4o", Created: 1700000000})
	allLines = append(allLines, lines...)

	// EventTextDelta × 2
	lines = s.Serialize(StreamDelta{Type: EventTextDelta, Content: "Hello"})
	allLines = append(allLines, lines...)
	lines = s.Serialize(StreamDelta{Type: EventTextDelta, Content: " world"})
	allLines = append(allLines, lines...)

	// EventUsage
	lines = s.Serialize(StreamDelta{Type: EventUsage, Usage: &UnifiedUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}})
	allLines = append(allLines, lines...)

	// EventDone
	lines = s.Serialize(StreamDelta{Type: EventDone, FinishReason: "stop"})
	allLines = append(allLines, lines...)

	// Flush
	lines = s.Flush()
	allLines = append(allLines, lines...)

	// Parse all event types
	eventTypes := extractEventTypes(allLines)

	// Expected lifecycle:
	expected := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",  // message item
		"response.content_part.added", // text content part
		"response.output_text.delta",  // "Hello"
		"response.output_text.delta",  // " world"
		"response.content_part.done",  // close text part (from Flush)
		"response.output_item.done",   // close message item (from Flush)
		"response.completed",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("event count = %d, want %d\ngot: %v", len(eventTypes), len(expected), eventTypes)
	}
	for i, want := range expected {
		if eventTypes[i] != want {
			t.Errorf("event[%d] = %q, want %q", i, eventTypes[i], want)
		}
	}

	// Verify response.completed has usage.
	completedLine := allLines[len(allLines)-2]
	completedData := extractSSEData(completedLine)
	root := gjson.ParseBytes(completedData)
	if root.Get("response.usage.input_tokens").Int() != 10 {
		t.Errorf("input_tokens = %d, want 10", root.Get("response.usage.input_tokens").Int())
	}
	if root.Get("response.usage.output_tokens").Int() != 5 {
		t.Errorf("output_tokens = %d, want 5", root.Get("response.usage.output_tokens").Int())
	}
	if terminal := strings.TrimSpace(string(allLines[len(allLines)-1])); terminal != "data: [DONE]" {
		t.Errorf("terminal line = %q, want data: [DONE]", terminal)
	}
}

func TestResponsesAPISerializer_GeminiMessageDoneKeepsOutputTextMetadata(t *testing.T) {
	s := newResponsesAPISerializer("gemini-test")
	s.geminiMode = true

	var allLines [][]byte
	allLines = append(allLines, s.Serialize(StreamDelta{Type: EventStart, ID: "resp_gemini", Model: "gemini-test"})...)
	allLines = append(allLines, s.Serialize(StreamDelta{Type: EventTextDelta, Content: "answer"})...)
	allLines = append(allLines, s.Serialize(StreamDelta{Type: EventDone, FinishReason: "stop"})...)
	allLines = append(allLines, s.Flush()...)

	var itemDone gjson.Result
	for _, line := range allLines {
		data := extractSSEData(line)
		if len(data) == 0 {
			continue
		}
		root := gjson.ParseBytes(data)
		if root.Get("type").String() == "response.output_item.done" && root.Get("item.type").String() == "message" {
			itemDone = root
			break
		}
	}
	if !itemDone.Exists() {
		t.Fatal("missing message response.output_item.done")
	}
	if !itemDone.Get("item.content.0.annotations").IsArray() {
		t.Fatalf("missing annotations array in message done: %s", itemDone.Raw)
	}
	if !itemDone.Get("item.content.0.logprobs").IsArray() {
		t.Fatalf("missing logprobs array in message done: %s", itemDone.Raw)
	}
}

func TestResponsesAPISerializer_BlankToolDoneDoesNotEmitFunctionCall(t *testing.T) {
	s := newResponsesAPISerializer("qwen-test")

	var allLines [][]byte
	allLines = append(allLines, s.Serialize(StreamDelta{Type: EventStart, ID: "resp_blank_tool", Model: "qwen-test"})...)
	allLines = append(allLines, s.Serialize(StreamDelta{Type: EventToolDone})...)
	allLines = append(allLines, s.Serialize(StreamDelta{Type: EventTextDelta, Content: "qwen-ok"})...)
	allLines = append(allLines, s.Serialize(StreamDelta{Type: EventDone, FinishReason: "stop"})...)
	allLines = append(allLines, s.Flush()...)

	for _, line := range allLines {
		data := extractSSEData(line)
		if len(data) == 0 {
			continue
		}
		root := gjson.ParseBytes(data)
		if root.Get("item.type").String() == "function_call" {
			t.Fatalf("blank function_call event emitted: %s", root.Raw)
		}
		for _, item := range root.Get("response.output").Array() {
			if item.Get("type").String() == "function_call" {
				t.Fatalf("blank function_call completed output emitted: %s", root.Raw)
			}
		}
	}
}

// TestResponsesAPISerializer_ToolLifecycle verifies function call lifecycle events.
func TestResponsesAPISerializer_ToolLifecycle(t *testing.T) {
	s := newResponsesAPISerializer("gpt-4o")

	var allLines [][]byte

	// Start
	lines := s.Serialize(StreamDelta{Type: EventStart, ID: "resp_002"})
	allLines = append(allLines, lines...)

	// Text first
	lines = s.Serialize(StreamDelta{Type: EventTextDelta, Content: "Let me check"})
	allLines = append(allLines, lines...)

	// Tool start
	lines = s.Serialize(StreamDelta{Type: EventToolStart, ToolCallID: "call_123", ToolName: "get_weather"})
	allLines = append(allLines, lines...)

	// Tool delta
	lines = s.Serialize(StreamDelta{Type: EventToolDelta, ToolCallID: "call_123", ToolArgs: `{"city":"SF"}`})
	allLines = append(allLines, lines...)

	// Tool done
	lines = s.Serialize(StreamDelta{Type: EventToolDone, ToolCallID: "call_123", ToolArgs: `{"city":"SF"}`})
	allLines = append(allLines, lines...)

	// Done + Flush
	lines = s.Serialize(StreamDelta{Type: EventDone, FinishReason: "tool_calls"})
	allLines = append(allLines, lines...)
	lines = s.Flush()
	allLines = append(allLines, lines...)

	eventTypes := extractEventTypes(allLines)

	// Expected:
	expected := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",  // message
		"response.content_part.added", // text content
		"response.output_text.delta",  // "Let me check"
		"response.content_part.done",  // close text part (before tool)
		"response.output_item.added",  // function_call
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done", // function_call done
		"response.output_item.done", // message done (from Flush)
		"response.completed",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("event count = %d, want %d\ngot: %v", len(eventTypes), len(expected), eventTypes)
	}
	for i, want := range expected {
		if eventTypes[i] != want {
			t.Errorf("event[%d] = %q, want %q", i, eventTypes[i], want)
		}
	}
}

func TestResponsesAPISerializer_OpenAIToolFirstNoRoleFlushesOnDoneWithUsage(t *testing.T) {
	session, err := NewStreamSession(FormatOpenAI, FormatOpenAIResponse, "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	var allLines [][]byte
	chunks, err := session.Translate([]byte(`data: {"id":"chatcmpl_tool_first","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	allLines = append(allLines, chunks...)
	chunks, err = session.Translate([]byte(`data: {"id":"chatcmpl_tool_first","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	if err != nil {
		t.Fatal(err)
	}
	allLines = append(allLines, chunks...)
	chunks, err = session.Translate([]byte(`data: [DONE]`))
	if err != nil {
		t.Fatal(err)
	}
	allLines = append(allLines, chunks...)

	joined := strings.Join(byteLinesToStrings(allLines), "\n")
	if strings.Count(joined, `"type":"response.created"`) != 1 || strings.Count(joined, `"type":"response.in_progress"`) != 1 {
		t.Fatalf("start lifecycle count malformed: %s", joined)
	}
	if strings.Count(joined, `"type":"response.completed"`) != 1 {
		t.Fatalf("completed count malformed: %s", joined)
	}
	if strings.Count(joined, "data: [DONE]") != 1 {
		t.Fatalf("[DONE] count malformed: %s", joined)
	}
	completed := lastEventDataByType(t, allLines, "response.completed")
	if completed.Get("response.usage.input_tokens").Int() != 3 || completed.Get("response.usage.output_tokens").Int() != 4 || completed.Get("response.usage.total_tokens").Int() != 7 {
		t.Fatalf("usage not preserved in completed: %s", completed.Raw)
	}
	output := completed.Get("response.output").Array()
	if len(output) != 1 || output[0].Get("type").String() != "function_call" || output[0].Get("name").String() != "lookup" {
		t.Fatalf("tool-first output malformed: %s", completed.Get("response.output").Raw)
	}

	bare, err := NewStreamSession(FormatOpenAI, FormatOpenAIResponse, "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	chunks, err = bare.Translate([]byte(`data: [DONE]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("bare pre-start DONE emitted %d frames", len(chunks))
	}
}

// TestResponsesAPISerializer_ThinkingDelta verifies reasoning events.
func TestResponsesAPISerializer_ThinkingDelta(t *testing.T) {
	s := newResponsesAPISerializer("gpt-4o")

	var allLines [][]byte

	lines := s.Serialize(StreamDelta{Type: EventStart, ID: "resp_003"})
	allLines = append(allLines, lines...)

	lines = s.Serialize(StreamDelta{Type: EventThinkingDelta, Content: "hmm..."})
	allLines = append(allLines, lines...)

	lines = s.Serialize(StreamDelta{Type: EventTextDelta, Content: "answer"})
	allLines = append(allLines, lines...)

	s.Serialize(StreamDelta{Type: EventDone, FinishReason: "stop"})
	lines = s.Flush()
	allLines = append(allLines, lines...)

	eventTypes := extractEventTypes(allLines)

	// Should have reasoning_summary_text.delta before text events.
	foundReasoning := false
	foundText := false
	for _, et := range eventTypes {
		if et == "response.reasoning_summary_text.delta" {
			foundReasoning = true
		}
		if et == "response.output_text.delta" {
			foundText = true
		}
	}
	if !foundReasoning {
		t.Error("Missing response.reasoning_summary_text.delta")
	}
	if !foundText {
		t.Error("Missing response.output_text.delta")
	}
}

// TestResponsesAPISerializer_RegistryLookup verifies FormatOpenAIResponse has its own handler.
func TestResponsesAPISerializer_RegistryLookup(t *testing.T) {
	r := DefaultRegistry()

	// FormatOpenAIResponse should resolve to its own handler (not fallback to Interactions).
	h, ok := r.Get(FormatOpenAIResponse)
	if !ok {
		t.Fatal("FormatOpenAIResponse handler not found")
	}

	// It should be an InteractionsHandler (same parser, different serializer).
	ih, ok := h.(*InteractionsHandler)
	if !ok {
		t.Fatalf("Handler type = %T, want *InteractionsHandler", h)
	}
	if ih.Mode != InteractionsModeResponsesAPI {
		t.Errorf("Mode = %d, want InteractionsModeResponsesAPI (%d)", ih.Mode, InteractionsModeResponsesAPI)
	}
}

// ----------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------

func extractEventTypes(lines [][]byte) []string {
	var types []string
	for _, line := range lines {
		s := string(line)
		if strings.HasPrefix(s, "event: ") {
			parts := strings.SplitN(s, "\n", 2)
			eventType := strings.TrimPrefix(parts[0], "event: ")
			types = append(types, eventType)
		}
	}
	return types
}

func extractSSEData(line []byte) []byte {
	s := string(line)
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "data: ") {
			return []byte(strings.TrimPrefix(l, "data: "))
		}
	}
	return nil
}

func byteLinesToStrings(lines [][]byte) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, string(line))
	}
	return out
}

func lastEventDataByType(t *testing.T, lines [][]byte, eventType string) gjson.Result {
	t.Helper()
	var result gjson.Result
	for _, line := range lines {
		text := string(line)
		if !strings.HasPrefix(text, "event: "+eventType+"\n") {
			continue
		}
		result = gjson.ParseBytes(extractSSEData(line))
	}
	if !result.Exists() {
		t.Fatalf("missing event %q", eventType)
	}
	return result
}
