package oagmsg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// ----------------------------------------------------------------
// Task 1: ContentBlock types + Format
// ----------------------------------------------------------------

func TestContentBlockInterface(t *testing.T) {
	// All 9 block types must implement ContentBlock
	blocks := []ContentBlock{
		TextBlock{Text: "hello"},
		ImageBlock{MediaType: "image/png", Data: "base64data"},
		FileBlock{Filename: "test.pdf", MediaType: "application/pdf", Data: "pdfdata"},
		ToolUseBlock{ID: "call_1", Name: "read_file", Input: map[string]any{"path": "/tmp"}},
		ToolResultBlock{ToolUseID: "call_1", Content: "file contents"},
		RawBlock{RawData: map[string]any{"custom": "data"}},
	}

	expectedTypes := []string{
		"text", "image", "file", "tool_use", "tool_result", "raw",
	}

	for i, block := range blocks {
		if block.blockType() != expectedTypes[i] {
			t.Errorf("block %d: expected type %q, got %q", i, expectedTypes[i], block.blockType())
		}
	}
}

func TestIsStandardFormat(t *testing.T) {
	tests := []struct {
		format Format
		want   bool
	}{
		{FormatOpenAI, true},
		{FormatAnthropic, true},
		{FormatGemini, true},
		{FormatInteractions, true},
		{FormatCodex, true},
		{Format("antigravity"), true},
		{Format("unknown"), false},
		{Format(""), false},
	}

	for _, tt := range tests {
		if got := IsStandardFormat(tt.format); got != tt.want {
			t.Errorf("IsStandardFormat(%q) = %v, want %v", tt.format, got, tt.want)
		}
	}
}

// ----------------------------------------------------------------
// Task 2: OagMessage constructors + introspection
// ----------------------------------------------------------------

func TestSystemMsg(t *testing.T) {
	msg := SystemMsg("You are helpful")
	if msg.Role != "system" {
		t.Errorf("expected role 'system', got %q", msg.Role)
	}
	if msg.GetText() != "You are helpful" {
		t.Errorf("expected text 'You are helpful', got %q", msg.GetText())
	}
}

func TestUserTextMsg(t *testing.T) {
	msg := UserTextMsg("Hello")
	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if msg.GetText() != "Hello" {
		t.Errorf("expected text 'Hello', got %q", msg.GetText())
	}
}

func TestAssistantTextMsg(t *testing.T) {
	msg := AssistantTextMsg("Hi there")
	if msg.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msg.Role)
	}
}

func TestToolResultMsg(t *testing.T) {
	msg := ToolResultMsg("call_1", "result data", false)
	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if !msg.HasToolResult() {
		t.Error("expected HasToolResult() = true")
	}
	results := msg.GetToolResults()
	if len(results) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(results))
	}
	if results[0].ToolUseID != "call_1" {
		t.Errorf("expected ToolUseID 'call_1', got %q", results[0].ToolUseID)
	}
}

func TestHasToolUse(t *testing.T) {
	msg := OagMessage{
		Role: "assistant",
		Content: []ContentBlock{
			TextBlock{Text: "Let me help"},
			ToolUseBlock{ID: "call_1", Name: "read_file", Input: map[string]any{}},
		},
	}
	if !msg.HasToolUse() {
		t.Error("expected HasToolUse() = true")
	}
	if msg.HasToolResult() {
		t.Error("expected HasToolResult() = false")
	}
	uses := msg.GetToolUses()
	if len(uses) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(uses))
	}
}

func TestGetTextMultipleBlocks(t *testing.T) {
	msg := OagMessage{
		Role: "user",
		Content: []ContentBlock{
			TextBlock{Text: "part1"},
			ImageBlock{URL: "http://example.com/img.png"},
			TextBlock{Text: "part2"},
		},
	}
	text := msg.GetText()
	if text != "part1\npart2" {
		t.Errorf("expected 'part1\\npart2', got %q", text)
	}
}

