package oagmsg

import (
	"context"
	"strings"
	"testing"

	claudeGemini "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/gemini"
	claudeInteractions "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/interactions"
	claudeOpenAI "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/chat-completions"
	claudeResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	codexOpenAI "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	codexResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/responses"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	geminiResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	interactionsResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/interactions/responses"
	openAIResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

func TestResponseModelPrecedenceToResponsesNonStream(t *testing.T) {
	original := []byte(`{"model":"request-model"}`)
	translated := []byte(`{"model":"translated-model"}`)
	if got := translatorcommon.RequestModelName(original, translated); got != "request-model" {
		t.Fatalf("translator request oracle = %q", got)
	}

	cases := []struct {
		name string
		from Format
		raw  []byte
	}{
		{
			name: "openai chat",
			from: FormatOpenAI,
			raw:  []byte(`{"id":"chatcmpl_1","created":1,"model":"upstream-chat","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`),
		},
		{
			name: "claude",
			from: FormatAnthropic,
			raw:  []byte(`{"id":"msg_1","model":"upstream-claude","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`),
		},
		{
			name: "gemini",
			from: FormatGemini,
			raw:  []byte(`{"responseId":"gem_1","modelVersion":"upstream-gemini","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`),
		},
		{
			name: "codex",
			from: FormatCodex,
			raw:  []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"upstream-codex","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`),
		},
		{
			name: "interactions",
			from: FormatInteractions,
			raw:  []byte(`{"id":"int_1","model":"upstream-interaction","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]}]}`),
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := TranslateNonStream(context.Background(), tt.from, FormatOpenAIResponse, "runtime-model", original, translated, tt.raw, nil)
			if got := gjson.GetBytes(out, "model").String(); got != "request-model" {
				t.Fatalf("response model = %q, want request-model; payload=%s", got, string(out))
			}
		})
	}
}

func TestResponseModelOverrideSkipsCompactionAndAllowsShapedResponse(t *testing.T) {
	original := []byte(`{"model":"request-model"}`)
	compaction := []byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "runtime-model", original, nil, compaction, nil)
	if string(out) != string(compaction) {
		t.Fatalf("compaction changed:\ngot  %s\nwant %s", string(out), string(compaction))
	}
	if gjson.GetBytes(out, "model").Exists() {
		t.Fatalf("compaction must not receive model: %s", string(out))
	}

	shaped := []byte(`{"id":"resp_2","status":"completed","output":[]}`)
	out = TranslateNonStream(context.Background(), FormatOpenAIResponse, FormatOpenAIResponse, "runtime-model", original, nil, shaped, nil)
	if got := gjson.GetBytes(out, "model").String(); got != "request-model" {
		t.Fatalf("shaped response model = %q, want request-model; payload=%s", got, string(out))
	}
}

func TestResponseModelOverridePreservesDataOnlySSEFraming(t *testing.T) {
	original := []byte(`{"model":"request-model"}`)
	raw := []byte("data:  {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"upstream\"}} \n\n")
	out := TranslateStream(context.Background(), FormatCodex, FormatOpenAIResponse, "runtime-model", original, nil, raw, nil)
	if len(out) != 1 {
		t.Fatalf("outputs = %d, want 1", len(out))
	}
	got := string(out[0])
	if !strings.HasPrefix(got, "data:  ") || strings.HasPrefix(got, "event: ") {
		t.Fatalf("data-only framing changed: %q", got)
	}
	if !strings.HasSuffix(got, " \n\n") {
		t.Fatalf("SSE suffix changed: %q", got)
	}
	event, data := parseFidelitySSE(out[0])
	if event != "response.completed" {
		t.Fatalf("event = %q, want response.completed; chunk=%q", event, got)
	}
	if gotModel := data.Get("response.model").String(); gotModel != "upstream" {
		t.Fatalf("response.model = %q, want upstream; chunk=%q", gotModel, got)
	}
}

