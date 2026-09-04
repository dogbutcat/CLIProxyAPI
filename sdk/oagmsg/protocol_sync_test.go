package oagmsg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// ----------------------------------------------------------------
// ThinkingBlock type tests
// ----------------------------------------------------------------

func TestThinkingBlockType(t *testing.T) {
	b := ThinkingBlock{Thinking: "I'm thinking"}
	if b.blockType() != "thinking" {
		t.Errorf("expected blockType() = %q, got %q", "thinking", b.blockType())
	}
}

func TestThinkingBlockInterface(t *testing.T) {
	var _ ContentBlock = ThinkingBlock{}
}

// ----------------------------------------------------------------
// AudioBlock type tests
// ----------------------------------------------------------------

func TestAudioBlockType(t *testing.T) {
	b := AudioBlock{Data: "base64data", Format: "wav"}
	if b.blockType() != "audio" {
		t.Errorf("expected blockType() = %q, got %q", "audio", b.blockType())
	}
}

func TestAudioBlockInterface(t *testing.T) {
	var _ ContentBlock = AudioBlock{}
}

// ----------------------------------------------------------------
// Tool Call ID Sanitization
// ----------------------------------------------------------------

func TestSanitizeClaudeToolID_Valid(t *testing.T) {
	// Already valid IDs should pass through unchanged
	tests := []string{
		"toolu_abc123",
		"call_1",
		"my-tool-id",
		"abc_def-ghi",
	}
	for _, id := range tests {
		result := sanitizeClaudeToolID(id)
		if result != id {
			t.Errorf("sanitizeClaudeToolID(%q) = %q, want unchanged", id, result)
		}
	}
}

