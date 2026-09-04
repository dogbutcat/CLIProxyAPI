package oagmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestAntigravityWebSearchResponseNonStreamMatchesDirectOracle(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	response := antigravityWebSearchGroundingResponse(t, "response", "Hello 世界 grounded tail.", []map[string]any{
		{"segment": map[string]any{"startIndex": 0, "endIndex": int64(len([]byte("Hello 世界"))), "text": "Hello 世界"}, "groundingChunkIndices": []any{0}},
	})
	response = antigravityWebSearchSetWrappedUsage(t, response, `{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":16}`)
	response, _ = sjson.SetRawBytes(response, "response.cpaUsageMetadata", []byte(`{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":99}`))

	got := normalizeAntigravityWebSearchNonStream(TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, response, nil))
	want := normalizeAntigravityWebSearchNonStream(agclaude.ConvertAntigravityResponseToClaudeNonStream(context.Background(), "model", original, translated, response, nil))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nonstream oracle mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, response, nil)
	if got := gjson.GetBytes(out, "id").String(); got != "resp_grounding" {
		t.Fatalf("id = %q, want resp_grounding: %s", got, out)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "gemini" {
		t.Fatalf("model = %q, want gemini: %s", got, out)
	}
	usage := gjson.GetBytes(out, "usage")
	if got := usage.Get("input_tokens").Int(); got != 10 {
		t.Fatalf("nonstream input_tokens = %d, want raw prompt 10: %s", got, out)
	}
	if got := usage.Get("cache_read_input_tokens").Int(); got != 3 {
		t.Fatalf("nonstream cache_read_input_tokens = %d, want 3: %s", got, out)
	}
	if got := usage.Get("output_tokens").Int(); got != 6 {
		t.Fatalf("nonstream fallback output_tokens = %d, want total-raw-prompt 6: %s", got, out)
	}
}

func TestAntigravityWebSearchResponseStreamMatchesDirectOracleOrdered(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	response := antigravityWebSearchGroundingResponse(t, "response", strings.Repeat("a", 55)+" 世界", []map[string]any{
		{"segment": map[string]any{"startIndex": 0, "endIndex": int64(len([]byte(strings.Repeat("a", 55)))), "text": strings.Repeat("a", 55)}, "groundingChunkIndices": []any{0}},
	})
	response = antigravityWebSearchSetWrappedUsage(t, response, `{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":0,"thoughtsTokenCount":2,"totalTokenCount":16}`)
	response, _ = sjson.SetRawBytes(response, "response.cpaUsageMetadata", []byte(`{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":99}`))

	oagEvents := collectAntigravityWebSearchOagStream(t, original, translated, [][]byte{response, []byte(`data: [DONE]`)})
	oracleEvents := collectAntigravityWebSearchOracleStream(t, original, translated, [][]byte{response, []byte(`[DONE]`)})
	got := normalizeAntigravityWebSearchStream(oagEvents)
	want := normalizeAntigravityWebSearchStream(oracleEvents)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stream oracle mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	messageStart := firstAntigravityWebSearchEvent(oagEvents, "message_start")
	if gotInput := messageStart.Get("message.usage.input_tokens").Int(); gotInput != 10 {
		t.Fatalf("message_start input_tokens = %d, want raw cpa prompt 10: %s", gotInput, messageStart.Raw)
	}
	if gotOutput := messageStart.Get("message.usage.output_tokens").Int(); gotOutput != 0 {
		t.Fatalf("message_start output_tokens = %d, want 0: %s", gotOutput, messageStart.Raw)
	}
	messageDelta := firstAntigravityWebSearchEvent(oagEvents, "message_delta")
	if gotInput := messageDelta.Get("usage.input_tokens").Int(); gotInput != 7 {
		t.Fatalf("stream final input_tokens = %d, want prompt-cache 7: %s", gotInput, messageDelta.Raw)
	}
	if gotOutput := messageDelta.Get("usage.output_tokens").Int(); gotOutput != 9 {
		t.Fatalf("stream final fallback output_tokens = %d, want total-adjusted-input 9: %s", gotOutput, messageDelta.Raw)
	}
	if gotCache := messageDelta.Get("usage.cache_read_input_tokens").Int(); gotCache != 3 {
		t.Fatalf("stream final cache_read_input_tokens = %d, want 3: %s", gotCache, messageDelta.Raw)
	}
	assertAntigravityWebSearchTerminalUsageOnce(t, oagEvents)
	textDeltas := antigravityTextDeltas(oagEvents)
	if len(textDeltas) < 2 || textDeltas[0] != strings.Repeat("a", 50) || textDeltas[1] != strings.Repeat("a", 5) {
		t.Fatalf("cited text not split into 50-rune deltas: %#v", got)
	}
}