func TestResponseModelPrecedenceDirectUpstreamOracles(t *testing.T) {
	original := []byte(`{"model":"request-model"}`)
	translated := []byte(`{"model":"translated-model"}`)
	ctx := context.Background()

	t.Run("openai chat to responses", func(t *testing.T) {
		chunk := []byte(`data: {"id":"chatcmpl_1","created":1,"model":"upstream-chat","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		var oracleParam any
		oracle := openAIResponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "runtime-model", original, translated, chunk, &oracleParam)
		assertSSEModel(t, oracle, "response.created", "request-model")
		var param any
		got := TranslateStream(ctx, FormatOpenAI, FormatOpenAIResponse, "runtime-model", original, translated, chunk, &param)
		assertSSEModel(t, got, "response.created", "request-model")
	})

	t.Run("claude to responses", func(t *testing.T) {
		chunk := []byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"upstream-claude"}}`)
		var oracleParam any
		oracle := claudeResponses.ConvertClaudeResponseToOpenAIResponses(ctx, "runtime-model", original, translated, chunk, &oracleParam)
		assertSSEModel(t, oracle, "response.created", "request-model")
		var param any
		got := TranslateStream(ctx, FormatAnthropic, FormatOpenAIResponse, "runtime-model", original, translated, chunk, &param)
		assertSSEModel(t, got, "response.created", "request-model")
	})

	t.Run("gemini to responses", func(t *testing.T) {
		chunk := []byte(`data: {"responseId":"gem_1","modelVersion":"upstream-gemini","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
		var oracleParam any
		oracle := geminiResponses.ConvertGeminiResponseToOpenAIResponses(ctx, "runtime-model", original, translated, chunk, &oracleParam)
		assertSSEModel(t, oracle, "response.created", "request-model")
		var param any
		got := TranslateStream(ctx, FormatGemini, FormatOpenAIResponse, "runtime-model", original, translated, chunk, &param)
		assertSSEModel(t, got, "response.created", "request-model")
	})

	t.Run("codex to responses", func(t *testing.T) {
		chunk := []byte(`data: {"type":"response.created","response":{"id":"resp_1","created_at":1}}`)
		oracle := codexResponses.ConvertCodexResponseToOpenAIResponses(ctx, "runtime-model", original, translated, chunk, nil)
		assertSSEModel(t, oracle, "response.created", "request-model")
		got := TranslateStream(ctx, FormatCodex, FormatOpenAIResponse, "runtime-model", original, translated, chunk, nil)
		assertSSEModel(t, got, "response.created", "request-model")
	})

	t.Run("google interactions to responses", func(t *testing.T) {
		raw := []byte(`{"id":"int_1","model":"upstream-interaction","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]}]}`)
		// The upstream Interactions converter is callable but not request-aware; it only
		// accepts modelName/root model. oagmsg owns the G10 request precedence fix.
		oracle := interactionsResponses.ConvertInteractionsResponseToOpenAIResponsesNonStream(ctx, "runtime-model", original, translated, raw, nil)
		if got := gjson.GetBytes(oracle, "model").String(); got != "runtime-model" {
			t.Fatalf("oracle model = %q, want runtime-model; payload=%s", got, string(oracle))
		}
		got := TranslateNonStream(ctx, FormatInteractions, FormatOpenAIResponse, "runtime-model", original, translated, raw, nil)
		if gotModel := gjson.GetBytes(got, "model").String(); gotModel != "request-model" {
			t.Fatalf("oagmsg model = %q, want request-model; payload=%s", gotModel, string(got))
		}
	})
}

func TestResponseModelPrecedenceSearchOrder(t *testing.T) {
	raw := []byte(`{"id":"msg_1","model":"upstream-claude","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	cases := []struct {
		name       string
		original   []byte
		translated []byte
		want       string
	}{
		{
			name:       "original model wins",
			original:   []byte(`{"model":"original-top","request":{"model":"original-nested"}}`),
			translated: []byte(`{"model":"translated-top","request":{"model":"translated-nested"}}`),
			want:       "original-top",
		},
		{
			name:       "original request model wins when original top-level missing",
			original:   []byte(`{"request":{"model":"original-nested"}}`),
			translated: []byte(`{"model":"translated-top","request":{"model":"translated-nested"}}`),
			want:       "original-nested",
		},
		{
			name:       "original request model wins when original top-level non-string",
			original:   []byte(`{"model":42,"request":{"model":"original-nested"}}`),
			translated: []byte(`{"model":"translated-top","request":{"model":"translated-nested"}}`),
			want:       "original-nested",
		},
		{
			name:       "original request model wins when original top-level blank",
			original:   []byte(`{"model":"  ","request":{"model":"original-nested"}}`),
			translated: []byte(`{"model":"translated-top","request":{"model":"translated-nested"}}`),
			want:       "original-nested",
		},
		{
			name:       "translated model wins translated request model",
			original:   []byte(`{`),
			translated: []byte(`{"model":"translated-top","request":{"model":"translated-nested"}}`),
			want:       "translated-top",
		},
		{
			name:       "translated request model wins when translated top-level invalid",
			original:   []byte(`{"model":false,"request":{"model":"  "}}`),
			translated: []byte(`{"model":false,"request":{"model":"translated-nested"}}`),
			want:       "translated-nested",
		},
		{
			name:       "runtime fallback when json invalid non-string and blank are exhausted",
			original:   []byte(`{"model":"   ","request":{"model":42}}`),
			translated: []byte(`{"model":false,"request":{"model":"  "}}`),
			want:       "runtime-model",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := TranslateNonStream(context.Background(), FormatAnthropic, FormatOpenAIResponse, "runtime-model", tt.original, tt.translated, raw, nil)
			if got := gjson.GetBytes(out, "model").String(); got != tt.want {
				t.Fatalf("response model = %q, want %q; payload=%s", got, tt.want, string(out))
			}
		})
	}
}

func TestResponseModelPrecedenceToResponsesStreamLifecycle(t *testing.T) {
	original := []byte(`{"model":"request-stream-model"}`)
	translated := []byte(`{"model":"translated-stream-model"}`)
	cases := []struct {
		name string
		from Format
		want string
		// codexPreservesExisting records the same-family Codex contract:
		// existing lifecycle models survive instead of using request/runtime fallback.
		codexPreservesExisting bool
		chunks                 [][]byte
	}{
		{
			name: "openai chat",
			from: FormatOpenAI,
			want: "request-stream-model",
			chunks: [][]byte{
				[]byte(`data: {"id":"chatcmpl_1","created":123,"model":"upstream-chat","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`),
				[]byte(`data: {"id":"chatcmpl_1","model":"upstream-chat","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`),
				[]byte(`data: {"id":"chatcmpl_1","model":"upstream-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
				[]byte(`data: [DONE]`),
			},
		},
		{
			name: "claude",
			from: FormatAnthropic,
			want: "request-stream-model",
			chunks: [][]byte{
				[]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"upstream-claude"}}`),
				[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`),
				[]byte(`data: {"type":"message_stop"}`),
			},
		},
		{
			name: "gemini",
			from: FormatGemini,
			want: "request-stream-model",
			chunks: [][]byte{
				[]byte(`data: {"responseId":"gem_1","modelVersion":"upstream-gemini","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`),
			},
		},
		{
			name:                   "codex preserves existing",
			from:                   FormatCodex,
			want:                   "upstream-codex",
			codexPreservesExisting: true,
			chunks: [][]byte{
				[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"upstream-codex","created_at":123}}`),
				[]byte(`data: {"type":"response.in_progress","response":{"id":"resp_1","model":"upstream-codex","status":"in_progress"}}`),
				[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","model":"upstream-codex","created_at":123,"status":"completed"}}`),
			},
		},
		{
			name: "interactions",
			from: FormatInteractions,
			want: "request-stream-model",
			chunks: [][]byte{
				[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"int_1","model":"upstream-interaction","created":"2026-08-06T00:00:00Z"}}`),
				[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"int_1","model":"upstream-interaction","status":"completed"}}`),
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			lifecycleModels := map[string][]string{
				"response.created":     nil,
				"response.in_progress": nil,
				"response.completed":   nil,
			}
			for _, chunk := range tt.chunks {
				for _, out := range TranslateStream(context.Background(), tt.from, FormatOpenAIResponse, "runtime-model", original, translated, chunk, &param) {
					event, data := parseFidelitySSE(out)
					switch event {
					case "response.created", "response.in_progress", "response.completed":
						lifecycleModels[event] = append(lifecycleModels[event], data.Get("response.model").String())
					}
				}
			}
			for _, event := range []string{"response.created", "response.in_progress", "response.completed"} {
				models := lifecycleModels[event]
				if len(models) != 1 {
					t.Fatalf("%s count = %d, want 1; lifecycle=%#v", event, len(models), lifecycleModels)
				}
				if got := models[0]; got != tt.want {
					t.Fatalf("%s model = %q, want %s; lifecycle=%#v", event, got, tt.want, lifecycleModels)
				}
			}
			if tt.codexPreservesExisting && lifecycleModels["response.completed"][0] != "upstream-codex" {
				t.Fatalf("codex completed model overwritten: lifecycle=%#v", lifecycleModels)
			}
		})
	}
}