func TestSanitizeClaudeToolID_Invalid(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"call.with space:1", "call_with_space_1"},
		{"id@#$%", "id____"},
		{"a b", "a_b"},
	}
	for _, tc := range tests {
		result := sanitizeClaudeToolID(tc.input)
		if result != tc.expected {
			t.Errorf("sanitizeClaudeToolID(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestSanitizeClaudeToolID_Empty(t *testing.T) {
	result := sanitizeClaudeToolID("")
	if result == "" {
		t.Error("sanitizeClaudeToolID(\"\") should generate fallback, got empty")
	}
	if !strings.HasPrefix(result, "toolu_") {
		t.Errorf("sanitizeClaudeToolID(\"\") = %q, should start with toolu_", result)
	}
}

// ----------------------------------------------------------------
// Anthropic: ThinkingBlock parse + serialize roundtrip
// ----------------------------------------------------------------

func TestAnthropicThinkingBlockParse(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [{
			"role": "assistant",
			"content": [
				{"type": "thinking", "thinking": "Let me reason...", "signature": "sig_abc"},
				{"type": "text", "text": "The answer is 42"}
			]
		}],
		"max_tokens": 1024
	}`

	h := &AnthropicHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	// Should have system(implicit) + assistant = messages
	assistantMsg := req.Messages[0]
	if len(assistantMsg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(assistantMsg.Content))
	}

	// First block should be ThinkingBlock
	tb, ok := assistantMsg.Content[0].(ThinkingBlock)
	if !ok {
		t.Fatalf("expected ThinkingBlock, got %T", assistantMsg.Content[0])
	}
	if tb.Thinking != "Let me reason..." {
		t.Errorf("thinking text = %q, want %q", tb.Thinking, "Let me reason...")
	}
	if tb.Signature != "sig_abc" {
		t.Errorf("signature = %q, want %q", tb.Signature, "sig_abc")
	}
	if tb.Redacted {
		t.Error("should not be redacted")
	}
}

func TestAnthropicRedactedThinkingParse(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [{
			"role": "assistant",
			"content": [
				{"type": "redacted_thinking", "data": "encrypted_blob"},
				{"type": "text", "text": "Hello"}
			]
		}],
		"max_tokens": 1024
	}`

	h := &AnthropicHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	tb, ok := req.Messages[0].Content[0].(ThinkingBlock)
	if !ok {
		t.Fatalf("expected ThinkingBlock, got %T", req.Messages[0].Content[0])
	}
	if !tb.Redacted {
		t.Error("should be redacted")
	}
}

func TestAnthropicThinkingBlockSerialize(t *testing.T) {
	msg := OagMessage{
		Role: "assistant",
		Content: []ContentBlock{
			ThinkingBlock{Thinking: "Deep thought", Signature: "sig_xyz"},
			TextBlock{Text: "Result"},
		},
	}

	h := &AnthropicHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	blocks := root.Get("0.content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(blocks))
	}

	// First should be thinking
	if blocks[0].Get("type").String() != "thinking" {
		t.Errorf("first block type = %q, want %q", blocks[0].Get("type").String(), "thinking")
	}
	if blocks[0].Get("thinking").String() != "Deep thought" {
		t.Errorf("thinking text = %q", blocks[0].Get("thinking").String())
	}
	if blocks[0].Get("signature").String() != "sig_xyz" {
		t.Errorf("signature = %q", blocks[0].Get("signature").String())
	}
}

func TestAnthropicRedactedThinkingSerialize(t *testing.T) {
	msg := OagMessage{
		Role: "assistant",
		Content: []ContentBlock{
			ThinkingBlock{Redacted: true},
			TextBlock{Text: "Hello"},
		},
	}

	h := &AnthropicHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	firstBlock := root.Get("0.content.0")
	if firstBlock.Get("type").String() != "redacted_thinking" {
		t.Errorf("type = %q, want %q", firstBlock.Get("type").String(), "redacted_thinking")
	}
}

// ----------------------------------------------------------------
// Anthropic: Tool ID sanitization in serialize
// ----------------------------------------------------------------

func TestAnthropicToolIDSanitization(t *testing.T) {
	msg := OagMessage{
		Role: "assistant",
		Content: []ContentBlock{
			ToolUseBlock{ID: "call.with space:1", Name: "read_file", Input: map[string]any{"path": "/tmp"}},
		},
	}

	h := &AnthropicHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	id := root.Get("0.content.0.id").String()
	if id != "call_with_space_1" {
		t.Errorf("tool_use id = %q, want %q", id, "call_with_space_1")
	}
}

func TestAnthropicToolResultIDSanitization(t *testing.T) {
	msg := OagMessage{
		Role: "user",
		Content: []ContentBlock{
			ToolResultBlock{ToolUseID: "call.with space:1", Content: "ok"},
		},
	}

	h := &AnthropicHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	id := root.Get("0.content.0.tool_use_id").String()
	if id != "call_with_space_1" {
		t.Errorf("tool_use_id = %q, want %q", id, "call_with_space_1")
	}
}

// ----------------------------------------------------------------
// Anthropic: max_tokens default + stream removal
// ----------------------------------------------------------------

func TestAnthropicMaxTokensDefault(t *testing.T) {
	req := &UnifiedRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []OagMessage{{Role: "user", Content: []ContentBlock{TextBlock{Text: "Hi"}}}},
		// MaxTokens is nil
	}

	h := &AnthropicHandler{}
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	maxTokens := gjson.GetBytes(out, "max_tokens").Int()
	if maxTokens != 32000 {
		t.Errorf("max_tokens = %d, want 32000", maxTokens)
	}
}

func TestAnthropicMaxTokensExplicit(t *testing.T) {
	maxTok := 4096
	req := &UnifiedRequest{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []OagMessage{{Role: "user", Content: []ContentBlock{TextBlock{Text: "Hi"}}}},
		MaxTokens: &maxTok,
	}

	h := &AnthropicHandler{}
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	maxTokens := gjson.GetBytes(out, "max_tokens").Int()
	if maxTokens != 4096 {
		t.Errorf("max_tokens = %d, want 4096", maxTokens)
	}
}

func TestAnthropicNoStreamInOutput(t *testing.T) {
	req := &UnifiedRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []OagMessage{{Role: "user", Content: []ContentBlock{TextBlock{Text: "Hi"}}}},
		Stream:   true,
	}

	h := &AnthropicHandler{}
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	if gjson.GetBytes(out, "stream").Exists() {
		t.Error("stream field should not be present in serialized Anthropic request")
	}
}

// ----------------------------------------------------------------
// OpenAI: AudioBlock parse
// ----------------------------------------------------------------

func TestOpenAIAudioBlockParse(t *testing.T) {
	input := `{
		"model": "gpt-4o-audio-preview",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "input_audio", "input_audio": {"data": "base64audio", "format": "wav"}}
			]
		}]
	}`

	h := &OpenAIHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	ab, ok := req.Messages[0].Content[0].(AudioBlock)
	if !ok {
		t.Fatalf("expected AudioBlock, got %T", req.Messages[0].Content[0])
	}
	if ab.Data != "base64audio" {
		t.Errorf("audio data = %q, want %q", ab.Data, "base64audio")
	}
	if ab.Format != "wav" {
		t.Errorf("audio format = %q, want %q", ab.Format, "wav")
	}
}