// ----------------------------------------------------------------
// Task 3: Builder
// ----------------------------------------------------------------

func TestBuilderBasic(t *testing.T) {
	msg := NewMessageBuilder("user").
		AddText("Hello").
		AddImage("image/png", "base64data").
		Build()

	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(msg.Content))
	}
}

func TestBuilderValidateSystemImageError(t *testing.T) {
	b := NewMessageBuilder("system").AddText("ok").AddImage("image/png", "data")
	if err := b.Validate(); err == nil {
		t.Error("expected error for system + ImageBlock")
	}
}

func TestBuilderValidateUserToolUseError(t *testing.T) {
	b := NewMessageBuilder("user").AddToolUse("id", "name", nil)
	if err := b.Validate(); err == nil {
		t.Error("expected error for user + ToolUseBlock")
	}
}

func TestBuilderValidateAssistantToolResultError(t *testing.T) {
	b := NewMessageBuilder("assistant").AddToolResult("id", "content", false)
	if err := b.Validate(); err == nil {
		t.Error("expected error for assistant + ToolResultBlock")
	}
}

func TestBuilderValidateSuccess(t *testing.T) {
	b := NewMessageBuilder("assistant").
		AddText("response").
		AddToolUse("call_1", "read_file", map[string]any{"path": "/tmp"})
	if err := b.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ----------------------------------------------------------------
// Task 5: HandlerRegistry
// ----------------------------------------------------------------

func TestDefaultRegistrySingleton(t *testing.T) {
	r1 := DefaultRegistry()
	r2 := DefaultRegistry()
	if r1 != r2 {
		t.Error("DefaultRegistry should return the same instance")
	}
}

func TestRegistryGet(t *testing.T) {
	r := DefaultRegistry()

	if _, ok := r.Get(FormatOpenAI); !ok {
		t.Error("OpenAI handler not found")
	}
	if _, ok := r.Get(FormatAnthropic); !ok {
		t.Error("Anthropic handler not found")
	}
	if _, ok := r.Get(FormatGemini); !ok {
		t.Error("Gemini handler not found")
	}
	if _, ok := r.Get(FormatInteractions); !ok {
		t.Error("Interactions handler not found")
	}
	if _, ok := r.Get(FormatCodex); !ok {
		t.Error("Codex handler not found")
	}
	if _, ok := r.Get(FormatAntigravity); !ok {
		t.Error("Antigravity handler not found")
	}
}

func TestSerializeRequestPreservingKeepsUnknownTopLevelFields(t *testing.T) {
	source := []byte(`{
		"model":"gpt-test",
		"parallel_tool_calls":false,
		"metadata":{"trace":"keep"},
		"messages":[{"role":"user","content":[{"type":"text","text":"old"}]}]
	}`)
	handler := DefaultRegistry().MustGet(FormatOpenAI)
	req, err := handler.ParseRequest(source)
	if err != nil {
		t.Fatalf("ParseRequest() error = %v", err)
	}
	req.Messages[0].Content = []ContentBlock{TextBlock{Text: "new"}}
	out, err := DefaultRegistry().SerializeRequestPreserving(FormatOpenAI, req, source)
	if err != nil {
		t.Fatalf("SerializeRequestPreserving() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got, ok := decoded["parallel_tool_calls"].(bool); !ok || got {
		t.Fatalf("parallel_tool_calls = %#v, want false", decoded["parallel_tool_calls"])
	}
	metadata, ok := decoded["metadata"].(map[string]any)
	if !ok || metadata["trace"] != "keep" {
		t.Fatalf("metadata = %#v", decoded["metadata"])
	}
	messages := decoded["messages"].([]any)
	message := messages[0].(map[string]any)
	if message["content"] != "new" {
		t.Fatalf("message content = %#v, want new", message["content"])
	}
}

func TestCodexHandlerFormat(t *testing.T) {
	h := &CodexHandler{}
	if h.Format() != FormatCodex {
		t.Errorf("expected FormatCodex, got %q", h.Format())
	}
}

// ----------------------------------------------------------------
// Task 6: OpenAI roundtrip
// ----------------------------------------------------------------

func TestOpenAIRoundtrip(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "You are helpful"},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!", "tool_calls": [
				{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"/tmp\"}"}
				}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "file contents"}
		],
		"temperature": 0.7,
		"stream": true
	}`

	h := &OpenAIHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if req.Model != "gpt-4" {
		t.Errorf("model: expected 'gpt-4', got %q", req.Model)
	}
	if !req.Stream {
		t.Error("expected Stream=true")
	}
	if len(req.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(req.Messages))
	}

	// system
	if req.Messages[0].Role != "system" {
		t.Error("msg[0] should be system")
	}
	// user
	if req.Messages[1].Role != "user" {
		t.Error("msg[1] should be user")
	}
	// assistant with tool_calls
	if !req.Messages[2].HasToolUse() {
		t.Error("msg[2] should have tool_calls")
	}
	// tool result (converted to user)
	if req.Messages[3].Role != "user" {
		t.Errorf("msg[3] role should be 'user', got %q", req.Messages[3].Role)
	}
	if !req.Messages[3].HasToolResult() {
		t.Error("msg[3] should have tool result")
	}

	// Serialize back
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed["model"] != "gpt-4" {
		t.Errorf("roundtrip model mismatch: got %v", parsed["model"])
	}
}

// ----------------------------------------------------------------
// Task 6: Anthropic roundtrip
// ----------------------------------------------------------------

func TestAnthropicRoundtrip(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"system": "You are helpful",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "Hi!"},
				{"type": "tool_use", "id": "call_1", "name": "read_file", "input": {"path": "/tmp"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": "file contents"}
			]}
		],
		"max_tokens": 4096
	}`

	h := &AnthropicHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if req.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model mismatch: got %q", req.Model)
	}

	// system + user + assistant + user(tool_result) = 4
	if len(req.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(req.Messages))
	}

	// System
	if req.Messages[0].Role != "system" {
		t.Error("msg[0] should be system")
	}
	// Assistant should have thinking + text + tool_use
	assistantMsg := req.Messages[2]
	if !assistantMsg.HasToolUse() {
		t.Error("assistant msg should have tool_use")
	}

	// Serialize back
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
}