func TestResponseModelPrecedenceCodexSameFamilyMissingLifecycle(t *testing.T) {
	original := []byte(`{"model":"request-stream-model"}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","created_at":123}}`),
		[]byte(`data: {"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","created_at":123,"status":"completed"}}`),
	}
	want := map[string]struct {
		exists bool
		model  string
	}{
		"response.created":     {exists: true, model: "request-stream-model"},
		"response.in_progress": {exists: true, model: "request-stream-model"},
		"response.completed":   {exists: false},
	}

	for _, chunk := range chunks {
		outputs := TranslateStream(context.Background(), FormatCodex, FormatOpenAIResponse, "runtime-model", original, nil, chunk, nil)
		if len(outputs) != 1 {
			t.Fatalf("outputs = %d, want 1 for %s", len(outputs), string(chunk))
		}
		event, data := parseFidelitySSE(outputs[0])
		expect, ok := want[event]
		if !ok {
			t.Fatalf("unexpected event %q: %s", event, string(outputs[0]))
		}
		model := data.Get("response.model")
		if model.Exists() != expect.exists {
			t.Fatalf("%s response.model exists=%v, want %v; chunk=%s", event, model.Exists(), expect.exists, string(outputs[0]))
		}
		if expect.exists && model.String() != expect.model {
			t.Fatalf("%s response.model = %q, want %q; chunk=%s", event, model.String(), expect.model, string(outputs[0]))
		}
	}
}

