package oagmsg

import (
	"testing"
)

// --- Interactions/Codex Parser Tests ---

func TestInteractions_ParseStreamChunk_ResponseCreated(t *testing.T) {
	h := &InteractionsHandler{}
	raw := []byte(`{"type":"response.created","response":{"id":"resp_123","model":"codex-mini","created_at":1700000000}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventStart {
		t.Fatalf("expected EventStart, got %+v", deltas)
	}
	if deltas[0].ID != "resp_123" || deltas[0].Model != "codex-mini" {
		t.Fatalf("wrong metadata: %+v", deltas[0])
	}
}

func TestInteractions_ParseStreamChunk_TextDelta(t *testing.T) {
	h := &InteractionsHandler{}
	raw := []byte(`{"type":"response.output_text.delta","delta":"Hello world"}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventTextDelta || deltas[0].Content != "Hello world" {
		t.Fatalf("expected TextDelta, got %+v", deltas)
	}
}

func TestInteractions_ParseStreamChunk_ReasoningDelta(t *testing.T) {
	h := &InteractionsHandler{}
	raw := []byte(`{"type":"response.reasoning_summary_text.delta","delta":"thinking..."}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventThinkingDelta || deltas[0].Content != "thinking..." {
		t.Fatalf("expected ThinkingDelta, got %+v", deltas)
	}
}

func TestInteractions_ParseStreamChunk_FunctionCallFlow(t *testing.T) {
	h := &InteractionsHandler{}

	// Tool start.
	raw := []byte(`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"search"}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventToolStart {
		t.Fatalf("expected ToolStart, got %+v", deltas)
	}
	if deltas[0].ToolCallID != "call_1" || deltas[0].ToolName != "search" || deltas[0].ToolType != "function" {
		t.Fatalf("wrong tool: %+v", deltas[0])
	}

	// Args delta.
	raw2 := []byte(`{"type":"response.function_call_arguments.delta","delta":"{\"q\":"}`)
	deltas2, err := h.ParseStreamChunk(raw2)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas2) != 1 || deltas2[0].Type != EventToolDelta {
		t.Fatalf("expected ToolDelta, got %+v", deltas2)
	}

	// Args done.
	raw3 := []byte(`{"type":"response.function_call_arguments.done","arguments":"{\"q\":\"test\"}"}`)
	deltas3, err := h.ParseStreamChunk(raw3)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas3) != 1 || deltas3[0].Type != EventToolDone {
		t.Fatalf("expected ToolDone, got %+v", deltas3)
	}
}

func TestInteractions_ParseStreamChunk_CustomToolCall(t *testing.T) {
	h := &InteractionsHandler{}
	raw := []byte(`{"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_2","name":"my_tool"}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].ToolType != "custom" {
		t.Fatalf("expected custom ToolType, got %+v", deltas)
	}
}

func TestInteractions_ParseStreamChunk_ImageDelta(t *testing.T) {
	h := &InteractionsHandler{}
	raw := []byte(`{"type":"response.image_generation_call.partial_image","partial_image_b64":"abc123","output_format":"png","item_id":"img_1"}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventImageDelta {
		t.Fatalf("expected ImageDelta, got %+v", deltas)
	}
	if deltas[0].ImageData != "abc123" || deltas[0].ImageFormat != "png" {
		t.Fatalf("wrong image: %+v", deltas[0])
	}
}

func TestInteractions_ParseStreamChunk_Completed(t *testing.T) {
	h := &InteractionsHandler{}
	raw := []byte(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	foundDone := false
	foundUsage := false
	for _, d := range deltas {
		if d.Type == EventDone {
			foundDone = true
		}
		if d.Type == EventUsage && d.Usage != nil && d.Usage.PromptTokens == 10 {
			foundUsage = true
		}
	}
	if !foundDone {
		t.Fatal("expected EventDone")
	}
	if !foundUsage {
		t.Fatal("expected EventUsage")
	}
}

func TestInteractions_ParseStreamChunk_Incomplete(t *testing.T) {
	h := &InteractionsHandler{}
	raw := []byte(`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_tokens"}}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventDone || deltas[0].FinishReason != "length" {
		t.Fatalf("expected EventDone with length, got %+v", deltas)
	}
}

// --- Interactions/Codex Serializer Tests ---

func TestInteractions_Serializer_TextFlow(t *testing.T) {
	h := &InteractionsHandler{}
	ser := h.NewStreamSerializer("codex-mini")

	// Start
	chunks := ser.Serialize(StreamDelta{Type: EventStart, ID: "resp_1", Model: "codex-mini", Created: 100})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 start chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), "response.created")
	assertContains(t, string(chunks[0]), "resp_1")

	// Text
	chunks = ser.Serialize(StreamDelta{Type: EventTextDelta, Content: "hi"})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 text chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), "response.output_text.delta")

	// Flush
	flushChunks := ser.Flush()
	if len(flushChunks) != 1 {
		t.Fatalf("expected 1 flush chunk, got %d", len(flushChunks))
	}
	assertContains(t, string(flushChunks[0]), "response.completed")
}
