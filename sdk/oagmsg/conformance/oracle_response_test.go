package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	agclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/claude"
	claudeResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	codexOpenAI "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	codexResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/responses"
	geminiResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	openAIResponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

type t82ResponseMode string

const (
	t82ResponseModeStream    t82ResponseMode = "stream"
	t82ResponseModeNonStream t82ResponseMode = "non-stream"
	t82ResponseManifestCount                 = 19
)

type t82ResponseManifestRow struct {
	gap     string
	source  oagmsg.Format
	target  oagmsg.Format
	mode    t82ResponseMode
	fixture string
}

type t82ResponseFixture struct {
	model                 string
	original              []byte
	translated            []byte
	raw                   []byte
	chunks                [][]byte
	paths                 []string
	streamDataPaths       []string
	terminalEvents        []string
	normalizeGeneratedIDs bool
}

var t82OracleResponseManifest = []t82ResponseManifestRow{
	{"G1 Codex web-search lifecycle, generated IDs, repeated empty metadata", oagmsg.FormatCodex, oagmsg.FormatAnthropic, t82ResponseModeStream, "g1_codex_web_search_stream_generated_repeated"},
	{"G1 Codex web-search results/citations and repeated populated metadata", oagmsg.FormatCodex, oagmsg.FormatAnthropic, t82ResponseModeNonStream, "g1_codex_web_search_nonstream_results_repeated"},
	{"G1 Antigravity grounding lifecycle/results/citations/annotations", oagmsg.FormatAntigravity, oagmsg.FormatAnthropic, t82ResponseModeStream, "g1_antigravity_grounding_stream_citations"},
	{"G1 Antigravity grounding missing/empty/repeated metadata", oagmsg.FormatAntigravity, oagmsg.FormatAnthropic, t82ResponseModeNonStream, "g1_antigravity_grounding_nonstream_metadata_edges"},
	{"G6 Gemini signature carrier thought/text association", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeStream, "g6_gemini_signature_stream_thought_text_order"},
	{"G6 Gemini signature carrier repeated/missing/empty signatures", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeNonStream, "g6_gemini_signature_nonstream_repeated_missing_empty"},
	{"G6 Gemini signature carrier tool winner and detached signature order", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeStream, "g6_gemini_signature_stream_tool_detached"},
	{"G10 explicit original request model wins", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeNonStream, "g10_model_original_explicit"},
	{"G10 original nested explicit blank blocks translated fallback", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeNonStream, "g10_model_translated_fallback"},
	{"G10 provider model/version fallback when request model absent", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeNonStream, "g10_model_provider_version_fallback"},
	{"G10 runtime requested model fallback when model fields are empty/missing", oagmsg.FormatOpenAI, oagmsg.FormatOpenAIResponse, t82ResponseModeStream, "g10_model_requested_stream_fallback"},
	{"G10 Codex same-family completed event model override", oagmsg.FormatCodex, oagmsg.FormatOpenAIResponse, t82ResponseModeStream, "g10_model_codex_completed_event"},
	{"G11 Claude detailed usage terminal stream placement/order", oagmsg.FormatAnthropic, oagmsg.FormatOpenAIResponse, t82ResponseModeStream, "g11_usage_claude_stream_terminal"},
	{"G11 Claude detailed usage non-stream aggregation", oagmsg.FormatAnthropic, oagmsg.FormatOpenAIResponse, t82ResponseModeNonStream, "g11_usage_claude_nonstream_cache_reasoning"},
	{"G11 Codex detailed usage explicit zero cache write", oagmsg.FormatCodex, oagmsg.FormatOpenAI, t82ResponseModeNonStream, "g11_usage_codex_chat_explicit_zero"},
	{"G11 Codex detailed usage missing cache write stays absent", oagmsg.FormatCodex, oagmsg.FormatOpenAI, t82ResponseModeNonStream, "g11_usage_codex_chat_missing_cache_write"},
	{"G11 Gemini detailed usage cache/read thought totals", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeNonStream, "g11_usage_gemini_nonstream_detailed"},
	{"G11 Gemini missing cache read pins explicit zero", oagmsg.FormatGemini, oagmsg.FormatOpenAIResponse, t82ResponseModeNonStream, "g11_usage_gemini_nonstream_missing_cache_zero"},
	{"G11 Codex Responses usage terminal stream placement/order", oagmsg.FormatCodex, oagmsg.FormatOpenAIResponse, t82ResponseModeStream, "g11_usage_codex_responses_stream_terminal"},
}

