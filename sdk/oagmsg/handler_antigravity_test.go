package oagmsg

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestAntigravityHandler_Format verifies the handler identifies as FormatAntigravity.
func TestAntigravityHandler_Format(t *testing.T) {
	h := &AntigravityHandler{}
	if got := h.Format(); got != FormatAntigravity {
		t.Fatalf("Format() = %q, want %q", got, FormatAntigravity)
	}
}

// TestAntigravityHandler_ParseRequest_Envelope verifies the Antigravity request envelope
// is correctly unwrapped: {"project":"", "request":{GEMINI}, "model":"MODEL"}.
func TestAntigravityHandler_ParseRequest_Envelope(t *testing.T) {
	input := `{
		"project": "my-project",
		"model": "gemini-2.5-pro",
		"request": {
			"contents": [{"role":"user","parts":[{"text":"hello"}]}],
			"generationConfig": {"temperature": 0.7, "maxOutputTokens": 100}
		}
	}`

	h := &AntigravityHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	// Envelope-level model takes priority.
	if req.Model != "gemini-2.5-pro" {
		t.Errorf("Model = %q, want %q", req.Model, "gemini-2.5-pro")
	}
	if req.SourceFormat != FormatAntigravity {
		t.Errorf("SourceFormat = %q, want %q", req.SourceFormat, FormatAntigravity)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].GetText() != "hello" {
		t.Errorf("Message text = %q, want %q", req.Messages[0].GetText(), "hello")
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", req.Temperature)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 100 {
		t.Errorf("MaxTokens = %v, want 100", req.MaxTokens)
	}
}

// TestAntigravityHandler_ParseRequest_BareGemini verifies fallback when no "request" wrapper exists.
func TestAntigravityHandler_ParseRequest_BareGemini(t *testing.T) {
	input := `{
		"model": "gemini-2.5-pro",
		"contents": [{"role":"user","parts":[{"text":"bare"}]}]
	}`

	h := &AntigravityHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}
	if len(req.Messages) != 1 || req.Messages[0].GetText() != "bare" {
		t.Errorf("Expected bare message 'bare', got %v", req.Messages)
	}
}

// TestAntigravityHandler_SerializeRequest_Envelope verifies the output has the
// Antigravity envelope and parametersJsonSchema rename.
func TestAntigravityHandler_SerializeRequest_Envelope(t *testing.T) {
	req := &UnifiedRequest{
		Model:    "gemini-2.5-pro",
		Messages: []OagMessage{UserTextMsg("hi")},
		Tools: []map[string]any{
			{
				"name":         "get_weather",
				"description":  "Gets weather",
				"input_schema": map[string]any{"type": "object"},
			},
		},
	}

	h := &AntigravityHandler{}
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	root := gjson.ParseBytes(out)

	// Verify envelope structure.
	if !root.Get("project").Exists() {
		t.Error("Missing 'project' in envelope")
	}
	if root.Get("model").String() != "gemini-2.5-pro" {
		t.Errorf("model = %q, want %q", root.Get("model").String(), "gemini-2.5-pro")
	}
	if !root.Get("request").IsObject() {
		t.Fatal("Missing 'request' object in envelope")
	}

	// Verify inner request has contents.
	inner := root.Get("request")
	if !inner.Get("contents").Exists() {
		t.Error("Missing contents in request body")
	}
}

func TestAntigravityHandler_OpenAIVideoURLToInlineData(t *testing.T) {
	input := []byte(`{
		"model": "gemini-3.7-flash-high",
		"messages": [{
			"role": "user",
			"content": [{
				"type": "video_url",
				"video_url": {"url": "data:video/mp4;base64,AAAAIGZ0eXBtcDQy"}
			}]
		}]
	}`)

	out := TranslateRequest(FormatOpenAI, FormatAntigravity, "gemini-3.7-flash-high", input, false)
	root := gjson.ParseBytes(out)
	part := root.Get("request.contents.0.parts.0.inlineData")
	if got := part.Get("mimeType").String(); got != "video/mp4" {
		t.Fatalf("mimeType = %q, want video/mp4; output=%s", got, out)
	}
	if got := part.Get("data").String(); got != "AAAAIGZ0eXBtcDQy" {
		t.Fatalf("data = %q, want video payload; output=%s", got, out)
	}
}

