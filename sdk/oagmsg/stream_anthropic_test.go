package oagmsg

import (
	"testing"
)

// --- Anthropic Parser Tests ---

func TestAnthropic_ParseStreamChunk_MessageStart(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"message_start","message":{"id":"msg_123","model":"claude-3","usage":{"input_tokens":100}}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) < 1 || deltas[0].Type != EventStart {
		t.Fatalf("expected EventStart, got %+v", deltas)
	}
	if deltas[0].ID != "msg_123" || deltas[0].Model != "claude-3" {
		t.Fatalf("wrong metadata: %+v", deltas[0])
	}
	// Should also have EventUsage.
	if len(deltas) < 2 || deltas[1].Type != EventUsage || deltas[1].Usage.PromptTokens != 100 {
		t.Fatalf("expected EventUsage with prompt_tokens=100, got %+v", deltas)
	}
}

func TestAnthropic_ParseStreamChunk_TextDelta(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventTextDelta || deltas[0].Content != "Hello" {
		t.Fatalf("expected TextDelta Hello, got %+v", deltas)
	}
}

func TestAnthropic_ParseStreamChunk_ThinkingDelta(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning..."}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventThinkingDelta || deltas[0].Content != "reasoning..." {
		t.Fatalf("expected ThinkingDelta, got %+v", deltas)
	}
}

func TestAnthropic_ParseStreamChunk_ToolStart(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"get_weather"}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventToolStart {
		t.Fatalf("expected ToolStart, got %+v", deltas)
	}
	if deltas[0].ToolCallID != "toolu_123" || deltas[0].ToolName != "get_weather" {
		t.Fatalf("wrong tool info: %+v", deltas[0])
	}
}

func TestAnthropic_ParseStreamChunk_InputJsonDelta(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventToolDelta || deltas[0].ToolArgs != `{"city":` {
		t.Fatalf("expected ToolDelta, got %+v", deltas)
	}
}

func TestAnthropic_ParseStreamChunk_MessageDelta(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	foundUsage := false
	for _, d := range deltas {
		if d.Type == EventDone {
			t.Fatalf("message_delta must not complete the stream: %+v", deltas)
		}
		if d.Type == EventUsage && d.Usage != nil && d.Usage.CompletionTokens == 50 {
			if got := stringValue(d.Extra["anthropic_stop_reason"]); got != "stop" {
				t.Fatalf("stop reason metadata = %q, want stop", got)
			}
			foundUsage = true
		}
	}
	if !foundUsage {
		t.Fatalf("expected EventUsage with output_tokens=50, got %+v", deltas)
	}
}

func TestAnthropic_ParseStreamChunk_MessageStop(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"message_stop"}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventDone || deltas[0].FinishReason != "stop" {
		t.Fatalf("expected message_stop EventDone, got %+v", deltas)
	}
}

func TestAnthropic_StreamSessionPreservesStopSequenceOnMessageStop(t *testing.T) {
	session, err := NewStreamSession(FormatAnthropic, FormatAnthropic, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := session.Translate([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"<END>"},"usage":{"output_tokens":4}}`)); err != nil || len(out) != 0 {
		t.Fatalf("message_delta output=%q err=%v", out, err)
	}
	out, err := session.Translate([]byte(`data: {"type":"message_stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := string(joinRuntimeOutputs(out))
	assertContains(t, joined, `"stop_sequence":"<END>"`)
}

func TestAnthropic_ParseStreamChunk_Ping(t *testing.T) {
	h := &AnthropicHandler{}
	raw := []byte(`{"type":"ping"}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventPing {
		t.Fatalf("expected EventPing, got %+v", deltas)
	}
}

// --- Anthropic Serializer Tests ---

func TestAnthropic_Serializer_TextStream(t *testing.T) {
	h := &AnthropicHandler{}
	ser := h.NewStreamSerializer("claude-3")

	// EventStart
	chunks := ser.Serialize(StreamDelta{Type: EventStart, ID: "msg_1", Model: "claude-3"})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for start, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), "event: message_start")
	assertContains(t, string(chunks[0]), `"msg_1"`)

	// EventTextDelta
	chunks = ser.Serialize(StreamDelta{Type: EventTextDelta, Content: "Hello"})
	if len(chunks) < 1 {
		t.Fatal("expected chunks for text delta")
	}
	// Should include content_block_start and content_block_delta.
	combined := ""
	for _, c := range chunks {
		combined += string(c)
	}
	assertContains(t, combined, "content_block_start")
	assertContains(t, combined, `"text_delta"`)

	// Flush
	flushChunks := ser.Flush()
	combined = ""
	for _, c := range flushChunks {
		combined += string(c)
	}
	assertContains(t, combined, "content_block_stop")
	assertContains(t, combined, "message_delta")
	assertContains(t, combined, "message_stop")
}

func TestAnthropic_Serializer_ToolStream(t *testing.T) {
	h := &AnthropicHandler{}
	ser := h.NewStreamSerializer("claude-3")

	// EventStart
	ser.Serialize(StreamDelta{Type: EventStart, ID: "msg_1"})

	// EventToolStart
	chunks := ser.Serialize(StreamDelta{Type: EventToolStart, ToolCallID: "toolu_1", ToolName: "search"})
	combined := ""
	for _, c := range chunks {
		combined += string(c)
	}
	assertContains(t, combined, "content_block_start")
	assertContains(t, combined, `"tool_use"`)
	assertContains(t, combined, `"toolu_1"`)

	// EventToolDelta
	chunks = ser.Serialize(StreamDelta{Type: EventToolDelta, ToolArgs: `{"q":`})
	if len(chunks) < 1 {
		t.Fatal("expected tool delta chunks")
	}
	assertContains(t, string(chunks[0]), "input_json_delta")

	// Flush should close tool block
	flushChunks := ser.Flush()
	combined = ""
	for _, c := range flushChunks {
		combined += string(c)
	}
	assertContains(t, combined, "content_block_stop")
	assertContains(t, combined, "message_delta")
}
