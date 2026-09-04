package oagmsg

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// --- TranslationContext Unit Tests ---

func TestContext_RestoreToolName(t *testing.T) {
	ctx := &TranslationContext{
		ToolNameReverse: map[string]string{
			"cr_ent": "mcp__memory__create_entities",
			"search": "brave_search",
		},
	}
	if got := ctx.RestoreToolName("cr_ent"); got != "mcp__memory__create_entities" {
		t.Fatalf("expected restored name, got %q", got)
	}
	// Unknown names pass through.
	if got := ctx.RestoreToolName("unknown"); got != "unknown" {
		t.Fatalf("expected passthrough, got %q", got)
	}
	// Nil context is safe.
	var nilCtx *TranslationContext
	if got := nilCtx.RestoreToolName("test"); got != "test" {
		t.Fatalf("expected passthrough on nil ctx, got %q", got)
	}
}

func TestContext_IsImageDuplicate(t *testing.T) {
	ctx := &TranslationContext{}

	// First time — not duplicate.
	if ctx.IsImageDuplicate("img_1", "base64data") {
		t.Fatal("first call should not be duplicate")
	}
	// Same data same ID — duplicate.
	if !ctx.IsImageDuplicate("img_1", "base64data") {
		t.Fatal("second call with same data should be duplicate")
	}
	// Different data same ID — not duplicate.
	if ctx.IsImageDuplicate("img_1", "different_data") {
		t.Fatal("different data should not be duplicate")
	}
	// Empty item ID — never duplicate.
	if ctx.IsImageDuplicate("", "anything") {
		t.Fatal("empty item ID should never be duplicate")
	}
	// Nil context — never duplicate.
	var nilCtx *TranslationContext
	if nilCtx.IsImageDuplicate("img_1", "data") {
		t.Fatal("nil context should never be duplicate")
	}
}

func TestContext_EffectiveFinishReason(t *testing.T) {
	ctx := &TranslationContext{}
	// No tool calls seen — pass through.
	if got := ctx.EffectiveFinishReason("stop"); got != "stop" {
		t.Fatalf("expected stop, got %q", got)
	}
	// After seeing tool call — override ordinary stop.
	ctx.SawToolCall = true
	if got := ctx.EffectiveFinishReason("stop"); got != "tool_calls" {
		t.Fatalf("expected tool_calls, got %q", got)
	}
	if got := ctx.EffectiveFinishReason("length"); got != "length" {
		t.Fatalf("expected length, got %q", got)
	}
	if got := ctx.EffectiveFinishReason("content_filter"); got != "content_filter" {
		t.Fatalf("expected content_filter, got %q", got)
	}
	// Nil context — pass through.
	var nilCtx *TranslationContext
	if got := nilCtx.EffectiveFinishReason("length"); got != "length" {
		t.Fatalf("expected length, got %q", got)
	}
}

// --- Middleware Integration Tests ---

func TestSession_Middleware_ToolNameRestore(t *testing.T) {
	ctx := &TranslationContext{
		ToolNameReverse: map[string]string{
			"sh": "bash_shell_command",
		},
	}
	session, err := NewStreamSession(FormatOpenAI, FormatOpenAI, "gpt-4", WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	// Send a tool call with shortened name.
	out, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"sh","arguments":""}}]},"finish_reason":null}]}`))
	if len(out) == 0 {
		t.Fatal("expected output for tool call")
	}
	// Output should contain restored name.
	assertContains(t, string(out[0]), "bash_shell_command")
}

func TestSession_Middleware_SawToolCallOverridesFinish(t *testing.T) {
	ctx := &TranslationContext{}
	session, err := NewStreamSession(FormatOpenAI, FormatOpenAI, "gpt-4", WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	// Start.
	session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))

	// Tool call (sets SawToolCall).
	session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"search","arguments":""}}]},"finish_reason":null}]}`))

	if !ctx.SawToolCall {
		t.Fatal("expected SawToolCall=true after tool call event")
	}

	// Finish with "stop" — should be overridden to "tool_calls".
	out, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
	found := false
	for _, chunk := range out {
		if strings.Contains(string(chunk), `"tool_calls"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected finish_reason=tool_calls override")
	}
}

func TestSession_Middleware_ImageDedup(t *testing.T) {
	ctx := &TranslationContext{}
	// Use Codex→Codex since image events are native to the Codex protocol.
	session, err := NewStreamSession(FormatCodex, FormatCodex, "codex", WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	// First image — should pass through.
	out1, _ := session.Translate([]byte(`data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"abc123","output_format":"png","item_id":"img_1"}`))
	if len(out1) == 0 {
		t.Fatal("first image should produce output")
	}

	// Same image same ID — should be deduplicated.
	out2, _ := session.Translate([]byte(`data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"abc123","output_format":"png","item_id":"img_1"}`))
	if len(out2) != 0 {
		t.Fatal("duplicate image should be suppressed")
	}

	// Different data same ID — should pass through.
	out3, _ := session.Translate([]byte(`data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"xyz789","output_format":"png","item_id":"img_1"}`))
	if len(out3) == 0 {
		t.Fatal("different image data should produce output")
	}
}

func TestSession_NoContext_NoMiddleware(t *testing.T) {
	// Without context, middleware should not run (backward compatibility).
	session, err := NewStreamSession(FormatOpenAI, FormatOpenAI, "gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	// Shortened tool name should pass through unchanged (no restore).
	out, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"sh","arguments":""}}]},"finish_reason":null}]}`))
	if len(out) == 0 {
		t.Fatal("expected output")
	}
	// Should still have "sh", not restored.
	assertContains(t, string(out[0]), `"sh"`)
}