// TestAntigravityHandler_ParseRequest_ThoughtRoundTrip verifies thought/thoughtSignature
// fields survive parse→serialize round-trip via RawBlock passthrough.
func TestAntigravityHandler_ParseRequest_ThoughtRoundTrip(t *testing.T) {
	input := `{
		"project": "",
		"model": "gemini-2.5-pro",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{"text": "I am thinking...", "thought": true, "thoughtSignature": "sig123"},
						{"text": "The answer is 42"}
					]
				}
			]
		}
	}`

	h := &AntigravityHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if len(req.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1", len(req.Messages))
	}

	msg := req.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(msg.Content))
	}

	// First block should be a RawBlock (thought: true → passthrough).
	raw, ok := msg.Content[0].(RawBlock)
	if !ok {
		t.Fatalf("Content[0] type = %T, want RawBlock", msg.Content[0])
	}
	if raw.RawData["thought"] != true {
		t.Error("RawBlock should preserve thought=true")
	}
	if raw.RawData["thoughtSignature"] != "sig123" {
		t.Errorf("RawBlock thoughtSignature = %v, want sig123", raw.RawData["thoughtSignature"])
	}

	// Second block should be a TextBlock.
	tb, ok := msg.Content[1].(TextBlock)
	if !ok {
		t.Fatalf("Content[1] type = %T, want TextBlock", msg.Content[1])
	}
	if tb.Text != "The answer is 42" {
		t.Errorf("TextBlock text = %q, want %q", tb.Text, "The answer is 42")
	}
}

// TestAntigravityHandler_ParseResponse_Envelope verifies response envelope unwrapping.
func TestAntigravityHandler_ParseResponse_Envelope(t *testing.T) {
	input := `{
		"response": {
			"candidates": [{
				"content": {"parts": [{"text": "hello world"}], "role": "model"},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 10,
				"candidatesTokenCount": 5,
				"totalTokenCount": 15
			}
		}
	}`

	h := &AntigravityHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}
	if resp.Content != "hello world" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello world")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
	}
}

// TestAntigravityHandler_ParseResponse_CpaUsageMetadata verifies cpaUsageMetadata → usageMetadata restore.
func TestAntigravityHandler_ParseResponse_CpaUsageMetadata(t *testing.T) {
	// Executor renames usageMetadata → cpaUsageMetadata in non-terminal chunks.
	input := `{
		"candidates": [{
			"content": {"parts": [{"text": "ok"}], "role": "model"},
			"finishReason": "STOP"
		}],
		"cpaUsageMetadata": {
			"promptTokenCount": 20,
			"candidatesTokenCount": 8,
			"totalTokenCount": 28
		}
	}`

	h := &AntigravityHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("Usage is nil — cpaUsageMetadata was not restored")
	}
	if resp.Usage.PromptTokens != 20 {
		t.Errorf("PromptTokens = %d, want 20", resp.Usage.PromptTokens)
	}
}

// TestAntigravityHandler_ParseStreamChunk_ResponseEnvelope verifies stream chunk
// envelope unwrapping and cpaUsageMetadata restore.
func TestAntigravityHandler_ParseStreamChunk_ResponseEnvelope(t *testing.T) {
	// Stream chunk with response envelope and cpaUsageMetadata.
	chunk := `{
		"response": {
			"candidates": [{"content":{"parts":[{"text":"delta"}],"role":"model"}}],
			"cpaUsageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 2, "totalTokenCount": 7}
		}
	}`

	h := &AntigravityHandler{}
	deltas, err := h.ParseStreamChunk([]byte(chunk))
	if err != nil {
		t.Fatalf("ParseStreamChunk error: %v", err)
	}

	// Should produce at least a text delta.
	var foundText bool
	var foundUsage bool
	for _, d := range deltas {
		if d.Type == EventTextDelta && d.Content == "delta" {
			foundText = true
		}
		if d.Type == EventUsage && d.Usage != nil && d.Usage.PromptTokens == 5 {
			foundUsage = true
		}
	}
	if !foundText {
		t.Error("Expected EventTextDelta with content 'delta'")
	}
	if !foundUsage {
		t.Error("Expected EventUsage with PromptTokens=5 (from cpaUsageMetadata restore)")
	}
}

// TestAntigravityHandler_ParseStreamChunk_BareGemini verifies parsing without response envelope.
func TestAntigravityHandler_ParseStreamChunk_BareGemini(t *testing.T) {
	chunk := `{
		"candidates": [{"content":{"parts":[{"text":"bare chunk"}],"role":"model"}}]
	}`

	h := &AntigravityHandler{}
	deltas, err := h.ParseStreamChunk([]byte(chunk))
	if err != nil {
		t.Fatalf("ParseStreamChunk error: %v", err)
	}

	var foundText bool
	for _, d := range deltas {
		if d.Type == EventTextDelta && d.Content == "bare chunk" {
			foundText = true
		}
	}
	if !foundText {
		t.Error("Expected EventTextDelta with content 'bare chunk'")
	}
}

