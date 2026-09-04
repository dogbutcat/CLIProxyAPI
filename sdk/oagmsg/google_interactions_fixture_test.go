// Package oagmsg — google_interactions_fixture_test.go provides real-world
// fixture data extracted from the internal/translator/gemini/interactions
// oracle implementation. These fixtures document the actual Google Interactions
// wire protocol and verify the native Google Interactions handler.
//
// Key protocol differences from Codex/OpenAI Responses:
//   - Events use "event_type" field (not "type")
//   - Event names: interaction.*, step.* (not response.*)
//   - Request: system_instruction (not instructions), input[] items have step-like types
//   - NonStream response: steps[] array (not output[])
//   - Step delta types: "text", "thought_summary", "thought_signature", "arguments_delta"
package oagmsg

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// =====================================================
// Google Interactions Request Fixtures
// =====================================================

// interactionsRequestBasicText is a minimal Google Interactions request
// with system_instruction and a single text input step.
const interactionsRequestBasicText = `{
	"model": "gemini-2.0-flash",
	"system_instruction": "You are a helpful assistant.",
	"input": [
		{"type": "user_input", "content": [{"type": "text", "text": "Hello, world!"}]}
	],
	"stream": true
}`

// interactionsRequestMultiTurn is a Google Interactions request with
// multiple conversation turns including model output.
const interactionsRequestMultiTurn = `{
	"model": "gemini-2.0-flash",
	"system_instruction": "You are a coding assistant.",
	"input": [
		{"type": "user_input", "content": [{"type": "text", "text": "Write a hello world in Go"}]},
		{"type": "model_output", "content": [{"type": "text", "text": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello, world!\")\n}"}]},
		{"type": "user_input", "content": [{"type": "text", "text": "Now in Python"}]}
	]
}`

// interactionsRequestToolCall is a Google Interactions request with
// function call history and a function_call step.
const interactionsRequestToolCall = `{
	"model": "gemini-2.0-flash",
	"input": [
		{"type": "user_input", "content": [{"type": "text", "text": "What is the weather in SF?"}]},
		{"type": "function_call", "name": "get_weather", "call_id": "call_1", "arguments": "{\"city\":\"SF\"}"},
		{"type": "function_result", "name": "get_weather", "call_id": "call_1", "result": {"temperature": 72, "condition": "sunny"}}
	],
	"tools": [
		{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}
	]
}`

// interactionsRequestThinking is a Google Interactions request with
// thinking/reasoning configuration.
const interactionsRequestThinking = `{
	"model": "gemini-2.5-flash",
	"system_instruction": "Think carefully.",
	"input": [
		{"type": "user_input", "content": [{"type": "text", "text": "Explain quantum entanglement"}]}
	],
	"generation_config": {
		"thinking_level": "medium",
		"thinking_summaries": "auto"
	}
}`

// =====================================================
// Google Interactions Stream Event Fixtures
// =====================================================

// interactionsStreamTextEvents is a complete Google Interactions text
// stream lifecycle: created → status_update → step.start → step.delta → step.stop → completed
var interactionsStreamTextEvents = []string{
	`{"event_type":"interaction.created","interaction":{"id":"int_abc123","model":"gemini-2.0-flash","status":"in_progress","created_at":1700000000}}`,
	`{"event_type":"interaction.status_update","interaction":{"id":"int_abc123","status":"in_progress"}}`,
	`{"event_type":"step.start","index":0,"step":{"name":"model_output","type":"model_output","id":"step_0"}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hello"}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":" world"}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"!"}}`,
	`{"event_type":"step.stop","index":0}`,
	`{"event_type":"interaction.completed","interaction":{"id":"int_abc123","model":"gemini-2.0-flash","status":"completed","usage":{"total_input_tokens":10,"input_tokens_by_modality":[{"modality":"text","tokens":10}],"total_output_tokens":3,"total_tokens":13,"total_cached_tokens":0,"total_tool_use_tokens":0,"total_thought_tokens":0}}}`,
}

// interactionsStreamToolCallEvents is a Google Interactions stream
// with a function_call step.
var interactionsStreamToolCallEvents = []string{
	`{"event_type":"interaction.created","interaction":{"id":"int_tool1","model":"gemini-2.0-flash","status":"in_progress"}}`,
	`{"event_type":"step.start","index":0,"step":{"name":"get_weather","type":"function_call","id":"step_fc1","call_id":"call_abc"}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"city\":"}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"SF\"}"}}`,
	`{"event_type":"step.stop","index":0}`,
	`{"event_type":"interaction.completed","interaction":{"id":"int_tool1","status":"completed","usage":{"total_input_tokens":15,"total_output_tokens":8,"total_tokens":23,"total_cached_tokens":0,"total_tool_use_tokens":0,"total_thought_tokens":0}}}`,
}