func TestOpenAIAudioBlockSerialize(t *testing.T) {
	msg := OagMessage{
		Role: "user",
		Content: []ContentBlock{
			AudioBlock{Data: "base64audio", Format: "mp3"},
		},
	}

	h := &OpenAIHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	block := root.Get("0.content.0")
	if block.Get("type").String() != "input_audio" {
		t.Errorf("type = %q, want %q", block.Get("type").String(), "input_audio")
	}
	if block.Get("input_audio.data").String() != "base64audio" {
		t.Errorf("data = %q", block.Get("input_audio.data").String())
	}
	if block.Get("input_audio.format").String() != "mp3" {
		t.Errorf("format = %q", block.Get("input_audio.format").String())
	}
}

func TestOpenAIThinkingBlockSkipped(t *testing.T) {
	msg := OagMessage{
		Role: "assistant",
		Content: []ContentBlock{
			ThinkingBlock{Thinking: "reasoning stuff"},
			TextBlock{Text: "answer"},
		},
	}

	h := &OpenAIHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	// ThinkingBlock should be skipped; only text block
	content := root.Get("0.content")
	if content.IsArray() {
		for _, item := range content.Array() {
			if item.Get("type").String() == "thinking" {
				t.Error("ThinkingBlock should be skipped in OpenAI serialization")
			}
		}
	}
}

// ----------------------------------------------------------------
// Interactions: ThinkingBlock, AudioBlock, custom tools, response_format
// ----------------------------------------------------------------

func TestInteractionsThinkingBlockSerialize(t *testing.T) {
	msg := OagMessage{
		Role: "assistant",
		Content: []ContentBlock{
			ThinkingBlock{Thinking: "Let me think..."},
		},
	}

	h := &InteractionsHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	item := root.Get("0")
	if item.Get("type").String() != "reasoning" {
		t.Errorf("type = %q, want %q", item.Get("type").String(), "reasoning")
	}
	if item.Get("content.0.type").String() != "summary_text" {
		t.Errorf("content[0].type = %q, want %q", item.Get("content.0.type").String(), "summary_text")
	}
	if item.Get("content.0.text").String() != "Let me think..." {
		t.Errorf("content[0].text = %q", item.Get("content.0.text").String())
	}
}

func TestInteractionsRedactedThinkingSkipped(t *testing.T) {
	msg := OagMessage{
		Role: "assistant",
		Content: []ContentBlock{
			ThinkingBlock{Redacted: true},
			TextBlock{Text: "answer"},
		},
	}

	h := &InteractionsHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	// Redacted thinking should be skipped; only the text message
	for _, item := range root.Array() {
		if item.Get("type").String() == "reasoning" {
			t.Error("redacted thinking should be skipped in Interactions serialization")
		}
	}
}

func TestInteractionsReasoningParse(t *testing.T) {
	input := `{
		"model": "codex-mini",
		"input": [
			{"type": "reasoning", "content": [{"type": "summary_text", "text": "I reasoned about it"}]}
		]
	}`

	h := &InteractionsHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	tb, ok := req.Messages[0].Content[0].(ThinkingBlock)
	if !ok {
		t.Fatalf("expected ThinkingBlock, got %T", req.Messages[0].Content[0])
	}
	if tb.Thinking != "I reasoned about it" {
		t.Errorf("thinking = %q, want %q", tb.Thinking, "I reasoned about it")
	}
}

func TestInteractionsAudioBlockSerialize(t *testing.T) {
	msg := OagMessage{
		Role: "user",
		Content: []ContentBlock{
			AudioBlock{Data: "base64audio", Format: "wav"},
		},
	}

	h := &InteractionsHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	item := root.Get("0")
	if item.Get("type").String() != "message" {
		t.Errorf("type = %q, want %q", item.Get("type").String(), "message")
	}
	content := item.Get("content.0")
	if content.Get("type").String() != "input_audio" {
		t.Errorf("content type = %q, want %q", content.Get("type").String(), "input_audio")
	}
	if content.Get("data").String() != "base64audio" {
		t.Errorf("data = %q", content.Get("data").String())
	}
}

