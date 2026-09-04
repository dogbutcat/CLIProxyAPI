package oagmsg

import (
	"testing"
)

// --- Gemini Parser Tests ---

func TestGemini_ParseStreamChunk_TextDelta(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventTextDelta || deltas[0].Content != "Hello" {
		t.Fatalf("expected TextDelta Hello, got %+v", deltas)
	}
}

func TestGemini_ParseStreamChunk_ThinkingDelta(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"thinking...","thought":true}],"role":"model"}}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventThinkingDelta || deltas[0].Content != "thinking..." {
		t.Fatalf("expected ThinkingDelta, got %+v", deltas)
	}
}

func TestGemini_ParseStreamChunk_FunctionCall(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search","args":{"q":"test"}}}],"role":"model"}}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Should produce EventToolStart + EventToolDone (Gemini sends complete calls).
	if len(deltas) < 2 {
		t.Fatalf("expected at least 2 deltas, got %d", len(deltas))
	}
	if deltas[0].Type != EventToolStart || deltas[0].ToolName != "search" {
		t.Fatalf("expected ToolStart search, got %+v", deltas[0])
	}
	if deltas[1].Type != EventToolDone {
		t.Fatalf("expected ToolDone, got %+v", deltas[1])
	}
	if deltas[1].ToolArgs == "" {
		t.Fatal("expected non-empty ToolArgs")
	}
}

func TestGemini_ParseStreamChunk_FinishReason(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"candidates":[{"finishReason":"STOP"}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventDone || deltas[0].FinishReason != "stop" {
		t.Fatalf("expected EventDone stop, got %+v", deltas)
	}
	if deltas[0].NativeFinishReason != "stop" {
		t.Fatalf("expected native_finish_reason=stop, got %q", deltas[0].NativeFinishReason)
	}
}

func TestGemini_ParseStreamChunk_MaxTokens(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"candidates":[{"finishReason":"MAX_TOKENS"}]}`)
	deltas, _ := h.ParseStreamChunk(raw)
	if len(deltas) != 1 || deltas[0].FinishReason != "length" {
		t.Fatalf("expected length, got %+v", deltas)
	}
}

func TestGemini_ParseStreamChunk_Usage(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10,"totalTokenCount":15,"thoughtsTokenCount":3}}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Type != EventUsage {
		t.Fatalf("expected EventUsage, got %+v", deltas)
	}
	if deltas[0].Usage.PromptTokens != 5 || deltas[0].Usage.CompletionTokens != 10 {
		t.Fatalf("wrong usage: %+v", deltas[0].Usage)
	}
	if deltas[0].Usage.ReasoningTokens != 3 {
		t.Fatalf("expected reasoning_tokens=3, got %d", deltas[0].Usage.ReasoningTokens)
	}
}

func TestGemini_ParseStreamChunk_Metadata(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"responseId":"resp-123","modelVersion":"gemini-2.0","createTime":"2025-01-01T00:00:00Z","candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"}}]}`)
	deltas, err := h.ParseStreamChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	// First delta should be EventStart with metadata.
	if len(deltas) < 1 || deltas[0].Type != EventStart {
		t.Fatalf("expected EventStart first, got %+v", deltas)
	}
	if deltas[0].ID != "resp-123" {
		t.Fatalf("expected ID=resp-123, got %q", deltas[0].ID)
	}
	if deltas[0].Model != "gemini-2.0" {
		t.Fatalf("expected Model=gemini-2.0, got %q", deltas[0].Model)
	}
	if deltas[0].Created == 0 {
		t.Fatal("expected non-zero Created timestamp")
	}
	// Should also have TextDelta.
	found := false
	for _, d := range deltas {
		if d.Type == EventTextDelta && d.Content == "Hi" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected TextDelta with Hi")
	}
}

func TestGemini_ParseStreamChunk_ThoughtSignatureSkip(t *testing.T) {
	h := &GeminiHandler{}
	raw := []byte(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig123"}],"role":"model"}}]}`)
	deltas, _ := h.ParseStreamChunk(raw)
	if len(deltas) != 1 || deltas[0].Type != EventThinkingDelta || deltas[0].Content != "" || deltas[0].Signature != "sig123" {
		t.Fatalf("expected signature-only thinking delta, got %+v", deltas)
	}
}

// --- Gemini Serializer Tests ---

func TestGemini_Serializer_TextDelta(t *testing.T) {
	h := &GeminiHandler{}
	ser := h.NewStreamSerializer("gemini-pro")
	chunks := ser.Serialize(StreamDelta{Type: EventTextDelta, Content: "Hello"})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), `"text":"Hello"`)
}

func TestGemini_Serializer_ThinkingDelta(t *testing.T) {
	h := &GeminiHandler{}
	ser := h.NewStreamSerializer("gemini-pro")
	chunks := ser.Serialize(StreamDelta{Type: EventThinkingDelta, Content: "hmm"})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), `"thought":true`)
}

func TestGemini_Serializer_ToolDone(t *testing.T) {
	h := &GeminiHandler{}
	ser := h.NewStreamSerializer("gemini-pro")
	chunks := ser.Serialize(StreamDelta{Type: EventToolDone, ToolName: "search", ToolArgs: `{"q":"test"}`})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	assertContains(t, string(chunks[0]), "functionCall")
	assertContains(t, string(chunks[0]), "search")
}

func TestGemini_Serializer_Flush(t *testing.T) {
	h := &GeminiHandler{}
	ser := h.NewStreamSerializer("gemini-pro")
	chunks := ser.Flush()
	if len(chunks) != 0 {
		t.Fatalf("Gemini Flush should return nil, got %d", len(chunks))
	}
}