var t82OracleResponseFixtures = map[string]t82ResponseFixture{
	"g1_codex_web_search_stream_generated_repeated": {
		model:                 "claude-sonnet-4-20250514",
		original:              t82ClaudeWebSearchOriginal(),
		normalizeGeneratedIDs: true,
		streamDataPaths:       []string{"type", "index", "content_block", "delta", "message.id", "message.model", "message.role"},
		terminalEvents:        []string{"content_block_stop", "message_delta", "message_stop"},
		chunks: [][]byte{
			[]byte(`data: {"type":"response.created","response":{"id":"resp_g1","model":"gpt-5.5","created_at":1786014000}}`),
			[]byte(`data: {"type":"response.output_item.added","item":{"type":"web_search_call","status":"in_progress"}}`),
			[]byte(`data: {"type":"response.web_search_call.searching"}`),
			[]byte(`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search"}}}`),
			[]byte(`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"weather"},"results":[{"title":"Weather","url":"https://example.com/weather"},{"title":"","url":"https://example.com/untitled"},{"title":"Missing URL"}]}}`),
			[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},"output_index":1}`),
			[]byte(`data: {"type":"response.completed","response":{"id":"resp_g1","status":"completed","stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`),
		},
	},
	"g1_codex_web_search_nonstream_results_repeated": {
		model:    "claude-sonnet-4-20250514",
		original: t82ClaudeWebSearchOriginal(),
		paths:    []string{"id", "content", "stop_reason", "usage"},
		raw: []byte(`{"type":"response.completed","response":{"id":"resp_g1_ns","model":"gpt-5.5","status":"completed","stop_reason":"stop","usage":{"input_tokens":5,"output_tokens":4,"total_tokens":9},"output":[` +
			`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"open_page"}},` +
			`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"weather"},"results":[{"title":"Weather","url":"https://example.com/weather"},{"title":"","url":"https://example.com/untitled"},{"title":"Missing URL"}]},` +
			`{"type":"web_search_call","status":"completed","action":{"type":"search","query":"missing id"}},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer with annotation-like search result"}]}` +
			`]}}`),
	},
	"g1_antigravity_grounding_stream_citations": {
		model:                 "claude-sonnet-4-20250514",
		original:              t82ClaudeWebSearchOriginal(),
		translated:            t82AntigravityGoogleSearchRequest(),
		normalizeGeneratedIDs: true,
		streamDataPaths:       []string{"type", "index", "content_block", "delta", "message.id", "message.model", "message.role"},
		terminalEvents:        []string{"content_block_stop", "message_delta", "message_stop"},
		chunks: [][]byte{
			t82AntigravityGroundingResponse("resp_ag_stream", "Alpha beta tail", `[{"segment":{"startIndex":0,"endIndex":5,"text":"Alpha"},"groundingChunkIndices":[0]},{"segment":{"startIndex":6,"endIndex":10,"text":"beta"},"groundingChunkIndices":[1,0]}]`, `[{"web":{"uri":"https://example.com/a","title":"A"}},{"web":{"uri":"https://example.com/b","title":""}}]`, `{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":0,"thoughtsTokenCount":2,"totalTokenCount":15}`),
			[]byte(`[DONE]`),
		},
	},
	"g1_antigravity_grounding_nonstream_metadata_edges": {
		model:                 "claude-sonnet-4-20250514",
		original:              t82ClaudeWebSearchOriginal(),
		translated:            t82AntigravityGoogleSearchRequest(),
		normalizeGeneratedIDs: true,
		paths:                 []string{"id", "model", "content", "stop_reason", "usage"},
		raw: t82AntigravityGroundingResponse("resp_ag_ns", "abcdef tail", `[`+
			`{"segment":{"startIndex":0,"endIndex":3,"text":"abc"},"groundingChunkIndices":[1,0]},`+
			`{"segment":{"startIndex":3,"endIndex":6,"text":"def"},"groundingChunkIndices":[99]},`+
			`{},`+
			`{"segment":{"startIndex":0,"endIndex":3,"text":"abc"},"groundingChunkIndices":[0]}`+
			`]`, `[`+
			`{"web":{"uri":"","title":""}},`+
			`{"web":{"uri":"https://example.com/a","title":"A"}},`+
			`{"web":{"uri":"https://example.com/a","title":"Duplicate"}},`+
			`{"web":{"uri":"https://example.com/no-title"}}`+
			`]`, `{"promptTokenCount":10,"cachedContentTokenCount":0,"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":12}`),
	},
	"g6_gemini_signature_stream_thought_text_order": {
		model:           "gemini-test",
		streamDataPaths: []string{"type", "output_index", "item", "response.output", "delta"},
		terminalEvents:  []string{"response.completed"},
		chunks: [][]byte{
			[]byte(`data: {"responseId":"resp_g6_stream","modelVersion":"gemini-2.5-pro","createTime":"2026-08-06T11:31:19Z","candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true,"thoughtSignature":"sig-thought-1"},{"text":"answer","thoughtSignature":"sig-visible-1"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":9}}`),
		},
	},
	"g6_gemini_signature_nonstream_repeated_missing_empty": {
		model: "gemini-test",
		paths: []string{"id", "output"},
		raw: []byte(`{"responseId":"resp_g6_ns","modelVersion":"gemini-2.5-pro","createTime":"2026-08-06T11:31:19Z","candidates":[{"content":{"role":"model","parts":[` +
			`{"text":"a"},` +
			`{"text":"b","thoughtSignature":"sig-repeat"},` +
			`{"text":"c","thoughtSignature":"sig-repeat"},` +
			`{"text":"","thoughtSignature":"sig-empty"},` +
			`{"text":"d"}` +
			`]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4,"thoughtsTokenCount":1,"totalTokenCount":10}}`),
	},
	"g6_gemini_signature_stream_tool_detached": {
		model:                 "gemini-test",
		normalizeGeneratedIDs: true,
		streamDataPaths:       []string{"type", "output_index", "item", "response.output", "delta"},
		terminalEvents:        []string{"response.completed"},
		chunks: [][]byte{
			[]byte(`data: {"responseId":"resp_g6_tool","modelVersion":"gemini-2.5-pro","createTime":"2026-08-06T11:31:19Z","candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-tool-leading"},{"functionCall":{"id":"native-call","name":"run_command","args":{"command":"true"}},"thoughtSignature":"sig-tool-call"},{"text":"after"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":1,"totalTokenCount":8}}`),
		},
	},
	"g10_model_original_explicit": {
		model:      "runtime-model",
		original:   []byte(`{"model":"original-explicit","request":{"model":"original-nested"}}`),
		translated: []byte(`{"model":"translated-model"}`),
		paths:      []string{"model"},
		raw:        t82GeminiModelResponse("resp_g10_original", "provider-version", "ok", `{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}`),
	},
	"g10_model_translated_fallback": {
		model:      "runtime-model",
		original:   []byte(`{"model":false,"request":{"model":"   "}}`),
		translated: []byte(`{"request":{"model":"translated-fallback"}}`),
		paths:      []string{"model"},
		raw:        t82GeminiModelResponse("resp_g10_translated", "provider-version", "ok", `{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}`),
	},
	"g10_model_provider_version_fallback": {
		model: "runtime-model",
		paths: []string{"model"},
		raw:   t82GeminiModelResponse("resp_g10_provider", "provider-version", "ok", `{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}`),
	},
	"g10_model_requested_stream_fallback": {
		model:           "requested-runtime",
		original:        []byte(`{"model":"   "}`),
		translated:      []byte(`{"model":false}`),
		streamDataPaths: []string{"type", "response.model", "response.status"},
		terminalEvents:  []string{"response.completed"},
		chunks: [][]byte{
			[]byte(`data: {"id":"chatcmpl_g10","created":1786014000,"model":"","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`),
			[]byte(`data: {"id":"chatcmpl_g10","created":1786014000,"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`),
			[]byte(`data: {"id":"chatcmpl_g10","created":1786014000,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
			[]byte(`data: [DONE]`),
		},
	},
	"g10_model_codex_completed_event": {
		model:           "runtime-codex",
		original:        []byte(`{"model":"codex-original"}`),
		streamDataPaths: []string{"type", "response.model", "response.status", "response.output"},
		terminalEvents:  []string{"response.completed"},
		chunks: [][]byte{
			[]byte(`data: {"type":"response.completed","response":{"id":"resp_g10_codex","model":"upstream-codex","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`),
		},
	},
	"g11_usage_claude_stream_terminal": {
		model:           "claude-test",
		streamDataPaths: []string{"type", "response.usage"},
		terminalEvents:  []string{"response.completed"},
		chunks: [][]byte{
			[]byte(`data: {"type":"message_start","message":{"id":"msg_g11","model":"claude-upstream","usage":{"input_tokens":13,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}}}`),
			[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"plan","signature":"sig-claude"}}`),
			[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" more"}}`),
			[]byte(`data: {"type":"content_block_stop","index":0}`),
			[]byte(`data: {"type":"message_delta","usage":{"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31,"thinking_tokens":2}}`),
			[]byte(`data: {"type":"message_stop"}`),
		},
	},
	"g11_usage_claude_nonstream_cache_reasoning": {
		model: "claude-test",
		paths: []string{"usage"},
		raw: []byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_g11_ns","model":"claude-upstream","usage":{"input_tokens":13,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}}`,
			`data: {"type":"message_delta","usage":{"output_tokens":4,"thinking_tokens":2}}`,
			`data: {"type":"message_stop"}`,
		}, "\n")),
	},
	"g11_usage_codex_chat_explicit_zero": {
		model: "codex-test",
		paths: []string{"usage"},
		raw:   []byte(`{"type":"response.completed","response":{"id":"resp_g11_zero","model":"codex-upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":5}}}}`),
	},
	"g11_usage_codex_chat_missing_cache_write": {
		model: "codex-test",
		paths: []string{"usage"},
		raw:   []byte(`{"type":"response.completed","response":{"id":"resp_g11_missing","model":"codex-upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":5}}}}`),
	},
	"g11_usage_gemini_nonstream_detailed": {
		model: "gemini-test",
		paths: []string{"usage"},
		raw:   t82GeminiModelResponse("resp_g11_gemini", "gemini-upstream", "ok", `{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"thoughtsTokenCount":3,"totalTokenCount":15}`),
	},
	"g11_usage_gemini_nonstream_missing_cache_zero": {
		model: "gemini-test",
		paths: []string{"usage"},
		raw:   t82GeminiModelResponse("resp_g11_gemini_missing_cache", "gemini-upstream", "ok", `{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15}`),
	},
	"g11_usage_codex_responses_stream_terminal": {
		model:           "codex-test",
		streamDataPaths: []string{"type", "response.usage"},
		terminalEvents:  []string{"response.completed"},
		chunks: [][]byte{
			[]byte(`data: {"type":"response.created","response":{"id":"resp_g11_stream","model":"codex-upstream","created_at":1786014000}}`),
			[]byte(`data: {"type":"response.output_text.delta","delta":"ok"}`),
			[]byte(`data: {"type":"response.completed","response":{"id":"resp_g11_stream","model":"codex-upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":9},"output_tokens_details":{"reasoning_tokens":5}}}}`),
		},
	},
}