// TestAntigravityHandler_ParametersJsonSchema verifies parameters → parametersJsonSchema rename.
func TestAntigravityHandler_ParametersJsonSchema(t *testing.T) {
	geminiBody := []byte(`{
		"tools": [{
			"functionDeclarations": [{
				"name": "get_weather",
				"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
			}]
		}]
	}`)

	result := renameToolParameters(geminiBody)
	root := gjson.ParseBytes(result)

	// "parameters" should be gone.
	if root.Get("tools.0.functionDeclarations.0.parameters").Exists() {
		t.Error("'parameters' should be renamed, but still exists")
	}
	// "parametersJsonSchema" should exist with the same content.
	pjs := root.Get("tools.0.functionDeclarations.0.parametersJsonSchema")
	if !pjs.Exists() {
		t.Fatal("'parametersJsonSchema' not found")
	}
	if pjs.Get("type").String() != "object" {
		t.Errorf("parametersJsonSchema.type = %q, want 'object'", pjs.Get("type").String())
	}
}

// TestAntigravityHandler_ToolNormalization_NoOpPreservesLargeInlineData verifies that
// no-op tool normalization keeps payload bytes unchanged for large inlineData content.
func TestAntigravityHandler_ToolNormalization_NoOpPreservesLargeInlineData(t *testing.T) {
	largeData := strings.Repeat("A", 128*1024)
	input := []byte(`{
		"contents":[
			{"role":"user","parts":[{"text":"hello"},{"inlineData":{"mimeType":"image/png","data":"` + largeData + `"}}]
		},
		{"role":"model","parts":[{"text":"ok"}]}
		],
		"tools":[{"type":"function","name":"noop","parameters":{"type":"object"}}],
		"systemInstruction":{"role":"user","parts":[{"text":"keep"}]}
	}`)

	out := normalizeAntigravityToolRequestBody(input)
	if string(out) != string(input) {
		t.Fatalf("large inlineData payload changed:\nwant %d bytes\ngot  %d bytes", len(input), len(out))
	}
}

// TestAntigravityHandler_FunctionResponseNormalization_NoopPreservesLargeInlineDataWithoutFunctionResponse ensures
// function response normalization does not modify payloads without functionResponse parts.
func TestAntigravityHandler_FunctionResponseNormalization_NoopPreservesLargeInlineDataWithoutFunctionResponse(t *testing.T) {
	largeData := strings.Repeat("B", 128*1024)
	input := []byte(`{
		"contents":[
			{"role":"user","parts":[{"text":"img"},{"inlineData":{"mimeType":"image/jpeg","data":"` + largeData + `"}}]}
		]
	}`)

	out := normalizeAntigravityFunctionResponseResults(input)
	if string(out) != string(input) {
		t.Fatalf("large inlineData function-response payload changed:\nwant %d bytes\ngot  %d bytes", len(input), len(out))
	}
}

// TestAntigravityHandler_RestoreCpaUsageMetadata verifies the field rename.
func TestAntigravityHandler_RestoreCpaUsageMetadata(t *testing.T) {
	data := []byte(`{"candidates":[],"cpaUsageMetadata":{"promptTokenCount":10}}`)
	result := restoreCpaUsageMetadata(data)

	root := gjson.ParseBytes(result)
	if root.Get("cpaUsageMetadata").Exists() {
		t.Error("cpaUsageMetadata should be removed")
	}
	if !root.Get("usageMetadata").Exists() {
		t.Error("usageMetadata should be present")
	}
	if root.Get("usageMetadata.promptTokenCount").Int() != 10 {
		t.Errorf("promptTokenCount = %d, want 10", root.Get("usageMetadata.promptTokenCount").Int())
	}
}

// TestAntigravityHandler_DefaultRegistryLookup verifies the handler is registered and retrievable.
func TestAntigravityHandler_DefaultRegistryLookup(t *testing.T) {
	r := DefaultRegistry()
	h, ok := r.Get(FormatAntigravity)
	if !ok {
		t.Fatal("AntigravityHandler not found in DefaultRegistry")
	}
	if h.Format() != FormatAntigravity {
		t.Errorf("Format() = %q, want %q", h.Format(), FormatAntigravity)
	}
}