func TestAnthropicSystemBlocks(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"system": [
			{"type": "text", "text": "System part 1", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "System part 2"}
		],
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	h := &AnthropicHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}

	// System message should have 2 TextBlocks
	sysMsg := req.Messages[0]
	if sysMsg.Role != "system" {
		t.Error("msg[0] should be system")
	}
	if len(sysMsg.Content) != 2 {
		t.Errorf("expected 2 system content blocks, got %d", len(sysMsg.Content))
	}
	// Check cache_control preserved
	tb, ok := sysMsg.Content[0].(TextBlock)
	if !ok {
		t.Fatal("expected TextBlock")
	}
	if tb.CacheControl == nil {
		t.Error("expected cache_control on first system block")
	}
}

// ----------------------------------------------------------------
// Task 6: Cross-protocol translate
// ----------------------------------------------------------------

func TestTranslateAnthropicToOpenAI(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"system": "Be helpful",
		"messages": [
			{"role": "user", "content": "What is 2+2?"},
			{"role": "assistant", "content": "4"}
		],
		"max_tokens": 1024
	}`

	out, err := DefaultRegistry().Translate(FormatAnthropic, FormatOpenAI, []byte(input))
	if err != nil {
		t.Fatalf("Translate error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	// Verify OpenAI structure
	msgs, ok := parsed["messages"].([]any)
	if !ok {
		t.Fatal("expected messages array")
	}
	if len(msgs) != 3 { // system + user + assistant
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
}

func TestTranslateOpenAIToAnthropic(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "Be helpful"},
			{"role": "user", "content": "Hello"}
		]
	}`

	out, err := DefaultRegistry().Translate(FormatOpenAI, FormatAnthropic, []byte(input))
	if err != nil {
		t.Fatalf("Translate error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	// Verify Anthropic structure - system should be extracted
	if _, ok := parsed["system"]; !ok {
		t.Error("expected system field in Anthropic output")
	}
	msgs, ok := parsed["messages"].([]any)
	if !ok {
		t.Fatal("expected messages array")
	}
	if len(msgs) != 1 { // only user (system extracted)
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}

// ----------------------------------------------------------------
// Task 7: Gemini roundtrip
// ----------------------------------------------------------------

func TestGeminiRoundtrip(t *testing.T) {
	input := `{
		"model": "gemini-2.5-pro",
		"systemInstruction": {
			"role": "user",
			"parts": [{"text": "You are helpful"}]
		},
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "Hello"}]
			},
			{
				"role": "model",
				"parts": [{"text": "Hi!"}]
			}
		],
		"generationConfig": {
			"temperature": 0.7,
			"maxOutputTokens": 2048
		}
	}`

	h := &GeminiHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	if req.Model != "gemini-2.5-pro" {
		t.Errorf("model mismatch: got %q", req.Model)
	}
	// system + user + assistant = 3
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}

	// Serialize back
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if _, ok := parsed["systemInstruction"]; !ok {
		t.Error("expected systemInstruction in Gemini output")
	}
}

func TestGeminiParseRequestSharesParsedRoot(t *testing.T) {
	input := []byte(`{
		"model": "gemini-2.5-pro",
		"systemInstruction": {
			"role": "user",
			"parts": [{"text": "You are a validator"}]
		},
		"contents": [
			{"role": "user", "parts": [{"text": "Hello"}]},
			{"role": "model", "parts": [{"text": "World"}]}
		],
		"tools": [
			{"functionDeclarations": [{"name": "echo", "description": "echo input"}]}
		]
	}`)

	h := &GeminiHandler{}
	reqFromBytes, err := h.ParseRequest(input)
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	root := util.ParseGJSONBytesNoCopy(input)
	msgsFromRoot := h.parseMessagesFromRoot(root)
	if !reflect.DeepEqual(reqFromBytes.Messages, msgsFromRoot) {
		t.Fatalf("messages mismatch between ParseRequest and parseMessagesFromRoot: %#v != %#v", reqFromBytes.Messages, msgsFromRoot)
	}

	reqFromRoot := h.parseRequestFromRoot(root)
	if !reflect.DeepEqual(reqFromBytes, reqFromRoot) {
		t.Fatalf("parseRequestFromRoot should produce same request as ParseRequest path")
	}

	msgsFromRaw, err := h.ParseMessages(input)
	if err != nil {
		t.Fatalf("ParseMessages error: %v", err)
	}
	if !reflect.DeepEqual(reqFromRoot.Messages, msgsFromRaw) {
		t.Fatalf("messages mismatch between parseMessagesFromRoot and ParseMessages")
	}
}

func TestTranslateAnthropicToGemini(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"system": "Be concise",
		"messages": [
			{"role": "user", "content": "Hi"},
			{"role": "assistant", "content": "Hello"}
		]
	}`

	out, err := DefaultRegistry().Translate(FormatAnthropic, FormatGemini, []byte(input))
	if err != nil {
		t.Fatalf("Translate error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if _, ok := parsed["systemInstruction"]; !ok {
		t.Error("expected systemInstruction in Gemini output")
	}
	if _, ok := parsed["contents"]; !ok {
		t.Error("expected contents array in Gemini output")
	}
}

func TestTranslateGeminiRequestDropsHiddenThoughtParts(t *testing.T) {
	input := []byte(`{
		"model": "gemini-2.5-pro",
		"systemInstruction": {
			"parts": [
				{"thought": true, "text": "hidden system", "thoughtSignature": "opaque-system-state"},
				{"text": "visible system"}
			]
		},
		"contents": [
			{
				"role": "model",
				"parts": [
					{"thought": true, "text": "internal reasoning", "thoughtSignature": "opaque-provider-state"},
					{"text": "visible answer"}
				]
			},
			{"role": "user", "parts": [{"text": "next turn"}]}
		]
	}`)

	for _, target := range []Format{FormatOpenAI, FormatAnthropic, FormatCodex} {
		t.Run(string(target), func(t *testing.T) {
			out := TranslateRequest(FormatGemini, target, "target-model", input, false)
			output := string(out)
			for _, forbidden := range []string{
				"hidden system",
				"internal reasoning",
				"opaque-system-state",
				"opaque-provider-state",
			} {
				if strings.Contains(output, forbidden) {
					t.Fatalf("hidden thought %q survived for %s: %s", forbidden, target, out)
				}
			}
			if !strings.Contains(output, "visible answer") || !strings.Contains(output, "next turn") {
				t.Fatalf("visible Gemini content was lost for %s: %s", target, out)
			}
		})
	}
}

// ----------------------------------------------------------------
// Task 7: Interactions roundtrip
// ----------------------------------------------------------------

func TestInteractionsRoundtrip(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"instructions": "Be helpful",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Hello"}]
			},
			{
				"type": "function_call",
				"call_id": "call_1",
				"name": "read_file",
				"arguments": "{\"path\":\"/tmp\"}"
			},
			{
				"type": "function_call_output",
				"call_id": "call_1",
				"output": "file contents"
			}
		]
	}`

	h := &InteractionsHandler{}
	req, err := h.ParseRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}

	// instructions (system) + user + assistant(tool_call) + user(tool_result) = 4
	if len(req.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(req.Messages))
	}

	if req.Messages[0].Role != "system" {
		t.Error("msg[0] should be system (from instructions)")
	}

	// Serialize back
	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
}