func TestInteractionsCustomToolCallParse(t *testing.T) {
	input := `{
		"model": "codex-mini",
		"input": [
			{"type": "custom_tool_call", "call_id": "ct_1", "name": "bash", "input": "{\"cmd\":\"ls\"}"},
			{"type": "custom_tool_call_output", "call_id": "ct_1", "output": "file.txt"}
		]
	}`

	h := &InteractionsHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}

	// custom_tool_call → CustomToolUseBlock
	tu, ok := req.Messages[0].Content[0].(CustomToolUseBlock)
	if !ok {
		t.Fatalf("expected CustomToolUseBlock, got %T", req.Messages[0].Content[0])
	}
	if tu.ID != "ct_1" {
		t.Errorf("tool use ID = %q, want %q", tu.ID, "ct_1")
	}
	if tu.Name != "bash" {
		t.Errorf("tool name = %q, want %q", tu.Name, "bash")
	}

	if tu.Input != "{\"cmd\":\"ls\"}" {
		t.Errorf("tool input = %q, want raw JSON string", tu.Input)
	}

	// custom_tool_call_output → CustomToolResultBlock
	tr, ok := req.Messages[1].Content[0].(CustomToolResultBlock)
	if !ok {
		t.Fatalf("expected CustomToolResultBlock, got %T", req.Messages[1].Content[0])
	}
	if tr.ToolUseID != "ct_1" {
		t.Errorf("tool result ID = %q, want %q", tr.ToolUseID, "ct_1")
	}
	if tr.Output != "file.txt" {
		t.Errorf("tool result output = %q, want file.txt", tr.Output)
	}
}

func TestInteractionsResponseFormatMapping(t *testing.T) {
	req := &UnifiedRequest{
		Model:    "codex-mini",
		Messages: []OagMessage{{Role: "user", Content: []ContentBlock{TextBlock{Text: "Hi"}}}},
		ResponseFormat: map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "person",
				"strict": true,
				"schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"name": map[string]any{"type": "string"}},
				},
			},
		},
	}

	h := &InteractionsHandler{}
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	root := gjson.ParseBytes(out)
	textFormat := root.Get("text.format")
	if !textFormat.Exists() {
		t.Fatal("text.format should be present")
	}
	if textFormat.Get("type").String() != "json_schema" {
		t.Errorf("text.format.type = %q, want %q", textFormat.Get("type").String(), "json_schema")
	}
	if textFormat.Get("name").String() != "person" {
		t.Errorf("text.format.name = %q", textFormat.Get("name").String())
	}
	if !textFormat.Get("strict").Bool() {
		t.Error("text.format.strict should be true")
	}
	if !textFormat.Get("schema").Exists() {
		t.Error("text.format.schema should exist")
	}
}

func TestInteractionsResponseFormatText(t *testing.T) {
	req := &UnifiedRequest{
		Model:          "codex-mini",
		Messages:       []OagMessage{{Role: "user", Content: []ContentBlock{TextBlock{Text: "Hi"}}}},
		ResponseFormat: map[string]any{"type": "text"},
	}

	h := &InteractionsHandler{}
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	root := gjson.ParseBytes(out)
	if root.Get("text.format.type").String() != "text" {
		t.Errorf("text.format.type = %q, want %q", root.Get("text.format.type").String(), "text")
	}
}

func TestInteractionsAudioBlockParse(t *testing.T) {
	input := `{
		"model": "codex-mini",
		"input": [
			{"type": "message", "role": "user", "content": [
				{"type": "input_audio", "data": "base64wav", "format": "wav"}
			]}
		]
	}`

	h := &InteractionsHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	ab, ok := req.Messages[0].Content[0].(AudioBlock)
	if !ok {
		t.Fatalf("expected AudioBlock, got %T", req.Messages[0].Content[0])
	}
	if ab.Data != "base64wav" {
		t.Errorf("data = %q, want %q", ab.Data, "base64wav")
	}
	if ab.Format != "wav" {
		t.Errorf("format = %q, want %q", ab.Format, "wav")
	}
}

// ----------------------------------------------------------------
// Gemini: ThinkingBlock + AudioBlock serialize
// ----------------------------------------------------------------

func TestGeminiThinkingBlockSerialize(t *testing.T) {
	msg := OagMessage{
		Role: "model",
		Content: []ContentBlock{
			ThinkingBlock{Thinking: "Reasoning..."},
			TextBlock{Text: "Answer"},
		},
	}

	h := &GeminiHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	parts := root.Get("0.parts").Array()
	if len(parts) < 2 {
		t.Fatalf("expected >=2 parts, got %d", len(parts))
	}

	// First part: thought=true
	if !parts[0].Get("thought").Bool() {
		t.Error("first part should have thought=true")
	}
	if parts[0].Get("text").String() != "Reasoning..." {
		t.Errorf("thinking text = %q", parts[0].Get("text").String())
	}
}