func TestResponseModelPrecedenceGeminiNonStreamDirection(t *testing.T) {
	cases := []struct {
		name       string
		original   []byte
		translated []byte
		raw        []byte
		want       string
		wantExists bool
	}{
		{
			name:       "selected exact blank blocks provider",
			original:   []byte(`{"request":{"model":""}}`),
			raw:        []byte(`{"responseId":"resp_blank","modelVersion":"provider-version","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`),
			want:       "",
			wantExists: true,
		},
		{
			name:       "no request model and no provider model does not use runtime",
			original:   []byte(`{"request":{"input":[]}}`),
			raw:        []byte(`{"responseId":"resp_no_model","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`),
			wantExists: false,
		},
		{
			name:       "response id only nested provider envelope",
			original:   []byte(`{"request":{"input":[]}}`),
			raw:        []byte(`{"response":{"responseId":"resp_nested","modelVersion":"provider-response-id"}}`),
			want:       "provider-response-id",
			wantExists: true,
		},
		{
			name:       "usage only nested provider envelope",
			original:   []byte(`{"request":{"input":[]}}`),
			raw:        []byte(`{"response":{"modelVersion":"provider-usage","usageMetadata":{"promptTokenCount":1}}}`),
			want:       "provider-usage",
			wantExists: true,
		},
		{
			name:       "nil and invalid request docs without provider omit runtime",
			translated: []byte(`{`),
			raw:        []byte(`{"responseId":"resp_invalid_no_model","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`),
			wantExists: false,
		},
		{
			name:       "nil and invalid request docs with provider use provider",
			translated: []byte(`{`),
			raw:        []byte(`{"responseId":"resp_invalid_provider","modelVersion":"provider-invalid","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`),
			want:       "provider-invalid",
			wantExists: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "runtime-model", tt.original, tt.translated, tt.raw, nil)
			model := gjson.GetBytes(out, "model")
			if model.Exists() != tt.wantExists {
				t.Fatalf("model exists=%v, want %v; payload=%s", model.Exists(), tt.wantExists, string(out))
			}
			if tt.wantExists && model.String() != tt.want {
				t.Fatalf("model = %q, want %q; payload=%s", model.String(), tt.want, string(out))
			}
		})
	}
}

func TestResponseModelPrecedenceGeminiNonStreamSSEAggregation(t *testing.T) {
	cases := []struct {
		name       string
		raw        []byte
		want       string
		wantExists bool
	}{
		{
			name: "provider model survives aggregation",
			raw: []byte(strings.Join([]string{
				`data: {"responseId":"resp_sse_provider","modelVersion":"provider-sse","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
			}, "\n")),
			want:       "provider-sse",
			wantExists: true,
		},
		{
			name: "missing provider model omits runtime after aggregation",
			raw: []byte(strings.Join([]string{
				`data: {"responseId":"resp_sse_no_model","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
			}, "\n")),
			wantExists: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "runtime-model", nil, []byte(`{`), tt.raw, nil)
			model := gjson.GetBytes(out, "model")
			if model.Exists() != tt.wantExists {
				t.Fatalf("model exists=%v, want %v; payload=%s", model.Exists(), tt.wantExists, string(out))
			}
			if tt.wantExists && model.String() != tt.want {
				t.Fatalf("model = %q, want %q; payload=%s", model.String(), tt.want, string(out))
			}
		})
	}
}

func TestUsageParityClaudeToResponsesAndStreamEquality(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-upstream","usage":{"input_tokens":13,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}}}`),
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31,"thinking_tokens":2}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var completed gjson.Result
	completedCount := 0
	for _, chunk := range chunks {
		for _, out := range TranslateStream(context.Background(), FormatAnthropic, FormatOpenAIResponse, "claude-test", nil, nil, chunk, &param) {
			event, data := parseFidelitySSE(out)
			if event == "response.completed" {
				completedCount++
				completed = data
			}
		}
	}
	if completedCount != 1 {
		t.Fatalf("response.completed count = %d, want 1", completedCount)
	}
	if got := completed.Get("response.usage.input_tokens").Int(); got != 22044 {
		t.Fatalf("stream input_tokens = %d, want 22044; payload=%s", got, completed.Raw)
	}
	if got := completed.Get("response.usage.input_tokens_details.cached_tokens").Int(); got != 22000 {
		t.Fatalf("stream cached_tokens = %d, want 22000; payload=%s", got, completed.Raw)
	}
	if got := completed.Get("response.usage.input_tokens_details.cache_write_tokens").Int(); got != 31 {
		t.Fatalf("stream cache_write_tokens = %d, want 31; payload=%s", got, completed.Raw)
	}
	if got := completed.Get("response.usage.output_tokens_details.reasoning_tokens").Int(); got != 2 {
		t.Fatalf("stream reasoning_tokens = %d, want 2; payload=%s", got, completed.Raw)
	}
	if got := completed.Get("response.usage.total_tokens").Int(); got != 22048 {
		t.Fatalf("stream total_tokens = %d, want 22048; payload=%s", got, completed.Raw)
	}

	rawNonStream := []byte(strings.Join([]string{
		string(chunks[0]),
		string(chunks[1]),
		string(chunks[2]),
	}, "\n"))
	nonStream := TranslateNonStream(context.Background(), FormatAnthropic, FormatOpenAIResponse, "claude-test", nil, nil, rawNonStream, nil)
	for _, path := range []string{
		"usage.input_tokens",
		"usage.input_tokens_details.cached_tokens",
		"usage.input_tokens_details.cache_write_tokens",
		"usage.output_tokens",
		"usage.total_tokens",
		"usage.output_tokens_details.reasoning_tokens",
	} {
		if got, want := gjson.GetBytes(nonStream, path).Raw, completed.Get("response."+path).Raw; got != want {
			t.Fatalf("%s stream/non-stream mismatch: got %s want %s\nnon-stream=%s\nstream=%s", path, got, want, string(nonStream), completed.Raw)
		}
	}

	var oracleParam any
	var oracleCompleted gjson.Result
	for _, chunk := range chunks {
		for _, out := range claudeResponses.ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &oracleParam) {
			event, data := parseFidelitySSE(out)
			if event == "response.completed" {
				oracleCompleted = data
			}
		}
	}
	if got, want := completed.Get("response.usage.input_tokens").Int(), oracleCompleted.Get("response.usage.input_tokens").Int(); got != want {
		t.Fatalf("oracle input_tokens mismatch: got %d want %d", got, want)
	}
	if got, want := completed.Get("response.usage.input_tokens_details.cached_tokens").Int(), oracleCompleted.Get("response.usage.input_tokens_details.cached_tokens").Int(); got != want {
		t.Fatalf("oracle cached_tokens mismatch: got %d want %d", got, want)
	}
}