// ----------------------------------------------------------------
// Preservation and malformed input
// ----------------------------------------------------------------

func TestBuilderPreservesUnknownTopLevelFields(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hello"}],
		"presence_penalty": 0.2,
		"seed": 1234,
		"metadata": {"trace_id": "abc"}
	}`)

	out, err := From(FormatOpenAI).Request(input).To(FormatAnthropic)
	if err != nil {
		t.Fatalf("To error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed["presence_penalty"] != 0.2 {
		t.Errorf("presence_penalty not preserved: %v", parsed["presence_penalty"])
	}
	if parsed["seed"] != float64(1234) {
		t.Errorf("seed not preserved: %v", parsed["seed"])
	}
	metadata, ok := parsed["metadata"].(map[string]any)
	if !ok || metadata["trace_id"] != "abc" {
		t.Errorf("metadata not preserved: %v", parsed["metadata"])
	}
	if _, ok := parsed["messages"]; !ok {
		t.Fatal("expected target structural messages field")
	}
}

func TestRegistryTranslatePreservesUnknownTopLevelFields(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hello"}],
		"parallel_tool_calls": false
	}`)

	out, err := DefaultRegistry().Translate(FormatOpenAI, FormatGemini, input)
	if err != nil {
		t.Fatalf("Translate error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed["parallel_tool_calls"] != false {
		t.Errorf("parallel_tool_calls not preserved: %v", parsed["parallel_tool_calls"])
	}
	if _, ok := parsed["contents"]; !ok {
		t.Fatal("expected target contents field")
	}
	if _, ok := parsed["messages"]; ok {
		t.Fatal("source structural messages field should not be merged into Gemini output")
	}
}