func TestGeminiAudioBlockSerialize(t *testing.T) {
	msg := OagMessage{
		Role: "user",
		Content: []ContentBlock{
			AudioBlock{Data: "base64audio", Format: "mp3"},
		},
	}

	h := &GeminiHandler{}
	out, err := h.SerializeMessages([]OagMessage{msg})
	if err != nil {
		t.Fatalf("SerializeMessages error: %v", err)
	}

	root := gjson.ParseBytes(out)
	part := root.Get("0.parts.0")
	if part.Get("inlineData.mimeType").String() != "audio/mp3" {
		t.Errorf("mimeType = %q, want %q", part.Get("inlineData.mimeType").String(), "audio/mp3")
	}
	if part.Get("inlineData.data").String() != "base64audio" {
		t.Errorf("data = %q", part.Get("inlineData.data").String())
	}
}

// ----------------------------------------------------------------
// Cross-protocol roundtrip: ThinkingBlock
// ----------------------------------------------------------------

func TestThinkingBlockAnthropicRoundtrip(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [{
			"role": "assistant",
			"content": [
				{"type": "thinking", "thinking": "Deep reasoning", "signature": "sig_123"},
				{"type": "text", "text": "The answer"}
			]
		}],
		"max_tokens": 1024
	}`

	h := &AnthropicHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	// Serialize back to Anthropic — should preserve
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	root := gjson.ParseBytes(out)
	blocks := root.Get("messages.0.content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Get("type").String() != "thinking" {
		t.Error("first block should be thinking")
	}
	if blocks[0].Get("signature").String() != "sig_123" {
		t.Errorf("signature = %q, want %q", blocks[0].Get("signature").String(), "sig_123")
	}
}

func TestAudioBlockOpenAIRoundtrip(t *testing.T) {
	input := `{
		"model": "gpt-4o",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "input_audio", "input_audio": {"data": "base64wav", "format": "wav"}}
			]
		}]
	}`

	h := &OpenAIHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	// Serialize back
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	root := gjson.ParseBytes(out)
	block := root.Get("messages.0.content.0")
	if block.Get("type").String() != "input_audio" {
		t.Errorf("type = %q, want %q", block.Get("type").String(), "input_audio")
	}
	if block.Get("input_audio.data").String() != "base64wav" {
		t.Errorf("data = %q", block.Get("input_audio.data").String())
	}
}

// ----------------------------------------------------------------
// Extended ContentBlockInterface test (updated with new types)
// ----------------------------------------------------------------

func TestContentBlockInterfaceExtended(t *testing.T) {
	blocks := []ContentBlock{
		TextBlock{Text: "hello"},
		ImageBlock{MediaType: "image/png", Data: "data"},
		FileBlock{Filename: "f.pdf"},
		ToolUseBlock{ID: "1", Name: "test"},
		ToolResultBlock{ToolUseID: "1"},
		ThinkingBlock{Thinking: "think"},
		AudioBlock{Data: "audio", Format: "wav"},
		RawBlock{RawData: map[string]any{"x": 1}},
	}
	expected := []string{"text", "image", "file", "tool_use", "tool_result", "thinking", "audio", "raw"}

	for i, block := range blocks {
		if block.blockType() != expected[i] {
			t.Errorf("block %d: blockType() = %q, want %q", i, block.blockType(), expected[i])
		}
	}

	// Verify JSON serializable
	for i, block := range blocks {
		_, err := json.Marshal(block)
		if err != nil {
			t.Errorf("block %d (%T): json.Marshal error: %v", i, block, err)
		}
	}
}

// ================================================================
// Response Path Tests
// ================================================================

// ----------------------------------------------------------------
// Interactions ParseResponse: custom_tool_call
// ----------------------------------------------------------------

func TestInteractionsResponseCustomToolCall(t *testing.T) {
	input := `{
		"id": "resp_1",
		"model": "codex-mini",
		"status": "completed",
		"output": [
			{"type": "custom_tool_call", "call_id": "ct_1", "name": "bash", "input": "{\"cmd\":\"ls\"}"}
		]
	}`

	h := &InteractionsHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc["type"] != "custom_tool_call" {
		t.Errorf("tool call type = %v, want %q", tc["type"], "custom_tool_call")
	}
	if tc["call_id"] != "ct_1" {
		t.Errorf("call_id = %v, want %q", tc["call_id"], "ct_1")
	}
}

// ----------------------------------------------------------------
// Interactions ParseResponse: image_generation_call
// ----------------------------------------------------------------

func TestInteractionsResponseImageGeneration(t *testing.T) {
	input := `{
		"id": "resp_1",
		"model": "gpt-image-1",
		"status": "completed",
		"output": [
			{"type": "image_generation_call", "result": "base64imagedata", "output_format": "png"}
		]
	}`

	h := &InteractionsHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if !strings.Contains(resp.Content, "data:image/png;base64,base64imagedata") {
		t.Errorf("content should contain base64 image URL, got: %s", resp.Content)
	}
}

func TestInteractionsResponseImageGenerationJPG(t *testing.T) {
	input := `{
		"id": "resp_1",
		"model": "gpt-image-1",
		"status": "completed",
		"output": [
			{"type": "image_generation_call", "result": "jpgdata", "output_format": "jpg"}
		]
	}`

	h := &InteractionsHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if !strings.Contains(resp.Content, "data:image/jpeg;base64,jpgdata") {
		t.Errorf("jpg should map to image/jpeg, got: %s", resp.Content)
	}
}

// ----------------------------------------------------------------
// Interactions ParseResponse: incomplete status
// ----------------------------------------------------------------

func TestInteractionsResponseIncompleteMaxTokens(t *testing.T) {
	input := `{
		"id": "resp_1",
		"model": "codex-mini",
		"status": "incomplete",
		"incomplete_details": {"reason": "max_tokens"},
		"output": [{"type": "message", "content": [{"type": "output_text", "text": "partial"}]}]
	}`

	h := &InteractionsHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if resp.FinishReason != "length" {
		t.Errorf("finish_reason = %q, want %q", resp.FinishReason, "length")
	}
	if resp.Content != "partial" {
		t.Errorf("content = %q, want %q", resp.Content, "partial")
	}
}

func TestInteractionsResponseIncompleteContentFilter(t *testing.T) {
	input := `{
		"id": "resp_1",
		"model": "codex-mini",
		"status": "incomplete",
		"incomplete_details": {"reason": "content_filter"},
		"output": []
	}`

	h := &InteractionsHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if resp.FinishReason != "content_filter" {
		t.Errorf("finish_reason = %q, want %q", resp.FinishReason, "content_filter")
	}
}

func TestInteractionsResponseFailed(t *testing.T) {
	input := `{
		"id": "resp_1",
		"model": "codex-mini",
		"status": "failed",
		"output": []
	}`

	h := &InteractionsHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q for failed", resp.FinishReason, "stop")
	}
}

// ----------------------------------------------------------------
// Anthropic ParseResponse: redacted_thinking
// ----------------------------------------------------------------

func TestAnthropicResponseRedactedThinking(t *testing.T) {
	input := `{
		"id": "msg_1",
		"model": "claude-sonnet-4-20250514",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "redacted_thinking", "data": "encrypted_opaque_blob"},
			{"type": "thinking", "thinking": "visible reasoning", "signature": "sig_abc"},
			{"type": "text", "text": "The answer is 42"}
		],
		"stop_reason": "end_turn"
	}`

	h := &AnthropicHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if resp.Content != "The answer is 42" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.ThinkingContent != "visible reasoning" {
		t.Errorf("thinking = %q, want %q", resp.ThinkingContent, "visible reasoning")
	}
	// When both redacted_thinking and thinking exist, the later thinking block
	// overwrites ThinkingSignature. Test that at least one gets preserved.
	if resp.ThinkingSignature == "" {
		t.Error("ThinkingSignature should not be empty")
	}
}

func TestAnthropicResponseOnlyRedactedThinking(t *testing.T) {
	input := `{
		"id": "msg_1",
		"model": "claude-sonnet-4-20250514",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "redacted_thinking", "data": "encrypted_opaque_blob"},
			{"type": "text", "text": "Hello"}
		],
		"stop_reason": "end_turn"
	}`

	h := &AnthropicHandler{}
	resp, err := h.ParseResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseResponse error: %v", err)
	}

	if resp.ThinkingSignature != "encrypted_opaque_blob" {
		t.Errorf("ThinkingSignature = %q, want %q", resp.ThinkingSignature, "encrypted_opaque_blob")
	}
}
