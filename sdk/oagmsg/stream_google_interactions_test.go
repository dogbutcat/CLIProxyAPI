package oagmsg

import (
	"strings"
	"testing"
)

func TestGoogleInteractionsStreamParserToolThinkingAndUsage(t *testing.T) {
	handler := &GoogleInteractionsHandler{}
	tests := []struct {
		name  string
		event string
		check func(t *testing.T, delta StreamDelta)
	}{
		{
			name:  "tool start",
			event: `{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call_1","name":"lookup"}}`,
			check: func(t *testing.T, delta StreamDelta) {
				if delta.Type != EventToolStart || delta.ToolIndex != 2 || delta.ToolCallID != "call_1" || delta.ToolName != "lookup" {
					t.Fatalf("tool delta = %#v", delta)
				}
			},
		},
		{
			name:  "tool arguments",
			event: `{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"q\":\"x\"}"}}`,
			check: func(t *testing.T, delta StreamDelta) {
				if delta.Type != EventToolDelta || delta.ToolIndex != 2 || delta.ToolArgs != `{"q":"x"}` {
					t.Fatalf("arguments delta = %#v", delta)
				}
			},
		},
		{
			name:  "thinking summary",
			event: `{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"text":"plan"}}}`,
			check: func(t *testing.T, delta StreamDelta) {
				if delta.Type != EventThinkingDelta || delta.Content != "plan" {
					t.Fatalf("thinking delta = %#v", delta)
				}
			},
		},
		{
			name:  "thinking signature",
			event: `{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_1"}}`,
			check: func(t *testing.T, delta StreamDelta) {
				if delta.Type != EventThinkingDelta || delta.Signature != "sig_1" {
					t.Fatalf("signature delta = %#v", delta)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deltas, err := handler.ParseStreamChunk([]byte(tt.event))
			if err != nil {
				t.Fatal(err)
			}
			if len(deltas) != 1 {
				t.Fatalf("deltas = %#v", deltas)
			}
			tt.check(t, deltas[0])
		})
	}

	deltas, err := handler.ParseStreamChunk([]byte(`event: interaction.completed
data: {"event_type":"interaction.completed","interaction":{"status":"completed","usage":{"total_input_tokens":10,"total_output_tokens":4,"total_tokens":14,"total_cached_tokens":2,"total_thought_tokens":3}}}

`))
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 2 || deltas[0].Type != EventDone || deltas[1].Type != EventUsage {
		t.Fatalf("completed deltas = %#v", deltas)
	}
	usage := deltas[1].Usage
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 4 || usage.CachedTokens != 2 || usage.ReasoningTokens != 3 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestGoogleInteractionsStreamParserStatefulToolLifecycle(t *testing.T) {
	handler := &GoogleInteractionsHandler{}
	state := &streamParseState{}

	deltas, err := handler.parseStreamChunkWithState([]byte(`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"step_1","call_id":"call_1","name":"lookup"}}`), state)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventToolStart || deltas[0].ToolCallID != "call_1" || deltas[0].ToolName != "lookup" {
		t.Fatalf("tool start deltas = %#v", deltas)
	}

	deltas, err = handler.parseStreamChunkWithState([]byte(`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"q\":\"x\"}"}}`), state)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventToolDelta || deltas[0].ToolCallID != "call_1" || deltas[0].ToolName != "lookup" || deltas[0].ToolArgs != `{"q":"x"}` {
		t.Fatalf("tool delta deltas = %#v", deltas)
	}

	deltas, err = handler.parseStreamChunkWithState([]byte(`{"event_type":"step.stop","index":2}`), state)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventToolDone || deltas[0].ToolCallID != "call_1" || deltas[0].ToolName != "lookup" {
		t.Fatalf("tool done deltas = %#v", deltas)
	}
}

func TestGoogleInteractionsStreamParserIgnoresNonToolStepStop(t *testing.T) {
	handler := &GoogleInteractionsHandler{}
	state := &streamParseState{}

	if _, err := handler.parseStreamChunkWithState([]byte(`{"event_type":"step.start","index":1,"step":{"type":"model_output","id":"step_text"}}`), state); err != nil {
		t.Fatal(err)
	}
	deltas, err := handler.parseStreamChunkWithState([]byte(`{"event_type":"step.stop","index":1}`), state)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 0 {
		t.Fatalf("non-tool stop deltas = %#v", deltas)
	}
}

func TestGoogleInteractionsStreamParserResponseFailedAlias(t *testing.T) {
	handler := &GoogleInteractionsHandler{}
	deltas, err := handler.ParseStreamChunk([]byte(`{"event_type":"response.failed","error":{"message":"server overload","code":"503"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventError || deltas[0].ErrorType != "503" || deltas[0].ErrorMessage != "server overload" {
		t.Fatalf("failed deltas = %#v", deltas)
	}
}

func TestGoogleInteractionsRequestRoundTrip(t *testing.T) {
	handler := &GoogleInteractionsHandler{}
	req, err := handler.ParseRequest([]byte(interactionsRequestToolCall))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 3 || len(req.Tools) != 1 {
		t.Fatalf("request = %#v", req)
	}
	out, err := handler.SerializeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"type":"user_input"`, `"type":"function_call"`, `"type":"function_result"`, `"system_instruction"`} {
		if field == `"system_instruction"` {
			continue
		}
		if !strings.Contains(string(out), field) {
			t.Fatalf("round-trip output missing %s: %s", field, out)
		}
	}
	if strings.Contains(string(out), `"instructions"`) {
		t.Fatalf("round-trip output used Responses field: %s", out)
	}
}

func TestGoogleInteractionsSerializerCanonicalUsageAndSignature(t *testing.T) {
	serializer := (&GoogleInteractionsHandler{}).NewStreamSerializer("gemini-test")
	serializer.Serialize(StreamDelta{Type: EventStart, ID: "int_1"})
	signatureOutput := serializer.Serialize(StreamDelta{Type: EventThinkingDelta, Signature: "sig_1"})
	serializer.Serialize(StreamDelta{Type: EventUsage, Usage: &UnifiedUsage{
		PromptTokens: 7, CompletionTokens: 5, TotalTokens: 12, CachedTokens: 2, ReasoningTokens: 3,
	}})
	combined := joinOutputs(append(signatureOutput, serializer.Flush()...))
	for _, expected := range []string{"thought_signature", "sig_1", "total_input_tokens", "total_output_tokens", "total_cached_tokens", "total_thought_tokens"} {
		if !strings.Contains(combined, expected) {
			t.Fatalf("serializer output missing %s: %s", expected, combined)
		}
	}
	if strings.Contains(combined, `"reasoning_tokens"`) {
		t.Fatalf("serializer emitted non-canonical stream usage key: %s", combined)
	}
}