func TestRegistryTranslateSuppressesResponsesParallelToolCallsWithoutTargetTools(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5.4",
		"parallel_tool_calls": true,
		"tools": [{"type": "image_generation"}],
		"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}],
		"tool_choice": {"type": "function", "name": "image_generation"}
	}`)

	for _, target := range []Format{FormatOpenAI, FormatAnthropic} {
		t.Run(string(target), func(t *testing.T) {
			out, err := DefaultRegistry().Translate(FormatOpenAIResponse, target, input)
			if err != nil {
				t.Fatalf("Translate error: %v", err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}
			for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
				if _, ok := parsed[field]; ok {
					t.Fatalf("%s should be omitted for zero surviving tools: %s", field, out)
				}
			}
		})
	}
}

func TestBuilderPreservesUnknownResponseTopLevelFields(t *testing.T) {
	input := []byte(`{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"created": 123,
		"model": "gpt-4",
		"system_fingerprint": "fp-test",
		"choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "Hi"}}]
	}`)

	out, err := From(FormatOpenAI).Response(input).To(FormatAnthropic)
	if err != nil {
		t.Fatalf("To response error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed["system_fingerprint"] != "fp-test" {
		t.Errorf("system_fingerprint not preserved: %v", parsed["system_fingerprint"])
	}
	if parsed["object"] == "chat.completion" {
		t.Fatal("source response structural object should not be merged")
	}
}

func TestResponseTranslationDoesNotPreserveProtocolEnvelope(t *testing.T) {
	input := []byte(`{
		"response": {
			"responseId": "resp-1",
			"candidates": [{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"STOP"}]
		}
	}`)

	out, err := DefaultRegistry().TranslateResponse(FormatAntigravity, FormatInteractions, "model", input)
	if err != nil {
		t.Fatalf("TranslateResponse error: %v", err)
	}
	if gjson.GetBytes(out, "response").Exists() {
		t.Fatalf("source response envelope was preserved: %s", out)
	}
	if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "Hi" {
		t.Fatalf("translated text = %q, want Hi: %s", got, out)
	}
}

func TestOpenAIRawBlockPreservation(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "Hello"},
			{"type": "custom_block", "payload": {"a": 1}}
		]}]
	}`)

	h := &OpenAIHandler{}
	req, err := h.ParseRequest(input)
	if err != nil {
		t.Fatalf("ParseRequest error: %v", err)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 2 {
		t.Fatalf("unexpected messages: %#v", req.Messages)
	}
	raw, ok := req.Messages[0].Content[1].(RawBlock)
	if !ok {
		t.Fatalf("expected RawBlock, got %T", req.Messages[0].Content[1])
	}
	if raw.RawData["type"] != "custom_block" {
		t.Errorf("raw block type not preserved: %v", raw.RawData)
	}

	out, err := h.SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	messages := parsed["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	if content[1].(map[string]any)["type"] != "custom_block" {
		t.Errorf("raw output block not preserved: %v", content[1])
	}
}