func TestOracleResponseManifestCoversFixtures(t *testing.T) {
	if len(t82OracleResponseManifest) != t82ResponseManifestCount {
		t.Fatalf("manifest count = %d, want %d", len(t82OracleResponseManifest), t82ResponseManifestCount)
	}
	seenRows := make(map[string]bool, len(t82OracleResponseManifest))
	seenFixtures := make(map[string]bool, len(t82OracleResponseManifest))
	for _, row := range t82OracleResponseManifest {
		if row.gap == "" || row.source == "" || row.target == "" || row.mode == "" || row.fixture == "" {
			t.Fatalf("manifest row has empty semantic field: %#v", row)
		}
		key := fmt.Sprintf("%s/%s/%s/%s", row.source, row.target, row.mode, row.fixture)
		if seenRows[key] {
			t.Fatalf("duplicate manifest row %s", key)
		}
		seenRows[key] = true
		fixture, ok := t82OracleResponseFixtures[row.fixture]
		if !ok {
			t.Fatalf("manifest row %q points to missing fixture %q", row.gap, row.fixture)
		}
		if row.mode == t82ResponseModeStream && len(fixture.chunks) == 0 {
			t.Fatalf("stream fixture %q has no chunks", row.fixture)
		}
		if row.mode == t82ResponseModeNonStream && len(fixture.raw) == 0 {
			t.Fatalf("non-stream fixture %q has no raw payload", row.fixture)
		}
		seenFixtures[row.fixture] = true
	}
	var unused []string
	for name := range t82OracleResponseFixtures {
		if !seenFixtures[name] {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Fatalf("fixtures missing manifest rows: %s", strings.Join(unused, ", "))
	}
}

func TestOracleResponseFidelity(t *testing.T) {
	for _, row := range t82OracleResponseManifest {
		row := row
		fixture := t82OracleResponseFixtures[row.fixture]
		t.Run(row.fixture, func(t *testing.T) {
			switch row.mode {
			case t82ResponseModeNonStream:
				oracle := t82OracleResponseNonStream(t, row, fixture)
				got := oagmsg.TranslateNonStream(context.Background(), row.source, row.target, fixture.model, fixture.original, fixture.translated, fixture.raw, nil)
				t82AssertProjectedJSONEqual(t, row, fixture, got, oracle)
			case t82ResponseModeStream:
				oracle := t82OracleResponseStream(t, row, fixture)
				got := t82OAGResponseStream(row, fixture)
				t82AssertStreamEqual(t, row, fixture, got, oracle)
			default:
				t.Fatalf("unsupported response mode %q", row.mode)
			}
		})
	}
}

func t82OracleResponseNonStream(t *testing.T, row t82ResponseManifestRow, fixture t82ResponseFixture) []byte {
	t.Helper()
	ctx := context.Background()
	switch {
	case row.source == oagmsg.FormatCodex && row.target == oagmsg.FormatAnthropic:
		return codexclaude.ConvertCodexResponseToClaudeNonStream(ctx, fixture.model, fixture.original, fixture.translated, fixture.raw, nil)
	case row.source == oagmsg.FormatAntigravity && row.target == oagmsg.FormatAnthropic:
		return agclaude.ConvertAntigravityResponseToClaudeNonStream(ctx, fixture.model, fixture.original, fixture.translated, fixture.raw, nil)
	case row.source == oagmsg.FormatGemini && row.target == oagmsg.FormatOpenAIResponse:
		return geminiResponses.ConvertGeminiResponseToOpenAIResponsesNonStream(ctx, fixture.model, fixture.original, fixture.translated, fixture.raw, nil)
	case row.source == oagmsg.FormatAnthropic && row.target == oagmsg.FormatOpenAIResponse:
		return claudeResponses.ConvertClaudeResponseToOpenAIResponsesNonStream(ctx, fixture.model, fixture.original, fixture.translated, fixture.raw, nil)
	case row.source == oagmsg.FormatCodex && row.target == oagmsg.FormatOpenAI:
		return codexOpenAI.ConvertCodexResponseToOpenAINonStream(ctx, fixture.model, fixture.original, fixture.translated, fixture.raw, nil)
	case row.source == oagmsg.FormatCodex && row.target == oagmsg.FormatOpenAIResponse:
		return codexResponses.ConvertCodexResponseToOpenAIResponsesNonStream(ctx, fixture.model, fixture.original, fixture.translated, fixture.raw, nil)
	default:
		t.Fatalf("missing non-stream oracle for %s -> %s", row.source, row.target)
		return nil
	}
}

func t82OracleResponseStream(t *testing.T, row t82ResponseManifestRow, fixture t82ResponseFixture) [][]byte {
	t.Helper()
	ctx := context.Background()
	var param any
	var out [][]byte
	for _, chunk := range fixture.chunks {
		switch {
		case row.source == oagmsg.FormatCodex && row.target == oagmsg.FormatAnthropic:
			out = append(out, codexclaude.ConvertCodexResponseToClaude(ctx, fixture.model, fixture.original, fixture.translated, chunk, &param)...)
		case row.source == oagmsg.FormatAntigravity && row.target == oagmsg.FormatAnthropic:
			out = append(out, agclaude.ConvertAntigravityResponseToClaude(ctx, fixture.model, fixture.original, fixture.translated, chunk, &param)...)
		case row.source == oagmsg.FormatGemini && row.target == oagmsg.FormatOpenAIResponse:
			out = append(out, geminiResponses.ConvertGeminiResponseToOpenAIResponses(ctx, fixture.model, fixture.original, fixture.translated, chunk, &param)...)
		case row.source == oagmsg.FormatOpenAI && row.target == oagmsg.FormatOpenAIResponse:
			out = append(out, openAIResponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, fixture.model, fixture.original, fixture.translated, chunk, &param)...)
		case row.source == oagmsg.FormatAnthropic && row.target == oagmsg.FormatOpenAIResponse:
			out = append(out, claudeResponses.ConvertClaudeResponseToOpenAIResponses(ctx, fixture.model, fixture.original, fixture.translated, chunk, &param)...)
		case row.source == oagmsg.FormatCodex && row.target == oagmsg.FormatOpenAIResponse:
			out = append(out, codexResponses.ConvertCodexResponseToOpenAIResponses(ctx, fixture.model, fixture.original, fixture.translated, chunk, &param)...)
		default:
			t.Fatalf("missing stream oracle for %s -> %s", row.source, row.target)
		}
	}
	return out
}