// interactionsStreamThinkingEvents is a Google Interactions stream
// with thought_summary and thought_signature steps.
var interactionsStreamThinkingEvents = []string{
	`{"event_type":"interaction.created","interaction":{"id":"int_think1","model":"gemini-2.5-flash","status":"in_progress"}}`,
	`{"event_type":"step.start","index":0,"step":{"name":"thought","type":"thought","id":"step_t1"}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"text":"Let me think about this..."}}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_encrypted_abc"}}`,
	`{"event_type":"step.stop","index":0}`,
	`{"event_type":"step.start","index":1,"step":{"name":"model_output","type":"model_output","id":"step_o1"}}`,
	`{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"The answer is 42."}}`,
	`{"event_type":"step.stop","index":1}`,
	`{"event_type":"interaction.completed","interaction":{"id":"int_think1","status":"completed","usage":{"total_input_tokens":20,"total_output_tokens":10,"total_tokens":35,"total_cached_tokens":0,"total_tool_use_tokens":0,"total_thought_tokens":5}}}`,
}

// interactionsStreamMultiToolEvents has parallel function calls.
var interactionsStreamMultiToolEvents = []string{
	`{"event_type":"interaction.created","interaction":{"id":"int_mt1","model":"gemini-2.0-flash","status":"in_progress"}}`,
	`{"event_type":"step.start","index":0,"step":{"name":"get_weather","type":"function_call","call_id":"call_w1"}}`,
	`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"city\":\"SF\"}"}}`,
	`{"event_type":"step.stop","index":0}`,
	`{"event_type":"step.start","index":1,"step":{"name":"get_weather","type":"function_call","call_id":"call_w2"}}`,
	`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"city\":\"LA\"}"}}`,
	`{"event_type":"step.stop","index":1}`,
	`{"event_type":"interaction.completed","interaction":{"id":"int_mt1","status":"completed","usage":{"total_input_tokens":12,"total_output_tokens":6,"total_tokens":18,"total_cached_tokens":0,"total_tool_use_tokens":0,"total_thought_tokens":0}}}`,
}

// =====================================================
// Google Interactions NonStream Response Fixtures
// =====================================================

// interactionsNonStreamTextResponse is a complete Google Interactions
// non-stream response with text output.
const interactionsNonStreamTextResponse = `{
	"id": "int_ns1",
	"model": "gemini-2.0-flash",
	"status": "completed",
	"steps": [
		{"type": "model_output", "content": [{"type": "text", "text": "Hello! How can I help you today?"}]}
	],
	"usage": {"input_tokens": 5, "output_tokens": 8, "total_tokens": 13}
}`

// interactionsNonStreamToolResponse is a Google Interactions non-stream
// response with a function call.
const interactionsNonStreamToolResponse = `{
	"id": "int_ns_tool",
	"model": "gemini-2.0-flash",
	"status": "completed",
	"steps": [
		{"type": "function_call", "name": "get_weather", "call_id": "call_ns1", "arguments": "{\"city\":\"SF\"}"}
	],
	"usage": {"input_tokens": 10, "output_tokens": 5}
}`

// interactionsNonStreamThinkingResponse is a Google Interactions non-stream
// response with thought and model output steps.
const interactionsNonStreamThinkingResponse = `{
	"id": "int_ns_think",
	"model": "gemini-2.5-flash",
	"status": "completed",
	"steps": [
		{"type": "thought", "content": [{"type": "text", "text": "Let me reason through this..."}]},
		{"type": "model_output", "content": [{"type": "text", "text": "The answer is 42."}]}
	],
	"usage": {"input_tokens": 15, "output_tokens": 10, "reasoning_tokens": 5}
}`

// interactionsNonStreamErrorResponse is a Google Interactions error response.
const interactionsNonStreamErrorResponse = `{
	"id": "int_ns_err",
	"model": "gemini-2.0-flash",
	"status": "failed",
	"error": {
		"message": "Content filter triggered",
		"type": "invalid_request_error",
		"code": 400
	}
}`

// =====================================================
// Fixture assertion tests
// =====================================================