// --- Builder WithTranslationContext Tests ---

func TestBuilder_WithTranslationContext(t *testing.T) {
	ctx := &TranslationContext{}

	input := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	_, err := From(FormatOpenAI).WithTranslationContext(ctx).Request(input).To(FormatOpenAI)
	if err != nil {
		t.Fatal(err)
	}

	// Context should have been populated.
	if !ctx.IsStreaming {
		t.Fatal("expected IsStreaming=true")
	}
	if ctx.ModelName != "gpt-4" {
		t.Fatalf("expected ModelName=gpt-4, got %q", ctx.ModelName)
	}
	if ctx.SourceFormat != FormatOpenAI {
		t.Fatalf("expected SourceFormat=openai, got %q", ctx.SourceFormat)
	}
	if ctx.TargetFormat != FormatOpenAI {
		t.Fatalf("expected TargetFormat=openai, got %q", ctx.TargetFormat)
	}
	if ctx.OriginalRequestJSON == nil {
		t.Fatal("expected OriginalRequestJSON to be set")
	}
}

// --- Fix B: Tool Call Dedup Tests ---

func TestSession_Middleware_ToolCallDedup_SuppressDoneAfterDeltas(t *testing.T) {
	// When arguments were streamed via deltas, the final .done event should be suppressed.
	ctx := &TranslationContext{}
	session, err := NewStreamSession(FormatCodex, FormatCodex, "codex", WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	// 1. output_item.added → ToolStart
	out1, _ := session.Translate([]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_abc","name":"search"}}`))
	if len(out1) == 0 {
		t.Fatal("ToolStart should produce output")
	}

	// 2. arguments.delta → ToolDelta (streams args incrementally)
	out2, _ := session.Translate([]byte(`data: {"type":"response.function_call_arguments.delta","call_id":"call_abc","delta":"{\"q\":"}`))
	if len(out2) == 0 {
		t.Fatal("ToolDelta should produce output")
	}

	// 3. arguments.done → should be SUPPRESSED (args already streamed)
	out3, _ := session.Translate([]byte(`data: {"type":"response.function_call_arguments.done","call_id":"call_abc","arguments":"{\"q\":\"test\"}"}`))
	if len(out3) != 0 {
		t.Fatalf("ToolDone should be suppressed after deltas, got %d outputs", len(out3))
	}
}

func TestSession_Middleware_ToolCallDedup_AllowDoneWithoutDeltas(t *testing.T) {
	// When no deltas were streamed, ToolDone should pass through.
	ctx := &TranslationContext{}
	session, err := NewStreamSession(FormatCodex, FormatCodex, "codex", WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	// 1. output_item.added
	session.Translate([]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_abc","name":"search"}}`))

	// 2. arguments.done WITHOUT any prior deltas → should pass through
	out, _ := session.Translate([]byte(`data: {"type":"response.function_call_arguments.done","call_id":"call_abc","arguments":"{\"q\":\"test\"}"}`))
	if len(out) == 0 {
		t.Fatal("ToolDone without prior deltas should produce output")
	}
}

func TestSession_Middleware_ToolCallDoneFallsBackFromUnknownItemID(t *testing.T) {
	var param any

	added := TranslateStream(context.Background(), FormatCodex, FormatOpenAI, "codex", nil, nil,
		[]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"TaskCreate","arguments":""}}`), &param)
	if len(added) != 1 {
		t.Fatalf("added chunks = %d, want 1", len(added))
	}

	done := TranslateStream(context.Background(), FormatCodex, FormatOpenAI, "codex", nil, nil,
		[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"TaskCreate","arguments":"{\"subject\":\"test\"}"}}`), &param)
	if len(done) != 1 {
		t.Fatalf("done chunks = %d, want 1", len(done))
	}

	addedName := gjson.GetBytes(added[0], "choices.0.delta.tool_calls.0.function.name").String()
	doneName := gjson.GetBytes(done[0], "choices.0.delta.tool_calls.0.function.name").String()
	if got := addedName + doneName; got != "TaskCreate" {
		t.Fatalf("assembled tool name = %q, want %q", got, "TaskCreate")
	}

	toolCall := gjson.GetBytes(done[0], "choices.0.delta.tool_calls.0")
	if toolCall.Get("id").Exists() || toolCall.Get("function.name").Exists() {
		t.Fatalf("done chunk repeated tool identity: %s", toolCall.Raw)
	}
	if got := toolCall.Get("index").Int(); got != 0 {
		t.Fatalf("done tool index = %d, want 0", got)
	}
	if got := toolCall.Get("function.arguments").String(); got != `{"subject":"test"}` {
		t.Fatalf("done arguments = %q", got)
	}
}

// --- Fix C: Fallback Path Tests ---

func TestSession_Middleware_FallbackToolFromOutputItemDone(t *testing.T) {
	// When output_item.done arrives for a tool that was never announced via output_item.added,
	// the middleware should synthesize a ToolStart + pass through ToolDone.
	ctx := &TranslationContext{}
	session, err := NewStreamSession(FormatCodex, FormatCodex, "codex", WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	// output_item.done with tool type, NO prior output_item.added
	out, _ := session.Translate([]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_xyz","name":"get_weather","arguments":"{\"city\":\"SF\"}"}}`))

	if !ctx.SawToolCall {
		t.Fatal("expected SawToolCall=true from fallback path")
	}

	// Should have output (middleware synthesizes ToolStart + passes ToolDone).
	if len(out) == 0 {
		t.Fatal("fallback path should produce output")
	}

	// Verify the output contains the tool name.
	found := false
	for _, chunk := range out {
		if strings.Contains(string(chunk), "get_weather") {
			found = true
		}
	}
	if !found {
		t.Fatal("output should contain tool name 'get_weather'")
	}
}

func TestSession_Middleware_DuplicateToolDone_Suppressed(t *testing.T) {
	// If the same ToolCallID gets multiple ToolDone events, only the first passes.
	ctx := &TranslationContext{}
	session, err := NewStreamSession(FormatCodex, FormatCodex, "codex", WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	// First: output_item.added
	session.Translate([]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_dup","name":"search"}}`))

	// First ToolDone → pass through
	out1, _ := session.Translate([]byte(`data: {"type":"response.function_call_arguments.done","call_id":"call_dup","arguments":"{}"}`))
	if len(out1) == 0 {
		t.Fatal("first ToolDone should pass through")
	}

	// Second ToolDone from output_item.done → should be suppressed
	out2, _ := session.Translate([]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_dup","name":"search","arguments":"{}"}}`))
	if len(out2) != 0 {
		t.Fatalf("duplicate ToolDone should be suppressed, got %d outputs", len(out2))
	}
}

// --- Serializer Fix Tests ---

func TestOpenAISerializer_EventError(t *testing.T) {
	session, err := NewStreamSession(FormatAnthropic, FormatOpenAI, "claude-3.5")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate Anthropic error event.
	out, _ := session.Translate([]byte(`data: {"type":"error","error":{"type":"overloaded_error","message":"Service overloaded"}}`))
	if len(out) == 0 {
		t.Fatal("EventError should produce OpenAI error JSON output")
	}
	s := string(out[0])
	if !strings.Contains(s, "overloaded_error") || !strings.Contains(s, "Service overloaded") {
		t.Fatalf("error output missing fields: %s", s)
	}
}

func TestOpenAISerializer_EventImageDelta(t *testing.T) {
	session, err := NewStreamSession(FormatCodex, FormatOpenAI, "codex")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := session.Translate([]byte(`data: {"type":"response.image_generation_call.partial_image","partial_image_b64":"dGVzdA==","output_format":"png","item_id":"img_1"}`))
	if len(out) == 0 {
		t.Fatal("EventImageDelta should produce OpenAI images[] output")
	}
	s := string(out[0])
	if !strings.Contains(s, "images") || !strings.Contains(s, "image_url") {
		t.Fatalf("image output missing images[]: %s", s)
	}
	if !strings.Contains(s, "data:image/png;base64,dGVzdA==") {
		t.Fatalf("image output missing data URL: %s", s)
	}
}

func TestOpenAISerializer_ToolDoneWithArgs(t *testing.T) {
	// EventToolDone with args (standalone, no middleware) should output tool_calls.
	h := &OpenAIHandler{}
	ser := h.NewStreamSerializer("gpt-4")
	out := ser.Serialize(StreamDelta{
		Type:     EventToolDone,
		ToolArgs: `{"q":"test"}`,
	})
	if len(out) == 0 {
		t.Fatal("ToolDone with args should produce output")
	}
	if !strings.Contains(string(out[0]), `"arguments"`) {
		t.Fatal("output should contain arguments")
	}
}

func TestOpenAISerializer_CacheCreationTokens(t *testing.T) {
	h := &OpenAIHandler{}
	ser := h.NewStreamSerializer("gpt-4")
	out := ser.Serialize(StreamDelta{
		Type: EventUsage,
		Usage: &UnifiedUsage{
			PromptTokens:             100,
			CompletionTokens:         50,
			TotalTokens:              150,
			CacheCreationInputTokens: 42,
		},
	})
	if len(out) == 0 {
		t.Fatal("Usage should produce output")
	}
	if !strings.Contains(string(out[0]), "cached_creation_tokens") {
		t.Fatalf("usage output missing cached_creation_tokens: %s", string(out[0]))
	}
}