func t82OAGResponseStream(row t82ResponseManifestRow, fixture t82ResponseFixture) [][]byte {
	var param any
	var out [][]byte
	for _, chunk := range fixture.chunks {
		out = append(out, oagmsg.TranslateStream(context.Background(), row.source, row.target, fixture.model, fixture.original, fixture.translated, chunk, &param)...)
	}
	return out
}

func t82AssertProjectedJSONEqual(t *testing.T, row t82ResponseManifestRow, fixture t82ResponseFixture, got, want []byte) {
	t.Helper()
	if row.fixture == "g11_usage_claude_nonstream_cache_reasoning" {
		t82AssertG11ClaudeUsage(t, row.fixture, got, want, "")
		return
	}
	gotProjected := t82ProjectJSON(t, got, fixture.paths, fixture.normalizeGeneratedIDs)
	wantProjected := t82ProjectJSON(t, want, fixture.paths, fixture.normalizeGeneratedIDs)
	if !reflect.DeepEqual(gotProjected, wantProjected) {
		t.Fatalf("%s non-stream oracle mismatch\nsource=%s target=%s fixture=%s\ngot:  %#v\nwant: %#v\ngot raw:  %s\nwant raw: %s", row.gap, row.source, row.target, row.fixture, gotProjected, wantProjected, got, want)
	}
}

