package oagmsg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseResponsePreservesReasoningSignaturesToolsAndUsage(t *testing.T) {
	cases := []struct {
		name      string
		handler   ProtocolHandler
		raw       []byte
		wantUsage func(*UnifiedUsage) bool
	}{
		{
			name:    "Anthropic",
			handler: &AnthropicHandler{},
			raw:     []byte(`{"id":"msg_1","model":"claude","content":[{"type":"thinking","thinking":"plan","signature":"sig_1"},{"type":"text","text":"done"},{"type":"tool_use","id":"toolu_1","name":"calc","input":{"x":1}}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}`),
			wantUsage: func(u *UnifiedUsage) bool {
				return u.PromptTokens == 10 && u.CompletionTokens == 20 && u.CacheCreationInputTokens == 3 && u.CacheReadInputTokens == 4
			},
		},
		{
			name:    "OpenAI",
			handler: &OpenAIHandler{},
			raw:     []byte(`{"id":"chatcmpl_1","created":1,"model":"gpt","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"done","reasoning_content":"plan","tool_calls":[{"id":"call_1","type":"function","function":{"name":"calc","arguments":"{\"x\":1}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":5}}}`),
			wantUsage: func(u *UnifiedUsage) bool {
				return u.PromptTokens == 10 && u.CompletionTokens == 20 && u.CachedTokens == 2 && u.ReasoningTokens == 5
			},
		},
		{
			name:    "Interactions",
			handler: &InteractionsHandler{},
			raw:     []byte(`{"id":"resp_1","model":"gpt","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"enc_1"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},{"type":"function_call","call_id":"call_1","name":"calc","arguments":"{\"x\":1}"}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":5}}}`),
			wantUsage: func(u *UnifiedUsage) bool {
				return u.PromptTokens == 10 && u.CompletionTokens == 20 && u.CachedTokens == 2 && u.ReasoningTokens == 5
			},
		},
		{
			name:    "GoogleInteractions",
			handler: &GoogleInteractionsHandler{},
			raw:     []byte(`{"id":"int_1","model":"gemini","status":"completed","steps":[{"type":"thought","content":[{"type":"text","text":"plan"}],"signature":"sig_1"},{"type":"model_output","content":[{"type":"text","text":"done"}]},{"type":"function_call","call_id":"call_1","name":"calc","arguments":{"x":1}}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"cached_tokens":2,"reasoning_tokens":5}}`),
			wantUsage: func(u *UnifiedUsage) bool {
				return u.PromptTokens == 10 && u.CompletionTokens == 20 && u.CachedTokens == 2 && u.ReasoningTokens == 5
			},
		},
		{
			name:    "Gemini",
			handler: &GeminiHandler{},
			raw:     []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true,"thoughtSignature":"sig_1"},{"text":"done"},{"functionCall":{"id":"call_1","name":"calc","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"thoughtsTokenCount":5,"cachedContentTokenCount":2,"totalTokenCount":35},"modelVersion":"gemini"}`),
			wantUsage: func(u *UnifiedUsage) bool {
				return u.PromptTokens == 10 && u.CompletionTokens == 20 && u.CachedTokens == 2 && u.ReasoningTokens == 5 && u.TotalTokens == 35
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := tt.handler.ParseResponse(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if resp.ThinkingContent != "plan" {
				t.Fatalf("ThinkingContent = %q", resp.ThinkingContent)
			}
			if tt.name != "OpenAI" && resp.ThinkingSignature == "" {
				t.Fatalf("missing thinking signature: %#v", resp)
			}
			if len(resp.ToolCalls) != 1 {
				t.Fatalf("ToolCalls = %#v", resp.ToolCalls)
			}
			if resp.Usage == nil || !tt.wantUsage(resp.Usage) {
				t.Fatalf("Usage = %#v", resp.Usage)
			}
		})
	}
}

func TestFormatResponseEmitsReasoningSignaturesToolsAndUsage(t *testing.T) {
	resp := &UnifiedResponse{
		ID:                "resp_1",
		Model:             "model_1",
		Content:           "done",
		FinishReason:      "stop",
		ThinkingContent:   "plan",
		ThinkingSignature: "sig_1",
		ToolCalls: []map[string]any{{
			"id":       "call_1",
			"type":     "function",
			"function": map[string]any{"name": "calc", "arguments": `{"x":1}`},
		}},
		Usage: &UnifiedUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 35, CachedTokens: 2, ReasoningTokens: 5},
	}

	outputs := map[string][]byte{}
	for name, handler := range map[string]ProtocolHandler{
		"Anthropic":          &AnthropicHandler{},
		"OpenAI":             &OpenAIHandler{},
		"Interactions":       &InteractionsHandler{},
		"GoogleInteractions": &GoogleInteractionsHandler{},
		"Gemini":             &GeminiHandler{},
	} {
		out, err := handler.FormatResponse(resp, "")
		if err != nil {
			t.Fatalf("%s FormatResponse: %v", name, err)
		}
		if !json.Valid(out) {
			t.Fatalf("%s output invalid JSON: %s", name, out)
		}
		outputs[name] = out
	}

	checks := map[string][]string{
		"Anthropic":          {`"type":"thinking"`, `"signature":"sig_1"`, `"type":"tool_use"`, `"cache_read_input_tokens":2`},
		"OpenAI":             {`"reasoning_content":"plan"`, `"tool_calls"`, `"reasoning_tokens":5`, `"cached_tokens":2`},
		"Interactions":       {`"type":"reasoning"`, `"encrypted_content":"sig_1"`, `"type":"function_call"`, `"reasoning_tokens":5`},
		"GoogleInteractions": {`"type":"thought"`, `"signature":"sig_1"`, `"type":"function_call"`, `"call_id":"call_1"`, `"reasoning_tokens":5`},
		"Gemini":             {`"thought":true`, `"thoughtSignature":"sig_1"`, `"functionCall"`, `"thoughtsTokenCount":5`},
	}
	for name, needles := range checks {
		out := string(outputs[name])
		for _, needle := range needles {
			if !strings.Contains(out, needle) {
				t.Fatalf("%s output missing %s: %s", name, needle, out)
			}
		}
	}
}
