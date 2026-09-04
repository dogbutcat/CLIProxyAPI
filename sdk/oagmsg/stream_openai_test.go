package oagmsg

import (
	"testing"
)

// --- OpenAI Parser Tests ---

func TestOpenAI_ParseStreamChunk_Start(t *testing.T) {
	h := &OpenAIHandler{}
	raw := []byte(`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventStart {
		t.Fatalf("expected EventStart, got %+v", deltas)
	}
	if deltas[0].ID != "chatcmpl-123" || deltas[0].Model != "gpt-4" || deltas[0].Created != 1700000000 {
		t.Fatalf("wrong metadata: %+v", deltas[0])
	}
}

func TestOpenAI_ParseStreamChunk_TextDelta(t *testing.T) {
	h := &OpenAIHandler{}
	raw := []byte(`{"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventTextDelta || deltas[0].Content != "Hello" {
		t.Fatalf("expected TextDelta Hello, got %+v", deltas)
	}
}

func TestOpenAI_ParseStreamChunk_ReasoningContent(t *testing.T) {
	h := &OpenAIHandler{}
	raw := []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"Let me think..."},"finish_reason":null}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventThinkingDelta || deltas[0].Content != "Let me think..." {
		t.Fatalf("expected ThinkingDelta, got %+v", deltas)
	}
}

func TestOpenAI_ParseStreamChunk_IgnoresEmptyTextAndToolArgumentDeltas(t *testing.T) {
	h := &OpenAIHandler{}
	raw := []byte(`{"choices":[{"delta":{"content":"","tool_calls":[{"index":0,"id":"","type":"function","function":{"arguments":""}}]},"index":0}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatalf("ParseStreamChunk() error = %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("ParseStreamChunk() returned empty semantic deltas: %+v", deltas)
	}
}

func TestOpenAI_ParseStreamChunk_ToolCalls(t *testing.T) {
	h := &OpenAIHandler{}
	// First chunk: tool start with id and name.
	raw := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventToolStart {
		t.Fatalf("expected ToolStart, got %+v", deltas)
	}
	if deltas[0].ToolCallID != "call_123" || deltas[0].ToolName != "get_weather" {
		t.Fatalf("wrong tool info: %+v", deltas[0])
	}

	// Second chunk: tool args delta.
	raw2 := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`)
	deltas2, err := h.ParseStreamChunk(raw2)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas2) != 1 || deltas2[0].Type != EventToolDelta {
		t.Fatalf("expected ToolDelta, got %+v", deltas2)
	}
	if deltas2[0].ToolArgs != `{"city":` {
		t.Fatalf("wrong args: %q", deltas2[0].ToolArgs)
	}
}

func TestOpenAI_ParseStreamChunk_FinishReason(t *testing.T) {
	h := &OpenAIHandler{}
	raw := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range deltas {
		if d.Type == EventDone && d.FinishReason == "stop" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected EventDone with stop, got %+v", deltas)
	}
}

func TestOpenAI_ParseStreamChunk_Usage(t *testing.T) {
	h := &OpenAIHandler{}
	raw := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range deltas {
		if d.Type == EventUsage && d.Usage != nil && d.Usage.PromptTokens == 10 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected EventUsage with prompt_tokens=10, got %+v", deltas)
	}
}

// --- OpenAI Serializer Tests ---

func TestOpenAI_Serializer_Start(t *testing.T) {
	h := &OpenAIHandler{}
	ser := h.NewStreamSerializer("gpt-4")
	chunks := ser.Serialize(StreamDelta{Type: EventStart, ID: "test-id", Model: "gpt-4", Created: 1700000000})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), `"role":"assistant"`)
	assertContains(t, string(chunks[0]), `"id":"test-id"`)
}

func TestOpenAI_Serializer_TextDelta(t *testing.T) {
	h := &OpenAIHandler{}
	ser := h.NewStreamSerializer("gpt-4")
	chunks := ser.Serialize(StreamDelta{Type: EventTextDelta, Content: "Hello"})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), `"content":"Hello"`)
}

func TestOpenAI_Serializer_Flush(t *testing.T) {
	h := &OpenAIHandler{}
	ser := h.NewStreamSerializer("gpt-4")
	chunks := ser.Flush()
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), "[DONE]")
}