func t82AssertStreamEqual(t *testing.T, row t82ResponseManifestRow, fixture t82ResponseFixture, got, want [][]byte) {
	t.Helper()
	if strings.HasPrefix(row.fixture, "g10_") {
		t82AssertG10StreamModelOnly(t, row, fixture, got, want)
		return
	}
	if row.fixture == "g11_usage_claude_stream_terminal" {
		t82AssertG11ClaudeStreamUsage(t, row.fixture, got, want)
		return
	}
	gotEvents := t82NormalizeSSE(t, got, fixture.streamDataPaths, fixture.normalizeGeneratedIDs)
	wantEvents := t82NormalizeSSE(t, want, fixture.streamDataPaths, fixture.normalizeGeneratedIDs)
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Fatalf("%s stream oracle mismatch\nsource=%s target=%s fixture=%s\ngot:  %#v\nwant: %#v\ngot raw:  %q\nwant raw: %q", row.gap, row.source, row.target, row.fixture, gotEvents, wantEvents, got, want)
	}
	if len(fixture.terminalEvents) > 0 {
		gotTail := t82TailEvents(gotEvents, len(fixture.terminalEvents))
		if !reflect.DeepEqual(gotTail, fixture.terminalEvents) {
			t.Fatalf("%s terminal event order = %#v, want %#v; events=%#v", row.fixture, gotTail, fixture.terminalEvents, gotEvents)
		}
	}
}