func TestUsageParityCodexCacheWriteToOpenAIChatExplicitZero(t *testing.T) {
	raw := []byte(`{"id":"resp_1","model":"codex-upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":5}}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAI, "codex-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens"); !got.Exists() || got.Int() != 0 {
		t.Fatalf("cached_creation_tokens explicit zero missing: %s", string(out))
	}

	var oracleParam any
	oracleEvent := []byte(`data: {"type":"response.completed","response":` + string(raw) + `}`)
	oracleOut := codexOpenAI.ConvertCodexResponseToOpenAI(context.Background(), "codex-test", nil, nil, oracleEvent, &oracleParam)
	if len(oracleOut) == 0 {
		t.Fatalf("codex oracle produced no output")
	}
	if got := gjson.GetBytes(oracleOut[0], "usage.prompt_tokens_details.cached_creation_tokens"); !got.Exists() || got.Int() != 0 {
		t.Fatalf("oracle cached_creation_tokens explicit zero missing: %s", string(oracleOut[0]))
	}
}

func TestUsageParityCodexCacheWriteMissingStaysAbsent(t *testing.T) {
	raw := []byte(`{"id":"resp_1","model":"codex-upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":5}}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAI, "codex-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens"); got.Exists() {
		t.Fatalf("cached_creation_tokens should be absent: %s", string(out))
	}
}

func TestGeminiUsageCachedTokensResponsesDefault(t *testing.T) {
	ctx := context.Background()
	base := `{"responseId":"gem_1","modelVersion":"gemini-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]`
	cases := []struct {
		name       string
		raw        []byte
		wantExists bool
		want       int64
	}{
		{
			name:       "missing cached",
			raw:        []byte(base + `,"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15}}`),
			wantExists: true,
			want:       0,
		},
		{
			name:       "explicit zero",
			raw:        []byte(base + `,"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":0,"totalTokenCount":12}}`),
			wantExists: true,
			want:       0,
		},
		{
			name:       "explicit nonzero",
			raw:        []byte(base + `,"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"totalTokenCount":12}}`),
			wantExists: true,
			want:       4,
		},
		{
			name:       "missing usage",
			raw:        []byte(base + `}`),
			wantExists: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := TranslateNonStream(ctx, FormatGemini, FormatOpenAIResponse, "runtime-model", nil, nil, tt.raw, nil)
			cached := gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens")
			if cached.Exists() != tt.wantExists {
				t.Fatalf("cached_tokens exists=%v, want %v; payload=%s", cached.Exists(), tt.wantExists, string(out))
			}
			if tt.wantExists && cached.Int() != tt.want {
				t.Fatalf("cached_tokens = %d, want %d; payload=%s", cached.Int(), tt.want, string(out))
			}
			if !tt.wantExists && gjson.GetBytes(out, "usage").Exists() {
				t.Fatalf("usage should be absent when usageMetadata is absent: %s", string(out))
			}
		})
	}
}