func TestAntigravityWebSearchResponseGroundingGateNegatives(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	response := antigravityWebSearchGroundingResponse(t, "response", "grounded", nil)

	cases := []struct {
		name       string
		original   []byte
		translated []byte
	}{
		{name: "missing_claude_typed_tool", original: []byte(`{"model":"claude","tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"q"}]}`), translated: translated},
		{name: "missing_native_google_search", original: original, translated: []byte(`{"model":"ag","request":{"contents":[]}}`)},
		{name: "both_absent", original: []byte(`{"model":"claude","messages":[{"role":"user","content":"q"}]}`), translated: []byte(`{"model":"ag","request":{"contents":[]}}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", tc.original, tc.translated, response, nil)
			if strings.Contains(string(out), `"type":"server_tool_use"`) {
				t.Fatalf("negative gate synthesized web search: %s", out)
			}
			var param any
			stream := bytes.Join(TranslateStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", tc.original, tc.translated, response, &param), nil)
			if strings.Contains(string(stream), `"type":"server_tool_use"`) {
				t.Fatalf("negative stream gate synthesized web search: %s", stream)
			}
		})
	}
}

func TestAntigravityWebSearchResponseWrappedAndBareRoots(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	wrapped := antigravityWebSearchGroundingResponse(t, "response", "wrapped", nil)
	bare := []byte(gjson.GetBytes(wrapped, "response").Raw)

	for _, tc := range []struct {
		name     string
		response []byte
	}{
		{name: "wrapped", response: wrapped},
		{name: "bare", response: bare},
	} {
		out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, tc.response, nil)
		want := agclaude.ConvertAntigravityResponseToClaudeNonStream(context.Background(), "model", original, translated, tc.response, nil)
		if gotNorm, wantNorm := normalizeAntigravityWebSearchNonStream(out), normalizeAntigravityWebSearchNonStream(want); !reflect.DeepEqual(gotNorm, wantNorm) {
			t.Fatalf("%s root oracle mismatch\ngot:  %#v\nwant: %#v", tc.name, gotNorm, wantNorm)
		}
		if got := gjson.GetBytes(out, "content.0.type").String(); got != "server_tool_use" {
			t.Fatalf("root did not synthesize web search, got %q: %s", got, out)
		}
		if got := gjson.GetBytes(out, "content.2.text").String(); got != "wrapped" {
			t.Fatalf("root text = %q: %s", got, out)
		}
		oagEvents := collectAntigravityWebSearchOagStream(t, original, translated, [][]byte{tc.response, []byte(`data: [DONE]`)})
		oracleEvents := collectAntigravityWebSearchOracleStream(t, original, translated, [][]byte{tc.response, []byte(`[DONE]`)})
		if gotNorm, wantNorm := normalizeAntigravityWebSearchStream(oagEvents), normalizeAntigravityWebSearchStream(oracleEvents); !reflect.DeepEqual(gotNorm, wantNorm) {
			t.Fatalf("%s stream root oracle mismatch\ngot:  %#v\nwant: %#v", tc.name, gotNorm, wantNorm)
		}
	}
}

func TestAntigravityWebSearchResponseResultSetAndCitationURLRules(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	response := antigravityWebSearchGroundingResponse(t, "response", "abcdef tail", []map[string]any{
		{"segment": map[string]any{"startIndex": 0, "endIndex": 3, "text": "abc"}, "groundingChunkIndices": []any{1, 0}},
		{"segment": map[string]any{"startIndex": 3, "endIndex": 6, "text": "def"}, "groundingChunkIndices": []any{99}},
	})
	response, _ = sjson.SetRawBytes(response, "response.candidates.0.groundingMetadata.groundingChunks", []byte(`[
		{"web":{"uri":"https://example.com/a","title":"A"}},
		{"web":{"uri":"","title":""}},
		{"web":{"uri":"https://example.com/a","title":"Duplicate"}},
		{"web":{"uri":"   ","title":"Empty"}},
		{"web":{"uri":"https://example.com/no-title"}}
	]`))

	out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, response, nil)
	results := gjson.GetBytes(out, "content.1.content")
	if got := results.Get("#").Int(); got != 2 {
		t.Fatalf("result set count = %d, want 2: %s", got, out)
	}
	if results.Get("1.title").Exists() {
		t.Fatalf("no-title result should not use fallback title: %s", out)
	}
	if got := gjson.GetBytes(out, "content.2.citations.0.url").String(); got != "" {
		t.Fatalf("citation first in-range empty URL = %q, want empty: %s", got, out)
	}
	if got := gjson.GetBytes(out, "content.2.citations.0.title").String(); got != "A" {
		t.Fatalf("citation later non-empty title = %q, want A: %s", got, out)
	}
	if got := gjson.GetBytes(out, "content.2.text").String(); got != "abc" {
		t.Fatalf("first cited text = %q: %s", got, out)
	}
	if got := gjson.GetBytes(out, "content.3.text").String(); got != " tail" {
		t.Fatalf("invalid-ref covered segment should be omitted and tail preserved, got %q: %s", got, out)
	}
}

func TestAntigravityWebSearchResponseResultTitlesPreservePresenceAndBytes(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	response := antigravityWebSearchGroundingResponse(t, "response", "titles", nil)
	response, _ = sjson.SetRawBytes(response, "response.candidates.0.groundingMetadata.groundingChunks", []byte(`[
		{"web":{"uri":"https://example.com/absent"}},
		{"web":{"uri":"https://example.com/empty","title":""}},
		{"web":{"uri":"https://example.com/space","title":"   "}},
		{"web":{"uri":"https://example.com/title","title":"Title"}}
	]`))

	out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, response, nil)
	want := agclaude.ConvertAntigravityResponseToClaudeNonStream(context.Background(), "model", original, translated, response, nil)
	if gotNorm, wantNorm := normalizeAntigravityWebSearchNonStream(out), normalizeAntigravityWebSearchNonStream(want); !reflect.DeepEqual(gotNorm, wantNorm) {
		t.Fatalf("title oracle mismatch\ngot:  %#v\nwant: %#v", gotNorm, wantNorm)
	}
	results := gjson.GetBytes(out, "content.1.content")
	if results.Get("0.title").Exists() {
		t.Fatalf("absent title should remain absent: %s", out)
	}
	if title := results.Get("1.title"); !title.Exists() || title.String() != "" {
		t.Fatalf("empty title should remain present empty, got exists=%v value=%q: %s", title.Exists(), title.String(), out)
	}
	if title := results.Get("2.title"); !title.Exists() || title.String() != "   " {
		t.Fatalf("whitespace title should remain byte-for-byte, got exists=%v value=%q: %s", title.Exists(), title.String(), out)
	}
	if title := results.Get("3.title"); !title.Exists() || title.String() != "Title" {
		t.Fatalf("normal title = exists=%v value=%q: %s", title.Exists(), title.String(), out)
	}
}

func TestAntigravityWebSearchResponseByteSpansGapsOverlapsAndMalformed(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	text := "aa世界bbcc"
	response := antigravityWebSearchGroundingResponse(t, "response", text, []map[string]any{
		{"segment": map[string]any{"startIndex": 2, "endIndex": int64(len([]byte("aa世界"))), "text": "世界"}, "groundingChunkIndices": []any{0}},
		{"segment": map[string]any{"startIndex": 4, "endIndex": int64(len([]byte("aa世界bb"))), "text": "界bb"}, "groundingChunkIndices": []any{0}},
		{"segment": map[string]any{"startIndex": 999, "endIndex": 1000, "text": "out"}, "groundingChunkIndices": []any{0}},
		{"groundingChunkIndices": []any{0}},
	})

	out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, response, nil)
	var texts []string
	for _, block := range gjson.GetBytes(out, "content").Array()[2:] {
		texts = append(texts, block.Get("text").String())
	}
	if !reflect.DeepEqual(texts, []string{"aa", "世界", "bb", "cc"}) {
		t.Fatalf("byte span block texts = %#v; output=%s", texts, out)
	}
	if !gjson.GetBytes(out, "content.3.citations").Exists() || !gjson.GetBytes(out, "content.4.citations").Exists() {
		t.Fatalf("expected overlapping cited ranges after trim: %s", out)
	}
}

func TestAntigravityWebSearchResponseMultipleAndEmptyGroundingData(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()

	empty := antigravityWebSearchGroundingResponse(t, "response", "plain", nil)
	empty, _ = sjson.DeleteBytes(empty, "response.candidates.0.groundingMetadata.groundingSupports")
	out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, empty, nil)
	if got := gjson.GetBytes(out, "content.2.text").String(); got != "plain" {
		t.Fatalf("empty supports should emit uncited text, got %q: %s", got, out)
	}

	multiple := antigravityWebSearchGroundingResponse(t, "response", "first", nil)
	second := map[string]any{
		"content":           map[string]any{"parts": []any{map[string]any{"text": "second"}}},
		"groundingMetadata": map[string]any{"webSearchQueries": []any{"second"}},
	}
	secondJSON, _ := json.Marshal(second)
	multiple, _ = sjson.SetRawBytes(multiple, "response.candidates.-1", secondJSON)
	out = TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, multiple, nil)
	if got := gjson.GetBytes(out, "content.0.input.query").String(); got != "q" {
		t.Fatalf("should use first candidate grounding query, got %q: %s", got, out)
	}
}

func TestAntigravityWebSearchResponseStreamBuffersAndFinalUsageFallback(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	prefix := []byte(`data: {"response":{"responseId":"resp_stream","modelVersion":"gemini","candidates":[{"content":{"parts":[{"text":"before "}]}}],"cpaUsageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":99}}}`)
	final := antigravityWebSearchGroundingResponse(t, "response", "after", []map[string]any{
		{"segment": map[string]any{"startIndex": 0, "endIndex": 12, "text": "before after"}, "groundingChunkIndices": []any{0}},
	})
	final, _ = sjson.SetRawBytes(final, "response.usageMetadata", []byte(`{"promptTokenCount":10,"cachedContentTokenCount":3,"totalTokenCount":16}`))

	events := collectAntigravityWebSearchOagStream(t, original, translated, [][]byte{prefix, append([]byte("data: "), final...), []byte(`data: [DONE]`)})
	messageStart := firstAntigravityWebSearchEvent(events, "message_start")
	if got := messageStart.Get("message.usage.output_tokens").Int(); got != 0 {
		t.Fatalf("message_start output_tokens = %d, want 0: %#v", got, normalizeAntigravityWebSearchStream(events))
	}
	if firstEventIndex(events, func(event gjson.Result) bool {
		return event.Get("type").String() == "content_block_start" && event.Get("content_block.type").String() == "text"
	}) < firstEventIndex(events, func(event gjson.Result) bool {
		return event.Get("type").String() == "content_block_start" && event.Get("content_block.type").String() == "server_tool_use"
	}) {
		t.Fatalf("buffered text emitted before grounding: %#v", normalizeAntigravityWebSearchStream(events))
	}
	messageDelta := firstAntigravityWebSearchEvent(events, "message_delta")
	if got := messageDelta.Get("usage.input_tokens").Int(); got != 7 {
		t.Fatalf("input_tokens = %d, want prompt-cache 7: %s", got, messageDelta.Raw)
	}
	if got := messageDelta.Get("usage.cache_read_input_tokens").Int(); got != 3 {
		t.Fatalf("cache_read_input_tokens = %d, want 3: %s", got, messageDelta.Raw)
	}
	if got := messageDelta.Get("usage.output_tokens").Int(); got != 9 {
		t.Fatalf("output_tokens fallback = %d, want total-adjusted-input 9: %s", got, messageDelta.Raw)
	}
	if got := messageDelta.Get("usage.server_tool_use.web_search_requests").Int(); got != 1 {
		t.Fatalf("web_search_requests = %d, want 1: %s", got, messageDelta.Raw)
	}
	assertAntigravityWebSearchTerminalUsageOnce(t, events)
}

func TestAntigravityWebSearchResponseAnthropicStreamSerializerIgnoresMessageStartUsageWithoutMarker(t *testing.T) {
	serializer := (&AnthropicHandler{}).NewStreamSerializer("claude-test")
	events := parseAntigravitySerializerEvents(t, serializer.Serialize(StreamDelta{
		Type: EventStart,
		ID:   "msg_test",
		Usage: &UnifiedUsage{
			PromptTokens: 42,
			usagePresence: usagePresence{
				Prompt: true,
			},
		},
	}))
	messageStart := firstAntigravityWebSearchEvent(events, "message_start")
	if got := messageStart.Get("message.usage.input_tokens").Int(); got != 0 {
		t.Fatalf("plain message_start input_tokens = %d, want 0 without private marker: %s", got, messageStart.Raw)
	}
}

func TestAntigravityWebSearchResponseAnthropicStreamSerializerDoesNotAddReasoningToCompletionUsage(t *testing.T) {
	serializer := (&AnthropicHandler{}).NewStreamSerializer("claude-test")
	var events []gjson.Result
	for _, delta := range []StreamDelta{
		{Type: EventStart, ID: "msg_test"},
		{Type: EventTextDelta, Content: "done"},
		{Type: EventUsage, Usage: &UnifiedUsage{
			CompletionTokens: 20,
			ReasoningTokens:  5,
			usagePresence: usagePresence{
				Completion: true,
				Reasoning:  true,
			},
		}},
		{Type: EventDone, FinishReason: "stop"},
	} {
		events = append(events, parseAntigravitySerializerEvents(t, serializer.Serialize(delta))...)
	}
	events = append(events, parseAntigravitySerializerEvents(t, serializer.Flush())...)
	messageDelta := firstAntigravityWebSearchEvent(events, "message_delta")
	if got := messageDelta.Get("usage.output_tokens").Int(); got != 20 {
		t.Fatalf("plain final output_tokens = %d, want completion only 20: %s", got, messageDelta.Raw)
	}
}

func TestAntigravityWebSearchResponseStreamSubsequentFinishUsageNoDuplicate(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	grounding := antigravityWebSearchGroundingResponse(t, "response", "grounded ", []map[string]any{
		{"segment": map[string]any{"startIndex": 0, "endIndex": 9, "text": "grounded "}, "groundingChunkIndices": []any{0}},
	})
	grounding, _ = sjson.DeleteBytes(grounding, "response.candidates.0.finishReason")
	grounding, _ = sjson.DeleteBytes(grounding, "response.usageMetadata")
	terminal := []byte(`{"response":{"responseId":"resp_grounding","modelVersion":"gemini","candidates":[{"content":{"parts":[{"text":"tail"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":2,"totalTokenCount":16}}}`)

	oagEvents := collectAntigravityWebSearchOagStream(t, original, translated, [][]byte{grounding, terminal, []byte(`data: [DONE]`)})
	oracleEvents := collectAntigravityWebSearchOracleStream(t, original, translated, [][]byte{grounding, terminal, []byte(`[DONE]`)})
	got := normalizeAntigravityWebSearchStream(oagEvents)
	want := normalizeAntigravityWebSearchStream(oracleEvents)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subsequent terminal oracle mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	assertAntigravityWebSearchTerminalUsageOnce(t, oagEvents)
	messageDelta := firstAntigravityWebSearchEvent(oagEvents, "message_delta")
	if gotOutput := messageDelta.Get("usage.output_tokens").Int(); gotOutput != 6 {
		t.Fatalf("output_tokens = %d, want candidates+thoughts 6: %s", gotOutput, messageDelta.Raw)
	}
}

func TestAntigravityWebSearchResponseNoGroundingGenericBehavior(t *testing.T) {
	original := antigravityWebSearchClaudeOriginalRequest()
	translated := antigravityWebSearchTranslatedRequest()
	response := []byte(`{"response":{"responseId":"resp_generic","modelVersion":"gemini","candidates":[{"content":{"parts":[{"text":"generic"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}}`)

	out := TranslateNonStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, response, nil)
	if got := gjson.GetBytes(out, "content.0.type").String(); got != "text" {
		t.Fatalf("no-grounding nonstream should stay generic, got %q: %s", got, out)
	}
	var param any
	events := collectAntigravityWebSearchOagStream(t, original, translated, [][]byte{append([]byte("data: "), response...), []byte(`data: [DONE]`)})
	_ = param
	if strings.Contains(formatAntigravityEvents(events), "server_tool_use") {
		t.Fatalf("no-grounding stream should stay generic: %#v", normalizeAntigravityWebSearchStream(events))
	}
}

func antigravityWebSearchClaudeOriginalRequest() []byte {
	return []byte(`{"model":"claude","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
}

func antigravityWebSearchTranslatedRequest() []byte {
	return []byte(`{"model":"ag","request":{"tools":[{"googleSearch":{}}]}}`)
}

func antigravityWebSearchGroundingResponse(t *testing.T, rootName, text string, supports []map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"responseId":   "resp_grounding",
		"modelVersion": "gemini",
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": text}}},
			"groundingMetadata": map[string]any{
				"webSearchQueries": []any{"q"},
				"groundingChunks": []any{
					map[string]any{"web": map[string]any{"uri": "https://example.com/a", "title": "A"}},
				},
				"groundingSupports": supports,
			},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 2, "thoughtsTokenCount": 3, "totalTokenCount": 15},
	}
	var root any = payload
	if rootName == "response" {
		root = map[string]any{"response": payload}
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("marshal grounding response: %v", err)
	}
	return raw
}

func antigravityWebSearchSetWrappedUsage(t *testing.T, raw []byte, usage string) []byte {
	t.Helper()
	updated, err := sjson.SetRawBytes(raw, "response.usageMetadata", []byte(usage))
	if err != nil {
		t.Fatalf("set wrapped usage: %v", err)
	}
	return updated
}

func collectAntigravityWebSearchOagStream(t *testing.T, original, translated []byte, chunks [][]byte) []gjson.Result {
	t.Helper()
	var param any
	var events []gjson.Result
	for _, chunk := range chunks {
		for _, output := range TranslateStream(context.Background(), FormatAntigravity, FormatAnthropic, "model", original, translated, chunk, &param) {
			events = append(events, parseCodexWebSearchSSEData(t, output)...)
		}
	}
	return events
}

func collectAntigravityWebSearchOracleStream(t *testing.T, original, translated []byte, chunks [][]byte) []gjson.Result {
	t.Helper()
	var param any
	var events []gjson.Result
	for _, chunk := range chunks {
		for _, output := range agclaude.ConvertAntigravityResponseToClaude(context.Background(), "model", original, translated, chunk, &param) {
			events = append(events, parseCodexWebSearchSSEData(t, output)...)
		}
	}
	return events
}

func parseAntigravitySerializerEvents(t *testing.T, outputs [][]byte) []gjson.Result {
	t.Helper()
	var events []gjson.Result
	for _, output := range outputs {
		events = append(events, parseCodexWebSearchSSEData(t, output)...)
	}
	return events
}

func normalizeAntigravityWebSearchNonStream(raw []byte) any {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		panic(err)
	}
	return normalizeAntigravityGeneratedValues(decoded)
}

func normalizeAntigravityWebSearchStream(events []gjson.Result) []any {
	out := make([]any, 0, len(events))
	for _, event := range events {
		var decoded any
		if err := json.Unmarshal([]byte(event.Raw), &decoded); err != nil {
			panic(err)
		}
		out = append(out, normalizeAntigravityGeneratedValues(decoded))
	}
	return out
}

func normalizeAntigravityGeneratedValues(value any) any {
	switch v := value.(type) {
	case map[string]any:
		if v["type"] == "server_tool_use" {
			if id, ok := v["id"].(string); ok && strings.HasPrefix(id, "srvtoolu_") {
				v["id"] = "srvtoolu_generated"
			}
		}
		if v["type"] == "web_search_tool_result" {
			if id, ok := v["tool_use_id"].(string); ok && strings.HasPrefix(id, "srvtoolu_") {
				v["tool_use_id"] = "srvtoolu_generated"
			}
		}
		for key, item := range v {
			v[key] = normalizeAntigravityGeneratedValues(item)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = normalizeAntigravityGeneratedValues(item)
		}
		return v
	default:
		return value
	}
}

func firstAntigravityWebSearchEvent(events []gjson.Result, eventType string) gjson.Result {
	for _, event := range events {
		if event.Get("type").String() == eventType {
			return event
		}
	}
	return gjson.Result{}
}

func assertAntigravityWebSearchTerminalUsageOnce(t *testing.T, events []gjson.Result) {
	t.Helper()
	if count := countStreamTypes(events, "message_delta"); count != 1 {
		t.Fatalf("message_delta/final usage count = %d, want 1: %#v", count, normalizeAntigravityWebSearchStream(events))
	}
	if count := countStreamTypes(events, "message_stop"); count != 1 {
		t.Fatalf("message_stop count = %d, want 1: %#v", count, normalizeAntigravityWebSearchStream(events))
	}
	messageDelta := firstAntigravityWebSearchEvent(events, "message_delta")
	if !messageDelta.Get("usage.server_tool_use.web_search_requests").Exists() {
		t.Fatalf("final web_search usage missing: %#v", normalizeAntigravityWebSearchStream(events))
	}
}

func antigravityTextDeltas(events []gjson.Result) []string {
	var out []string
	for _, event := range events {
		if event.Get("type").String() == "content_block_delta" && event.Get("delta.type").String() == "text_delta" {
			out = append(out, event.Get("delta.text").String())
		}
	}
	return out
}

func formatAntigravityEvents(events []gjson.Result) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString(event.Raw)
		b.WriteByte('\n')
	}
	return b.String()
}