func t82ProjectJSON(t *testing.T, payload []byte, paths []string, normalizeGeneratedIDs bool) map[string]any {
	t.Helper()
	normalizer := t82NewGeneratedIDNormalizer(normalizeGeneratedIDs)
	return t82ProjectJSONWithNormalizer(t, payload, paths, normalizer)
}

func t82ProjectJSONWithNormalizer(t *testing.T, payload []byte, paths []string, normalizer *t82GeneratedIDNormalizer) map[string]any {
	t.Helper()
	if len(paths) == 0 {
		var whole any
		if err := json.Unmarshal(payload, &whole); err != nil {
			t.Fatalf("invalid JSON payload: %v\n%s", err, payload)
		}
		return map[string]any{"$": normalizer.normalize(whole)}
	}
	out := make(map[string]any, len(paths))
	root := gjson.ParseBytes(payload)
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() {
			out[path] = t82MissingFieldMarker{}
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(value.Raw), &decoded); err != nil {
			t.Fatalf("invalid JSON value at %s: %v\n%s", path, err, payload)
		}
		out[path] = normalizer.normalize(decoded)
	}
	return out
}

type t82MissingFieldMarker struct{}

type t82SSEEvent struct {
	Event string
	Data  any
	Done  bool
}

func t82NormalizeSSE(t *testing.T, chunks [][]byte, dataPaths []string, normalizeGeneratedIDs bool) []t82SSEEvent {
	t.Helper()
	out := make([]t82SSEEvent, 0, len(chunks))
	normalizer := t82NewGeneratedIDNormalizer(normalizeGeneratedIDs)
	for _, chunk := range chunks {
		for _, event := range t82ParseSSEChunk(t, chunk) {
			if event.Done {
				out = append(out, event)
				continue
			}
			dataBytes, err := json.Marshal(event.Data)
			if err != nil {
				t.Fatalf("marshal SSE event data: %v", err)
			}
			if len(dataPaths) > 0 {
				event.Data = t82ProjectJSONWithNormalizer(t, dataBytes, dataPaths, normalizer)
			} else {
				event.Data = normalizer.normalize(event.Data)
			}
			out = append(out, event)
		}
	}
	return out
}

func t82ParseSSEChunk(t *testing.T, chunk []byte) []t82SSEEvent {
	t.Helper()
	chunk = bytes.TrimSpace(chunk)
	if len(chunk) == 0 {
		return nil
	}
	blocks := bytes.Split(chunk, []byte("\n\n"))
	events := make([]t82SSEEvent, 0, len(blocks))
	for _, block := range blocks {
		block = bytes.TrimSpace(block)
		if len(block) == 0 {
			continue
		}
		var eventName string
		var dataLines []string
		for _, rawLine := range bytes.Split(block, []byte("\n")) {
			line := strings.TrimSpace(string(rawLine))
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			default:
				dataLines = append(dataLines, line)
			}
		}
		dataRaw := strings.TrimSpace(strings.Join(dataLines, "\n"))
		if dataRaw == "" {
			continue
		}
		if dataRaw == "[DONE]" {
			events = append(events, t82SSEEvent{Event: "[DONE]", Done: true})
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(dataRaw), &decoded); err != nil {
			t.Fatalf("invalid SSE JSON: %v\nchunk=%q\ndata=%s", err, chunk, dataRaw)
		}
		if eventName == "" {
			root := gjson.Parse(dataRaw)
			eventName = root.Get("type").String()
			if eventName == "" {
				eventName = root.Get("event_type").String()
			}
		}
		events = append(events, t82SSEEvent{Event: eventName, Data: decoded})
	}
	return events
}