func TestGeminiUsageCachedTokensResponsesStreamDefault(t *testing.T) {
	base := `{"responseId":"gem_stream","modelVersion":"gemini-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]`
	cases := []struct {
		name       string
		chunk      []byte
		wantExists bool
		want       int64
	}{
		{
			name:       "missing cached",
			chunk:      []byte(`data: ` + base + `,"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15}}`),
			wantExists: true,
			want:       0,
		},
		{
			name:       "explicit zero",
			chunk:      []byte(`data: ` + base + `,"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":0,"totalTokenCount":12}}`),
			wantExists: true,
			want:       0,
		},
		{
			name:       "explicit nonzero",
			chunk:      []byte(`data: ` + base + `,"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"totalTokenCount":12}}`),
			wantExists: true,
			want:       4,
		},
		{
			name:       "missing usage",
			chunk:      []byte(`data: ` + base + `}`),
			wantExists: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			var completed gjson.Result
			for _, chunk := range [][]byte{tt.chunk, []byte(`data: [DONE]`)} {
				for _, out := range TranslateStream(context.Background(), FormatGemini, FormatOpenAIResponse, "runtime-model", nil, nil, chunk, &param) {
					event, data := parseFidelitySSE(out)
					if event == "response.completed" {
						completed = data
					}
				}
			}
			if !completed.Exists() {
				t.Fatalf("response.completed missing")
			}
			cached := completed.Get("response.usage.input_tokens_details.cached_tokens")
			if cached.Exists() != tt.wantExists {
				t.Fatalf("stream cached_tokens exists=%v, want %v; payload=%s", cached.Exists(), tt.wantExists, completed.Raw)
			}
			if tt.wantExists && cached.Int() != tt.want {
				t.Fatalf("stream cached_tokens = %d, want %d; payload=%s", cached.Int(), tt.want, completed.Raw)
			}
			if !tt.wantExists && completed.Get("response.usage").Exists() {
				t.Fatalf("stream usage should be absent when usageMetadata is absent: %s", completed.Raw)
			}
		})
	}
}

func TestUsageCachedTokensResponsesDefaultNonGeminiStaysAbsent(t *testing.T) {
	raw := []byte(`{"id":"chatcmpl_1","created":1,"model":"openai-upstream","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	out := TranslateNonStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "runtime-model", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens"); got.Exists() {
		t.Fatalf("non-Gemini cached_tokens should be absent: %s", string(out))
	}

	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl_1","created":1,"model":"openai-upstream","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`),
		[]byte(`data: {"id":"chatcmpl_1","created":1,"model":"openai-upstream","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`),
		[]byte(`data: [DONE]`),
	}
	var param any
	var completed gjson.Result
	for _, chunk := range chunks {
		for _, out := range TranslateStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "runtime-model", nil, nil, chunk, &param) {
			event, data := parseFidelitySSE(out)
			if event == "response.completed" {
				completed = data
			}
		}
	}
	if !completed.Exists() {
		t.Fatalf("non-Gemini response.completed missing")
	}
	if got := completed.Get("response.usage.input_tokens_details.cached_tokens"); got.Exists() {
		t.Fatalf("non-Gemini stream cached_tokens should be absent: %s", completed.Raw)
	}
}

func TestUsageProjectionMatrix(t *testing.T) {
	ctx := context.Background()
	claudeRaw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-upstream","usage":{"input_tokens":13,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":4,"thinking_tokens":2}}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))
	cases := []struct {
		name     string
		target   Format
		oracle   []byte
		expected []usageFieldExpectation
	}{
		{
			name:   "claude to openai chat",
			target: FormatOpenAI,
			oracle: claudeOpenAI.ConvertClaudeResponseToOpenAINonStream(ctx, "runtime-model", nil, nil, claudeRaw, nil),
			expected: []usageFieldExpectation{
				{path: "usage.prompt_tokens", want: 22044},
				{path: "usage.completion_tokens", want: 4},
				{path: "usage.total_tokens", want: 22048},
				{path: "usage.prompt_tokens_details.cached_tokens", want: 22000},
				{path: "usage.prompt_tokens_details.cached_creation_tokens", want: 31},
				// Upstream Claude->OpenAI chat does not map usage.thinking_tokens;
				// oagmsg pins it to OpenAI's completion_tokens_details.
				{path: "usage.completion_tokens_details.reasoning_tokens", want: 2, oracleAbsent: true},
			},
		},
		{
			name:   "claude to responses",
			target: FormatOpenAIResponse,
			oracle: claudeResponses.ConvertClaudeResponseToOpenAIResponsesNonStream(ctx, "runtime-model", nil, nil, claudeRaw, nil),
			expected: []usageFieldExpectation{
				{path: "usage.input_tokens", want: 22044},
				{path: "usage.output_tokens", want: 4},
				{path: "usage.total_tokens", want: 22048},
				{path: "usage.input_tokens_details.cached_tokens", want: 22000},
				// Upstream preserves Claude cache read but has no Responses cache_write
				// projection for cache_creation_input_tokens; oagmsg pins it.
				{path: "usage.input_tokens_details.cache_write_tokens", want: 31, oracleAbsent: true},
				// Upstream estimates reasoning only from reasoning output text, not
				// usage.thinking_tokens; oagmsg preserves the explicit counter.
				{path: "usage.output_tokens_details.reasoning_tokens", want: 2, oracleAbsent: true},
			},
		},
		{
			name:   "claude to interactions",
			target: FormatInteractions,
			oracle: claudeInteractions.ConvertClaudeResponseToInteractionsNonStream(ctx, "runtime-model", nil, nil, claudeRaw, nil),
			expected: []usageFieldExpectation{
				{path: "usage.input_tokens", want: 13},
				{path: "usage.output_tokens", want: 4},
				{path: "usage.total_tokens", want: 17},
				{path: "usage.cached_tokens", want: 22031},
				{path: "usage.total_cached_tokens", want: 22031},
				{path: "usage.reasoning_tokens", want: 2},
				{path: "usage.total_thought_tokens", want: 2},
			},
		},
		{
			name:   "claude to gemini",
			target: FormatGemini,
			oracle: claudeGemini.ConvertClaudeResponseToGeminiNonStream(ctx, "runtime-model", nil, nil, claudeRaw, nil),
			expected: []usageFieldExpectation{
				// Upstream Claude->Gemini non-stream processes split SSE one event at a
				// time and misses message_start usage; oagmsg merges all source presence.
				{path: "usageMetadata.promptTokenCount", want: 13, oracleDiverges: true},
				{path: "usageMetadata.candidatesTokenCount", want: 4},
				{path: "usageMetadata.totalTokenCount", want: 17, oracleDiverges: true},
				{path: "usageMetadata.cachedContentTokenCount", want: 22031, oracleAbsent: true},
				{path: "usageMetadata.thoughtsTokenCount", want: 2},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := TranslateNonStream(ctx, FormatAnthropic, tt.target, "runtime-model", nil, nil, claudeRaw, nil)
			if len(tt.oracle) == 0 || !gjson.ValidBytes(tt.oracle) {
				t.Fatalf("oracle output invalid for %s: %s", tt.name, string(tt.oracle))
			}
			assertUsageFieldExpectations(t, out, tt.oracle, tt.expected)
		})
	}
}