func TestParseRequestMalformedInput(t *testing.T) {
	handlers := []ProtocolHandler{
		&OpenAIHandler{},
		&AnthropicHandler{},
		&GeminiHandler{},
		&InteractionsHandler{},
		&GoogleInteractionsHandler{},
		&CodexHandler{},
	}
	for _, handler := range handlers {
		if _, err := handler.ParseRequest([]byte(`{"model":`)); err == nil {
			t.Fatalf("%s ParseRequest succeeded for malformed JSON", handler.Format())
		}
		if _, err := handler.ParseRequest([]byte(`[]`)); err == nil {
			t.Fatalf("%s ParseRequest succeeded for non-object JSON", handler.Format())
		}
	}
}

// ----------------------------------------------------------------
// HasToolsDefined
// ----------------------------------------------------------------

func TestHasToolsDefined(t *testing.T) {
	withTools := `{"messages": [], "tools": [{"type": "function", "function": {"name": "foo"}}]}`
	withoutTools := `{"messages": []}`
	emptyTools := `{"messages": [], "tools": []}`

	h := &OpenAIHandler{}
	if !h.HasToolsDefined([]byte(withTools)) {
		t.Error("expected HasToolsDefined=true for payload with tools")
	}
	if h.HasToolsDefined([]byte(withoutTools)) {
		t.Error("expected HasToolsDefined=false for payload without tools")
	}
	if h.HasToolsDefined([]byte(emptyTools)) {
		t.Error("expected HasToolsDefined=false for empty tools array")
	}
}