func t82TailEvents(events []t82SSEEvent, n int) []string {
	if n > len(events) {
		n = len(events)
	}
	out := make([]string, 0, n)
	for _, event := range events[len(events)-n:] {
		out = append(out, event.Event)
	}
	return out
}

var (
	t82GeneratedSearchIDPattern      = regexp.MustCompile(`^(web_search|srvtoolu)_[0-9]+$`)
	t82GeneratedNumericCallIDPattern = regexp.MustCompile(`^call_[0-9]{10,}_[0-9]+$`)
	t82GeneratedSeededCallIDPattern  = regexp.MustCompile(`^call_[A-Za-z0-9][A-Za-z0-9_]*_[0-9]+$`)
)

type t82GeneratedIDNormalizer struct {
	enabled   bool
	searchIDs map[string]string
	callIDs   map[string]string
}

func t82NewGeneratedIDNormalizer(enabled bool) *t82GeneratedIDNormalizer {
	return &t82GeneratedIDNormalizer{
		enabled:   enabled,
		searchIDs: make(map[string]string),
		callIDs:   make(map[string]string),
	}
}

func (n *t82GeneratedIDNormalizer) normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = n.normalize(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = n.normalize(child)
		}
		return out
	case string:
		if !n.enabled {
			return typed
		}
		if strings.HasPrefix(typed, "fc_") {
			callID := strings.TrimPrefix(typed, "fc_")
			if normalized, ok := n.normalizeGeneratedCallID(callID); ok {
				return "fc_" + normalized
			}
		}
		if normalized, ok := n.normalizeGeneratedCallID(typed); ok {
			return normalized
		}
		if t82GeneratedSearchIDPattern.MatchString(typed) {
			if normalized, ok := n.searchIDs[typed]; ok {
				return normalized
			}
			normalized := fmt.Sprintf("<generated-web-search-id-%d>", len(n.searchIDs))
			n.searchIDs[typed] = normalized
			return normalized
		}
		return typed
	default:
		return typed
	}
}

func (n *t82GeneratedIDNormalizer) normalizeGeneratedCallID(value string) (string, bool) {
	if !t82GeneratedNumericCallIDPattern.MatchString(value) && !t82GeneratedSeededCallIDPattern.MatchString(value) {
		return "", false
	}
	if normalized, ok := n.callIDs[value]; ok {
		return normalized, true
	}
	normalized := fmt.Sprintf("<generated-call-id-%d>", len(n.callIDs))
	n.callIDs[value] = normalized
	return normalized, true
}

func t82AssertG10StreamModelOnly(t *testing.T, row t82ResponseManifestRow, fixture t82ResponseFixture, got, want [][]byte) {
	t.Helper()
	gotTerminal := t82LastNonDoneEvent(t, got)
	wantTerminal := t82LastNonDoneEvent(t, want)
	if gotTerminal.Event != "response.completed" {
		t.Fatalf("%s terminal event = %q, want response.completed", row.fixture, gotTerminal.Event)
	}
	if wantTerminal.Event != "response.completed" {
		t.Fatalf("%s oracle terminal event = %q, want response.completed", row.fixture, wantTerminal.Event)
	}
	gotModel := t82EventDataPath(t, gotTerminal, "response.model")
	wantModel := t82EventDataPath(t, wantTerminal, "response.model")
	if row.fixture == "g10_model_requested_stream_fallback" {
		wantModel = fixture.model
	}
	gotModelValue, gotModelOK := gotModel.(string)
	wantModelValue, wantModelOK := wantModel.(string)
	if !gotModelOK || !wantModelOK || gotModelValue != wantModelValue {
		t.Fatalf("%s terminal response.model mismatch: got %#v want %#v\ngot terminal: %#v\nwant terminal: %#v", row.fixture, gotModel, wantModel, gotTerminal, wantTerminal)
	}
}

func t82AssertG11ClaudeStreamUsage(t *testing.T, fixtureName string, got, want [][]byte) {
	t.Helper()
	gotTerminal := t82LastNonDoneEvent(t, got)
	wantTerminal := t82LastNonDoneEvent(t, want)
	if gotTerminal.Event != "response.completed" {
		t.Fatalf("%s terminal event = %q, want response.completed", fixtureName, gotTerminal.Event)
	}
	if wantTerminal.Event != "response.completed" {
		t.Fatalf("%s oracle terminal event = %q, want response.completed", fixtureName, wantTerminal.Event)
	}
	for _, event := range t82FlattenSSE(t, got) {
		if event.Done || event.Event == "response.completed" {
			continue
		}
		if _, ok := t82EventDataPathOK(t, event, "response.usage"); ok {
			t.Fatalf("%s usage appeared before terminal event %q: %#v", fixtureName, event.Event, event)
		}
	}
	t82AssertG11ClaudeUsage(t, fixtureName, t82MustMarshalEventData(t, gotTerminal), t82MustMarshalEventData(t, wantTerminal), "response.")
}