func TestUsagePresenceProjectionBranches(t *testing.T) {
	ctx := context.Background()

	openAIChatRaw := []byte(`{"id":"chatcmpl_1","created":1,"model":"openai-upstream","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":5,"cached_creation_tokens":7},"completion_tokens_details":{"reasoning_tokens":11}}}`)
	openAIToClaude := TranslateNonStream(ctx, FormatOpenAI, FormatAnthropic, "runtime-model", nil, nil, openAIChatRaw, nil)
	assertJSONIntBytes(t, openAIToClaude, "usage.input_tokens", 10)
	assertJSONIntBytes(t, openAIToClaude, "usage.output_tokens", 2)
	assertJSONIntBytes(t, openAIToClaude, "usage.cache_read_input_tokens", 5)
	assertJSONIntBytes(t, openAIToClaude, "usage.cache_creation_input_tokens", 7)
	assertJSONIntBytes(t, openAIToClaude, "usage.thinking_tokens", 11)

	codexRaw := []byte(`{"id":"resp_1","model":"codex-upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":9},"output_tokens_details":{"reasoning_tokens":5}}}`)
	codexToClaude := TranslateNonStream(ctx, FormatCodex, FormatAnthropic, "runtime-model", nil, nil, codexRaw, nil)
	assertJSONIntBytes(t, codexToClaude, "usage.input_tokens", 100)
	assertJSONIntBytes(t, codexToClaude, "usage.output_tokens", 20)
	assertJSONIntBytes(t, codexToClaude, "usage.cache_read_input_tokens", 30)
	assertJSONIntBytes(t, codexToClaude, "usage.cache_creation_input_tokens", 9)
	assertJSONIntBytes(t, codexToClaude, "usage.thinking_tokens", 5)

	codexToOpenAI := TranslateNonStream(ctx, FormatCodex, FormatOpenAI, "runtime-model", nil, nil, codexRaw, nil)
	assertJSONIntBytes(t, codexToOpenAI, "usage.prompt_tokens_details.cached_tokens", 30)
	assertJSONIntBytes(t, codexToOpenAI, "usage.prompt_tokens_details.cached_creation_tokens", 9)
	assertJSONIntBytes(t, codexToOpenAI, "usage.completion_tokens_details.reasoning_tokens", 5)

	geminiUsageRaw := []byte(`{"responseId":"gem_1","modelVersion":"gemini-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"thoughtsTokenCount":3}}`)
	antigravityUsageRaw := []byte(`{"response":{"responseId":"gem_1","modelVersion":"gemini-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"thoughtsTokenCount":3}}}`)
	geminiToResponses := TranslateNonStream(ctx, FormatGemini, FormatOpenAIResponse, "runtime-model", nil, nil, geminiUsageRaw, nil)
	antigravityToResponses := TranslateNonStream(ctx, FormatAntigravity, FormatOpenAIResponse, "runtime-model", nil, nil, antigravityUsageRaw, nil)
	for _, payload := range [][]byte{geminiToResponses, antigravityToResponses} {
		assertJSONIntBytes(t, payload, "usage.input_tokens", 10)
		assertJSONIntBytes(t, payload, "usage.output_tokens", 2)
		assertJSONIntBytes(t, payload, "usage.total_tokens", 15)
		assertJSONIntBytes(t, payload, "usage.input_tokens_details.cached_tokens", 4)
		assertJSONIntBytes(t, payload, "usage.output_tokens_details.reasoning_tokens", 3)
	}

	explicitZeroTotalRaw := []byte(`{"id":"chatcmpl_2","created":1,"model":"openai-upstream","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":0}}`)
	explicitZeroTotal := TranslateNonStream(ctx, FormatOpenAI, FormatOpenAIResponse, "runtime-model", nil, nil, explicitZeroTotalRaw, nil)
	assertJSONIntBytes(t, explicitZeroTotal, "usage.total_tokens", 0)

	missingTotalRaw := []byte(`{"id":"chatcmpl_3","created":1,"model":"openai-upstream","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	missingTotal := TranslateNonStream(ctx, FormatOpenAI, FormatOpenAIResponse, "runtime-model", nil, nil, missingTotalRaw, nil)
	assertJSONIntBytes(t, missingTotal, "usage.total_tokens", 3)
}

func TestUsageProjectionGeminiDerivedTotalIncludesReasoning(t *testing.T) {
	raw := []byte(`{"responseId":"gem_1","modelVersion":"gemini-upstream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3}}`)
	out := TranslateNonStream(context.Background(), FormatGemini, FormatOpenAIResponse, "runtime-model", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 15 {
		t.Fatalf("total_tokens = %d, want 15; payload=%s", got, string(out))
	}
	oracle := geminiResponses.ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "runtime-model", nil, nil, raw, nil)
	if got := gjson.GetBytes(oracle, "usage.output_tokens_details.reasoning_tokens").Int(); got != 3 {
		t.Fatalf("oracle reasoning_tokens = %d, want 3; payload=%s", got, string(oracle))
	}
	// Upstream currently omits total_tokens when Gemini omits totalTokenCount;
	// oagmsg pins the intended fallback total so Responses clients still see it.
	if got := gjson.GetBytes(oracle, "usage.total_tokens"); got.Exists() {
		t.Fatalf("oracle unexpectedly emitted total_tokens=%s; payload=%s", got.Raw, string(oracle))
	}
}

func assertSSEModel(t *testing.T, chunks [][]byte, eventName, want string) {
	t.Helper()
	for _, chunk := range chunks {
		event, data := parseFidelitySSE(chunk)
		if event == eventName {
			if got := data.Get("response.model").String(); got != want {
				t.Fatalf("%s model = %q, want %q; chunk=%s", eventName, got, want, string(chunk))
			}
			return
		}
	}
	t.Fatalf("missing %s in chunks: %q", eventName, chunks)
}

type usageFieldExpectation struct {
	path           string
	want           int64
	oracleAbsent   bool
	oracleDiverges bool
}

func assertUsageFieldExpectations(t *testing.T, out, oracle []byte, expected []usageFieldExpectation) {
	t.Helper()
	for _, field := range expected {
		assertJSONIntBytes(t, out, field.path, field.want)
		oracleValue := gjson.GetBytes(oracle, field.path)
		switch {
		case field.oracleAbsent:
			if oracleValue.Exists() {
				t.Fatalf("oracle %s unexpectedly exists with %s; oracle=%s", field.path, oracleValue.Raw, string(oracle))
			}
		case field.oracleDiverges:
			if !oracleValue.Exists() {
				t.Fatalf("oracle %s missing but divergence requires a present non-matching value; oracle=%s", field.path, string(oracle))
			}
			if oracleValue.Int() == field.want {
				t.Fatalf("oracle %s unexpectedly matches pinned value %d; oracle=%s", field.path, field.want, string(oracle))
			}
		default:
			if !oracleValue.Exists() {
				t.Fatalf("oracle %s missing; oracle=%s", field.path, string(oracle))
			}
			if got := oracleValue.Int(); got != field.want {
				t.Fatalf("oracle %s = %d, want %d; oracle=%s", field.path, got, field.want, string(oracle))
			}
		}
	}
}

func assertJSONIntBytes(t *testing.T, payload []byte, path string, want int64) {
	t.Helper()
	value := gjson.GetBytes(payload, path)
	if !value.Exists() {
		t.Fatalf("%s missing; payload=%s", path, string(payload))
	}
	if got := value.Int(); got != want {
		t.Fatalf("%s = %d, want %d; payload=%s", path, got, want, string(payload))
	}
}

func parseFidelitySSE(chunk []byte) (string, gjson.Result) {
	event := ""
	data := ""
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" && strings.HasPrefix(string(chunk), "data: ") {
		data = strings.TrimSpace(strings.TrimPrefix(string(chunk), "data: "))
	}
	parsed := gjson.Parse(data)
	if event == "" {
		event = parsed.Get("type").String()
	}
	return event, parsed
}