func TestGoogleInteractionsFixture_RequestsHaveRequiredFields(t *testing.T) {
	fixtures := map[string]string{
		"basic_text": interactionsRequestBasicText,
		"multi_turn": interactionsRequestMultiTurn,
		"tool_call":  interactionsRequestToolCall,
		"thinking":   interactionsRequestThinking,
	}

	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			if !json.Valid([]byte(fixture)) {
				t.Fatalf("fixture is invalid JSON: %s", fixture)
			}
			assertJSONField(t, fixture, "model", "request must have model")
			assertJSONField(t, fixture, "input", "request must have input[]")
			// Google Interactions requests use system_instruction, NOT instructions
			if name != "tool_call" { // tool_call fixture has no system
				assertJSONField(t, fixture, "system_instruction", "request must use system_instruction (not instructions)")
			}
			assertNoJSONField(t, fixture, "instructions", "request must NOT use instructions (that's Responses API)")
			req, err := (&GoogleInteractionsHandler{}).ParseRequest([]byte(fixture))
			if err != nil {
				t.Fatalf("native handler rejected fixture: %v", err)
			}
			if req.SourceFormat != FormatInteractions || len(req.Messages) == 0 {
				t.Fatalf("parsed request = %#v", req)
			}
		})
	}
}

func TestGoogleInteractionsFixture_StreamEventsHaveEventType(t *testing.T) {
	allStreams := map[string][]string{
		"text":       interactionsStreamTextEvents,
		"tool_call":  interactionsStreamToolCallEvents,
		"thinking":   interactionsStreamThinkingEvents,
		"multi_tool": interactionsStreamMultiToolEvents,
	}

	for name, events := range allStreams {
		t.Run(name, func(t *testing.T) {
			for i, evt := range events {
				if !json.Valid([]byte(evt)) {
					t.Fatalf("stream event %d is invalid JSON: %s", i, evt)
				}
				assertJSONField(t, evt, "event_type", "stream event %d must have event_type", i)
			}
		})
	}
}

func TestGoogleInteractionsFixture_NonStreamResponsesHaveSteps(t *testing.T) {
	fixtures := map[string]string{
		"text":     interactionsNonStreamTextResponse,
		"tool":     interactionsNonStreamToolResponse,
		"thinking": interactionsNonStreamThinkingResponse,
	}

	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			if !json.Valid([]byte(fixture)) {
				t.Fatalf("fixture is invalid JSON: %s", fixture)
			}
			assertJSONField(t, fixture, "id", "response must have id")
			assertJSONField(t, fixture, "status", "response must have status")
			assertJSONField(t, fixture, "steps", "response must use steps[] (not output[])")
			assertNoJSONField(t, fixture, "output", "response must NOT use output[] (that's Responses API)")
			response, err := (&GoogleInteractionsHandler{}).ParseResponse([]byte(fixture))
			if err != nil {
				t.Fatalf("native handler rejected fixture: %v", err)
			}
			if response.ID == "" || response.FinishReason == "" {
				t.Fatalf("parsed response = %#v", response)
			}
		})
	}
}

func TestGoogleInteractionsFixture_ErrorIsConsumed(t *testing.T) {
	if !json.Valid([]byte(interactionsNonStreamErrorResponse)) {
		t.Fatal("error fixture is invalid JSON")
	}
	unified := parseUnifiedError([]byte(interactionsNonStreamErrorResponse))
	if unified == nil || unified.StatusCode != 400 || unified.ErrorType != "invalid_request_error" {
		t.Fatalf("parsed error = %#v", unified)
	}
	formatted, err := (&GoogleInteractionsHandler{}).FormatError(unified)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(formatted, "status").String() != "failed" {
		t.Fatalf("formatted error = %s", formatted)
	}
}

func TestGoogleInteractionsFixture_StreamUsageUsesCanonicalOracleKeys(t *testing.T) {
	completed := interactionsStreamThinkingEvents[len(interactionsStreamThinkingEvents)-1]
	usage := gjson.Get(completed, "interaction.usage")
	for _, key := range []string{"total_input_tokens", "total_output_tokens", "total_tokens", "total_thought_tokens"} {
		if !usage.Get(key).Exists() {
			t.Fatalf("oracle usage missing %s: %s", key, completed)
		}
	}
	if usage.Get("input_tokens").Exists() || usage.Get("reasoning_tokens").Exists() {
		t.Fatalf("stream fixture uses non-canonical usage aliases: %s", completed)
	}
}

// =====================================================
// Helpers for fixture validation
// =====================================================

func assertJSONField(t *testing.T, json, field, msg string, args ...any) {
	t.Helper()
	if !gjsonValid(json, field) {
		t.Errorf(msg, args...)
	}
}

func assertNoJSONField(t *testing.T, json, field, msg string, args ...any) {
	t.Helper()
	if gjsonValid(json, field) {
		t.Errorf(msg, args...)
	}
}

func gjsonValid(jsonStr, path string) bool {
	return json.Valid([]byte(jsonStr)) && gjson.Get(jsonStr, path).Exists()
}