func t82AssertG11ClaudeUsage(t *testing.T, fixtureName string, got, want []byte, prefix string) {
	t.Helper()
	for _, path := range []string{
		"usage.input_tokens",
		"usage.input_tokens_details.cached_tokens",
		"usage.output_tokens",
		"usage.total_tokens",
	} {
		t82AssertJSONPathEqual(t, fixtureName, got, want, prefix+path)
	}
	t82AssertJSONInt(t, fixtureName, got, prefix+"usage.input_tokens_details.cache_write_tokens", 31)
	t82AssertJSONInt(t, fixtureName, got, prefix+"usage.output_tokens_details.reasoning_tokens", 2)
}

func t82AssertJSONPathEqual(t *testing.T, fixtureName string, got, want []byte, path string) {
	t.Helper()
	gotValue := gjson.GetBytes(got, path)
	wantValue := gjson.GetBytes(want, path)
	if !gotValue.Exists() || !wantValue.Exists() || gotValue.Raw != wantValue.Raw {
		t.Fatalf("%s %s mismatch: got %s want %s\ngot:  %s\nwant: %s", fixtureName, path, gotValue.Raw, wantValue.Raw, got, want)
	}
}

func t82AssertJSONInt(t *testing.T, fixtureName string, payload []byte, path string, want int64) {
	t.Helper()
	value := gjson.GetBytes(payload, path)
	if !value.Exists() || value.Int() != want {
		t.Fatalf("%s %s = %s, want %d; payload=%s", fixtureName, path, value.Raw, want, payload)
	}
}

func t82FlattenSSE(t *testing.T, chunks [][]byte) []t82SSEEvent {
	t.Helper()
	var out []t82SSEEvent
	for _, chunk := range chunks {
		out = append(out, t82ParseSSEChunk(t, chunk)...)
	}
	return out
}

func t82LastNonDoneEvent(t *testing.T, chunks [][]byte) t82SSEEvent {
	t.Helper()
	events := t82FlattenSSE(t, chunks)
	for i := len(events) - 1; i >= 0; i-- {
		if !events[i].Done {
			return events[i]
		}
	}
	t.Fatal("stream has no non-DONE events")
	return t82SSEEvent{}
}

func t82EventDataPath(t *testing.T, event t82SSEEvent, path string) any {
	t.Helper()
	value, ok := t82EventDataPathOK(t, event, path)
	if !ok {
		return t82MissingFieldMarker{}
	}
	return value.Value()
}

func t82EventDataPathOK(t *testing.T, event t82SSEEvent, path string) (gjson.Result, bool) {
	t.Helper()
	raw := t82MustMarshalEventData(t, event)
	value := gjson.GetBytes(raw, path)
	return value, value.Exists()
}

func t82MustMarshalEventData(t *testing.T, event t82SSEEvent) []byte {
	t.Helper()
	raw, err := json.Marshal(event.Data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	return raw
}

func t82ClaudeWebSearchOriginal() []byte {
	return []byte(`{"model":"claude-sonnet-4-20250514","tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}],"messages":[{"role":"user","content":"weather"}]}`)
}

func t82AntigravityGoogleSearchRequest() []byte {
	return []byte(`{"model":"gemini","request":{"contents":[{"role":"user","parts":[{"text":"weather"}]}],"tools":[{"googleSearch":{"enhancedContent":{"imageSearch":{"maxResultCount":5}}}}]}}`)
}

func t82AntigravityGroundingResponse(id, text, supports, chunks, usage string) []byte {
	return []byte(`{"response":{"responseId":"` + id + `","modelVersion":"gemini","createTime":"2026-08-06T11:31:19Z","candidates":[{"content":{"role":"model","parts":[{"text":` + t82JSONString(text) + `}]},"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["weather"],"groundingSupports":` + supports + `,"groundingChunks":` + chunks + `}}],"usageMetadata":` + usage + `,"cpaUsageMetadata":` + usage + `}}`)
}

func t82GeminiModelResponse(id, model, text, usage string) []byte {
	return []byte(`{"responseId":"` + id + `","modelVersion":"` + model + `","createTime":"2026-08-06T11:31:19Z","candidates":[{"content":{"role":"model","parts":[{"text":` + t82JSONString(text) + `}]},"finishReason":"STOP"}],"usageMetadata":` + usage + `}`)
}

func t82JSONString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
