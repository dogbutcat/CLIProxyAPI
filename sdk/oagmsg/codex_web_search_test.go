package oagmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	"github.com/tidwall/gjson"
)

func TestCodexWebSearchStreamMatchesDirectOracleNormalID(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search weather"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"id":"ws_123","type":"web_search_call","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.web_search_call.searching","item_id":"ws_123"}`),
		[]byte(`data: {"type":"response.web_search_call.completed","item_id":"ws_123"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"ws_123","type":"web_search_call","status":"completed","action":{"type":"search","query":"search weather"},"results":[{"title":"Weather","url":"https://example.com/weather"}]}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},"output_index":1}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2}}}`),
	}
	assertCodexWebSearchStreamOracle(t, original, chunks)
}

func TestCodexWebSearchStreamMatchesDirectOracleFallbackID(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search weather"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"web_search_call","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.web_search_call.completed","item_id":"ws_from_upstream"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"ws_from_upstream","type":"web_search_call","status":"completed","action":{"type":"search","query":"search weather"}}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2}}}`),
	}
	assertCodexWebSearchStreamOracle(t, original, chunks)
}

func TestCodexWebSearchNonStreamMatchesDirectOracleNormal(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search weather"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex-spark","stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2},"output":[{"type":"web_search_call","id":"ws_123","status":"completed","action":{"type":"search","query":"search weather"},"results":[{"title":"Weather","url":"https://example.com/weather"}]},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`)
	assertCodexWebSearchNonStreamOracle(t, original, response)
}

func TestCodexWebSearchNonStreamMatchesDirectOracleEndTurn(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search weather"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex-spark","stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2},"output":[{"type":"web_search_call","id":"ws_123","status":"completed","action":{"type":"search","query":"search weather"}},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, response, nil)
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn; output=%s", got, out)
	}
	assertCodexWebSearchNonStreamOracle(t, original, response)
}

func TestCodexWebSearchNonStreamMatchesDirectOracleEmptyDedupe(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex-spark","stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"open_page"}},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"weather"}},{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, response, nil)
	if strings.Count(string(out), `"type":"server_tool_use"`) != 1 {
		t.Fatalf("server_tool_use count mismatch: %s", out)
	}
	if !strings.Contains(string(out), `"weather"`) {
		t.Fatalf("populated duplicate did not win: %s", out)
	}
	assertCodexWebSearchNonStreamOracle(t, original, response)
}

func TestCodexWebSearchNonStreamMissingIDMatchesDirectOracleSkip(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex-spark","stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}},{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, response, nil)
	if strings.Contains(string(out), `"type":"server_tool_use"`) {
		t.Fatalf("missing-id nonstream web_search should be skipped: %s", out)
	}
	assertCodexWebSearchNonStreamOracle(t, original, response)
}

func TestCodexWebSearchNonStreamMultipleResultsURLFilteringAndFunctionCoexist(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"type":"function","name":"lookup","parameters":{"type":"object"}}],"messages":[{"role":"user","content":"q"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"first"},"results":[{"title":"First","url":"https://example.com/first"},{"title":"","url":"https://example.com/fallback"},{"title":"No URL"}]},{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"search","query":"second"},"results":[{"title":"Second","url":"https://example.com/second"}]},{"type":"function_call","call_id":"call_lookup","name":"lookup","arguments":"{\"id\":1}"},{"type":"message","content":[{"type":"output_text","text":"after"}]}]}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, response, nil)
	root := gjson.ParseBytes(out)
	if got := root.Get("stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use with genuine function; output=%s", got, out)
	}
	content := root.Get("content").Array()
	if got := codexWebSearchContentTypes(content); !reflect.DeepEqual(got, []string{"server_tool_use", "web_search_tool_result", "server_tool_use", "web_search_tool_result", "tool_use", "text"}) {
		t.Fatalf("content type order = %v; output=%s", got, out)
	}
	firstResults := root.Get("content.1.content").Array()
	if len(firstResults) != 2 {
		t.Fatalf("filtered result count = %d, want 2; output=%s", len(firstResults), out)
	}
	if got := root.Get("content.1.content.1.title").String(); got != "https://example.com/fallback" {
		t.Fatalf("title fallback = %q; output=%s", got, out)
	}
	if !root.Get("content.1.content.0.page_age").Exists() || root.Get("content.1.content.0.page_age").Type != gjson.Null {
		t.Fatalf("page_age null missing: %s", out)
	}
}

func TestCodexWebSearchNonStreamCustomCoexistPreservesClaudeOrderAndFreeformInput(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"type":"custom","name":"exec"}],"messages":[{"role":"user","content":"q"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}},{"type":"custom_tool_call","call_id":"call_exec","name":"exec","input":"pwd"},{"type":"message","content":[{"type":"output_text","text":"after"}]}]}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, response, nil)
	root := gjson.ParseBytes(out)
	if got := root.Get("stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use with genuine custom call; output=%s", got, out)
	}
	content := root.Get("content").Array()
	if got := codexWebSearchContentTypes(content); !reflect.DeepEqual(got, []string{"server_tool_use", "web_search_tool_result", "tool_use", "text"}) {
		t.Fatalf("content type order = %v; output=%s", got, out)
	}
	if got := root.Get("content.2.name").String(); got != "exec" {
		t.Fatalf("custom tool_use name = %q; output=%s", got, out)
	}
	if got := root.Get("content.2.input.input").String(); got != "pwd" {
		t.Fatalf("custom freeform input = %q, want pwd; output=%s", got, out)
	}
	if got := root.Get("content.3.text").String(); got != "after" {
		t.Fatalf("message text order/content = %q; output=%s", got, out)
	}
}

func TestCodexWebSearchStreamReasoningTextBoundaryAndTerminalOnce(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.added"}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"think"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"sig_final"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"answer"}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`data: [DONE]`),
	}
	got := collectCodexWebSearchOagStream(t, original, chunks)
	if count := countStreamTypes(got, "message_stop"); count != 1 {
		t.Fatalf("message_stop count = %d, want 1; events=%v", count, got)
	}
	assertCodexWebSearchStreamOracle(t, original, chunks[:len(chunks)-1])
}

func TestCodexWebSearchStreamFallbackIDMatchesDirectOracleAfterText(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"before"}]}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexWebSearchOagStream(t, original, chunks)
	want := collectCodexWebSearchOracleStream(t, original, chunks)
	gotID, gotResultID := firstCodexWebSearchStreamIDs(t, got)
	wantID, wantResultID := firstCodexWebSearchStreamIDs(t, want)
	if gotID != wantID || gotResultID != wantResultID || gotID != "web_search_1" {
		t.Fatalf("fallback IDs got (%q,%q), want oracle (%q,%q) and web_search_1", gotID, gotResultID, wantID, wantResultID)
	}
	gotNormalized := normalizeCodexWebSearchStreamEvents(got)
	wantNormalized := normalizeCodexWebSearchStreamEvents(want)
	if !reflect.DeepEqual(gotNormalized, wantNormalized) {
		t.Fatalf("fallback stream oracle mismatch\ngot:  %#v\nwant: %#v", gotNormalized, wantNormalized)
	}
}

func TestCodexWebSearchStreamReasoningDoneSummaryNotReplayedBeforeSearch(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.added"}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"think"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"sig_final"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	assertCodexWebSearchStreamOracle(t, original, chunks)
	got := collectCodexWebSearchOagStream(t, original, chunks)
	if count := countThinkingDeltaText(got, "think"); count != 1 {
		t.Fatalf("thinking summary replay count = %d, want 1; events=%v", count, normalizeCodexWebSearchStreamEvents(got))
	}
	thinkingStop := firstEventIndex(got, func(event gjson.Result) bool {
		return event.Get("type").String() == "content_block_stop" && event.Get("index").Int() == 0
	})
	searchStart := firstEventIndex(got, func(event gjson.Result) bool {
		return event.Get("type").String() == "content_block_start" && event.Get("content_block.type").String() == "server_tool_use"
	})
	if thinkingStop < 0 || searchStart < 0 || thinkingStop > searchStart {
		t.Fatalf("thinking did not close before search; events=%v", normalizeCodexWebSearchStreamEvents(got))
	}
}

func TestCodexWebSearchStreamSignatureOnlyReasoningMatchesDirectOracle(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"sig_only"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	assertCodexWebSearchStreamOracle(t, original, chunks)
}

func TestCodexWebSearchSameFamilyFastPathPreservesNativeFrames(t *testing.T) {
	raw := []byte(`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`)
	var param any
	out := TranslateStream(context.Background(), FormatCodex, FormatOpenAIResponse, "", nil, nil, raw, &param)
	if len(out) != 1 || !bytes.Equal(out[0], raw) {
		t.Fatalf("same-family stream fast path changed frame: %#q", out)
	}

	response := []byte(`{"id":"resp_1","object":"response","model":"gpt-5","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}]}`)
	body := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "", nil, nil, response, nil)
	if !bytes.Equal(body, response) {
		t.Fatalf("same-family nonstream fast path changed body:\ngot  %s\nwant %s", body, response)
	}
}

func TestCodexWebSearchResponsesStreamMetadataReconstructionPreservesNativeSearch(t *testing.T) {
	longName := strings.Repeat("a", codexToolNameLimitBytes+8)
	shortName := strings.Repeat("a", codexToolNameLimitBytes)
	original := []byte(`{"model":"gpt-5","tools":[{"type":"function","name":"` + longName + `","parameters":{"type":"object"}}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5","created_at":1}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"},"results":[{"title":"Result","url":"https://example.com"}]}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"` + shortName + `","arguments":"{}"}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"message","content":[{"type":"output_text","text":"done"}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`data: [DONE]`),
	}
	events := collectCodexWebSearchResponsesStream(t, original, chunks)
	doneItems := responseDoneOutputItems(events)
	if got := codexWebSearchContentTypes(doneItems); !reflect.DeepEqual(got, []string{"web_search_call", "function_call", "message"}) {
		t.Fatalf("done item type order = %v; events=%v", got, events)
	}
	// Search metadata reconstruction is intentionally minimal: one native done
	// item plus the terminal completed aggregate, with no added/search lifecycle
	// item and no ordinary function/custom surrogate.
	if countItemsOfType(doneItems, "web_search_call") != 1 || countItemsOfType(doneItems, "function_call") != 1 {
		t.Fatalf("native item type counts changed: doneItems=%v", doneItems)
	}
	if got := doneItems[0].Get("action.query").String(); got != "q" {
		t.Fatalf("web_search query = %q; item=%s", got, doneItems[0].Raw)
	}
	if got := doneItems[0].Get("results.0.url").String(); got != "https://example.com" {
		t.Fatalf("web_search results lost: %s", doneItems[0].Raw)
	}
	if got := doneItems[1].Get("name").String(); got != longName {
		t.Fatalf("function name = %q, want restored long name; item=%s", got, doneItems[1].Raw)
	}
	completedOutput := lastResponseCompletedOutput(events)
	if got := codexWebSearchContentTypes(completedOutput); !reflect.DeepEqual(got, []string{"web_search_call", "function_call", "message"}) {
		t.Fatalf("completed output order = %v; output=%v", got, completedOutput)
	}
	if countItemsOfType(completedOutput, "web_search_call") != 1 {
		t.Fatalf("completed output web_search count changed: output=%v", completedOutput)
	}
	if countDoneSignal(events) != 1 {
		t.Fatalf("terminal [DONE] count = %d, want 1; events=%v", countDoneSignal(events), events)
	}
}

func TestCodexWebSearchResponsesStreamCustomMetadataCoexistPreservesNativeTypes(t *testing.T) {
	longName := strings.Repeat("e", codexToolNameLimitBytes+8)
	metadata := buildRequestToolMetadataFromRequests([]byte(`{"model":"gpt-5","tools":[{"type":"custom","name":"` + longName + `"}]}`))
	shortName := metadata.toolNameForward[longName]
	if shortName == "" || shortName == longName {
		t.Fatalf("fixture did not allocate short custom name: %+v", metadata.toolNameForward)
	}
	original := []byte(`{"model":"gpt-5","tools":[{"type":"custom","name":"` + longName + `"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5","created_at":1}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"},"results":[{"title":"Result","url":"https://example.com"}]}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"ctc_call_exec","type":"custom_tool_call","status":"completed","call_id":"call_exec","name":"` + shortName + `","input":"pwd"}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`data: [DONE]`),
	}
	events := collectCodexWebSearchResponsesStream(t, original, chunks)
	doneItems := responseDoneOutputItems(events)
	if got := codexWebSearchContentTypes(doneItems); !reflect.DeepEqual(got, []string{"web_search_call", "custom_tool_call"}) {
		t.Fatalf("done item type order = %v; events=%v", got, events)
	}
	if countItemsOfType(doneItems, "web_search_call") != 1 || countItemsOfType(doneItems, "custom_tool_call") != 1 || countItemsOfType(doneItems, "function_call") != 0 {
		t.Fatalf("native item type counts changed: doneItems=%v", doneItems)
	}
	if got := doneItems[0].Get("action.query").String(); got != "q" {
		t.Fatalf("web_search query = %q; item=%s", got, doneItems[0].Raw)
	}
	if got := doneItems[0].Get("results.0.url").String(); got != "https://example.com" {
		t.Fatalf("web_search results lost: %s", doneItems[0].Raw)
	}
	if got := doneItems[1].Get("name").String(); got != longName {
		t.Fatalf("custom name = %q, want restored long name; item=%s", got, doneItems[1].Raw)
	}
	if got := doneItems[1].Get("input").String(); got != "pwd" {
		t.Fatalf("custom input = %q, want pwd; item=%s", got, doneItems[1].Raw)
	}
	completedOutput := lastResponseCompletedOutput(events)
	if got := codexWebSearchContentTypes(completedOutput); !reflect.DeepEqual(got, []string{"web_search_call", "custom_tool_call"}) {
		t.Fatalf("completed output order = %v; output=%v", got, completedOutput)
	}
	if countItemsOfType(completedOutput, "web_search_call") != 1 || countItemsOfType(completedOutput, "custom_tool_call") != 1 || countItemsOfType(completedOutput, "function_call") != 0 {
		t.Fatalf("completed native item counts changed: output=%v", completedOutput)
	}
	if got := completedOutput[1].Get("name").String(); got != longName {
		t.Fatalf("completed custom name = %q, want restored long name; output=%v", got, completedOutput)
	}
	if countDoneSignal(events) != 1 {
		t.Fatalf("terminal [DONE] count = %d, want 1; events=%v", countDoneSignal(events), events)
	}
}

func TestCodexWebSearchResponsesStreamTwoNoIDSearchesStayDistinct(t *testing.T) {
	longName := strings.Repeat("d", codexToolNameLimitBytes+8)
	original := []byte(`{"model":"gpt-5","tools":[{"type":"function","name":"` + longName + `","parameters":{"type":"object"}}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5","created_at":1}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"first"}}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"second"}}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`data: [DONE]`),
	}
	events := collectCodexWebSearchResponsesStream(t, original, chunks)
	doneItems := responseDoneOutputItems(events)
	if got := codexWebSearchContentTypes(doneItems); !reflect.DeepEqual(got, []string{"web_search_call", "web_search_call"}) {
		t.Fatalf("done item type order = %v; events=%v", got, events)
	}
	if got := []string{doneItems[0].Get("action.query").String(), doneItems[1].Get("action.query").String()}; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("no-id search queries changed: %v; items=%v", got, doneItems)
	}
	if doneItems[0].Get("id").Exists() || doneItems[1].Get("id").Exists() {
		t.Fatalf("Responses reconstruction should not expose internal fallback ids: %v", doneItems)
	}
	completedOutput := lastResponseCompletedOutput(events)
	if got := codexWebSearchContentTypes(completedOutput); !reflect.DeepEqual(got, []string{"web_search_call", "web_search_call"}) {
		t.Fatalf("completed output type order = %v; output=%v", got, completedOutput)
	}
	if countItemsOfType(completedOutput, "web_search_call") != 2 {
		t.Fatalf("completed output search count = %d, want 2; output=%v", countItemsOfType(completedOutput, "web_search_call"), completedOutput)
	}
	if countDoneSignal(events) != 1 {
		t.Fatalf("terminal [DONE] count = %d, want 1; events=%v", countDoneSignal(events), events)
	}
}

func TestCodexWebSearchSameFamilyReconstructionPreservesNativeOrder(t *testing.T) {
	original := []byte(`{"model":"gpt-5","tools":[{"type":"function","name":"` + strings.Repeat("a", codexToolNameLimitBytes+8) + `","parameters":{"type":"object"}}]}`)
	shortName := strings.Repeat("a", codexToolNameLimitBytes)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}},{"type":"function_call","call_id":"call_1","name":"` + shortName + `","arguments":"{}"},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "", original, nil, response, nil)
	output := gjson.GetBytes(out, "output").Array()
	if got := codexWebSearchContentTypes(output); !reflect.DeepEqual(got, []string{"web_search_call", "function_call", "message"}) {
		t.Fatalf("reconstructed output type order = %v; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.0.action.query").String(); got != "q" {
		t.Fatalf("web_search item lost action query: %s", out)
	}
	if got := gjson.GetBytes(out, "output.1.name").String(); got != strings.Repeat("a", codexToolNameLimitBytes+8) {
		t.Fatalf("metadata reconstruction did not restore function name: %q; output=%s", got, out)
	}
}

func TestCodexWebSearchSameFamilyCustomReconstructionRestoresLongName(t *testing.T) {
	longName := strings.Repeat("f", codexToolNameLimitBytes+8)
	metadata := buildRequestToolMetadataFromRequests([]byte(`{"model":"gpt-5","tools":[{"type":"custom","name":"` + longName + `"}]}`))
	shortName := metadata.toolNameForward[longName]
	if shortName == "" || shortName == longName {
		t.Fatalf("fixture did not allocate short custom name: %+v", metadata.toolNameForward)
	}
	original := []byte(`{"model":"gpt-5","tools":[{"type":"custom","name":"` + longName + `"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}},{"type":"custom_tool_call","call_id":"call_exec","name":"` + shortName + `","input":"pwd"},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`)
	out := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "", original, nil, response, nil)
	output := gjson.GetBytes(out, "output").Array()
	if got := codexWebSearchContentTypes(output); !reflect.DeepEqual(got, []string{"web_search_call", "custom_tool_call", "message"}) {
		t.Fatalf("reconstructed output type order = %v; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.1.name").String(); got != longName {
		t.Fatalf("metadata reconstruction did not restore custom name: %q; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.1.input").String(); got != "pwd" {
		t.Fatalf("metadata reconstruction changed custom input: %q; output=%s", got, out)
	}
}

func TestCodexWebSearchResponseContentSidecarOnlyWhenSearchPresent(t *testing.T) {
	raw := []byte(`{"id":"resp_1","object":"response","model":"gpt-5","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`)
	out := TranslateNonStream(context.Background(), FormatOpenAIResponse, FormatAnthropic, "model", nil, nil, raw, nil)
	types := codexWebSearchContentTypes(gjson.GetBytes(out, "content").Array())
	if !reflect.DeepEqual(types, []string{"text", "tool_use"}) {
		t.Fatalf("no-search Responses->Claude changed legacy ordering/types: %v; output=%s", types, out)
	}
}

func TestCodexWebSearchNonStreamImageAndUnknownCoexistPreserved(t *testing.T) {
	longName := strings.Repeat("b", codexToolNameLimitBytes+8)
	shortName := strings.Repeat("b", codexToolNameLimitBytes)
	original := []byte(`{"model":"gpt-5","tools":[{"type":"function","name":"` + longName + `","parameters":{"type":"object"}}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}},{"type":"image_generation_call","id":"img_1","result":"abc","output_format":"png"},{"type":"future_item","id":"future_1","payload":{"keep":true}},{"type":"function_call","call_id":"call_1","name":"` + shortName + `","arguments":"{}"},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`)
	claude := TranslateNonStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, response, nil)
	if !strings.Contains(string(claude), `![generated image](data:image/png;base64,abc)`) {
		t.Fatalf("Claude image markdown lost with web_search coexist: %s", claude)
	}
	reconstructed := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "", original, nil, response, nil)
	output := gjson.GetBytes(reconstructed, "output").Array()
	if got := codexWebSearchContentTypes(output); !reflect.DeepEqual(got, []string{"web_search_call", "image_generation_call", "future_item", "function_call", "message"}) {
		t.Fatalf("raw output order/type loss = %v; output=%s", got, reconstructed)
	}
	if !gjson.GetBytes(reconstructed, "output.2.payload.keep").Bool() {
		t.Fatalf("unknown future item payload lost: %s", reconstructed)
	}
	if got := gjson.GetBytes(reconstructed, "output.3.name").String(); got != longName {
		t.Fatalf("function name not restored inside raw sidecar: %q; output=%s", got, reconstructed)
	}
}

func TestCodexWebSearchResponsesStreamUnknownItemPassthrough(t *testing.T) {
	longName := strings.Repeat("c", codexToolNameLimitBytes+8)
	shortName := strings.Repeat("c", codexToolNameLimitBytes)
	original := []byte(`{"model":"gpt-5","tools":[{"type":"function","name":"` + longName + `","parameters":{"type":"object"}}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5","created_at":1}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"future_item","id":"future_1","payload":{"keep":true}}}`),
		[]byte(`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"call_1","name":"` + shortName + `","arguments":"{}"}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`data: [DONE]`),
	}
	events := collectCodexWebSearchResponsesStream(t, original, chunks)
	completedOutput := lastResponseCompletedOutput(events)
	if got := codexWebSearchContentTypes(completedOutput); !reflect.DeepEqual(got, []string{"web_search_call", "future_item", "function_call"}) {
		t.Fatalf("stream completed output order/type loss = %v; events=%v", got, events)
	}
	if !completedOutput[1].Get("payload.keep").Bool() {
		t.Fatalf("stream unknown item payload lost: %v", completedOutput)
	}
	if got := completedOutput[2].Get("name").String(); got != longName {
		t.Fatalf("stream function name = %q, want restored long name; output=%v", got, completedOutput)
	}
	if countDoneSignal(events) != 1 {
		t.Fatalf("terminal [DONE] count = %d, want 1; events=%v", countDoneSignal(events), events)
	}
}

func assertCodexWebSearchStreamOracle(t *testing.T, original []byte, chunks [][]byte) {
	t.Helper()
	got := normalizeCodexWebSearchStreamEvents(collectCodexWebSearchOagStream(t, original, chunks))
	want := normalizeCodexWebSearchStreamEvents(collectCodexWebSearchOracleStream(t, original, chunks))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stream oracle mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func assertCodexWebSearchNonStreamOracle(t *testing.T, original, response []byte) {
	t.Helper()
	got := normalizeCodexWebSearchNonStream(TranslateNonStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, response, nil))
	want := normalizeCodexWebSearchNonStream(codexclaude.ConvertCodexResponseToClaudeNonStream(context.Background(), "", original, nil, response, nil))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nonstream oracle mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func collectCodexWebSearchOagStream(t *testing.T, original []byte, chunks [][]byte) []gjson.Result {
	t.Helper()
	var param any
	var events []gjson.Result
	for _, chunk := range chunks {
		for _, output := range TranslateStream(context.Background(), FormatCodex, FormatAnthropic, "model", original, nil, chunk, &param) {
			events = append(events, parseCodexWebSearchSSEData(t, output)...)
		}
	}
	return events
}

func collectCodexWebSearchOracleStream(t *testing.T, original []byte, chunks [][]byte) []gjson.Result {
	t.Helper()
	var param any
	var events []gjson.Result
	for _, chunk := range chunks {
		for _, output := range codexclaude.ConvertCodexResponseToClaude(context.Background(), "", original, nil, chunk, &param) {
			events = append(events, parseCodexWebSearchSSEData(t, output)...)
		}
	}
	return events
}

func parseCodexWebSearchSSEData(t *testing.T, output []byte) []gjson.Result {
	t.Helper()
	var events []gjson.Result
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			events = append(events, gjson.Parse(`{"type":"[DONE]"}`))
			continue
		}
		if !gjson.Valid(payload) {
			t.Fatalf("invalid SSE JSON payload %q in %q", payload, output)
		}
		events = append(events, gjson.Parse(payload))
	}
	return events
}

func normalizeCodexWebSearchStreamEvents(events []gjson.Result) []map[string]any {
	var out []map[string]any
	for _, event := range events {
		item := map[string]any{"type": event.Get("type").String()}
		if index := event.Get("index"); index.Exists() {
			item["index"] = index.Int()
		}
		switch event.Get("type").String() {
		case "content_block_start":
			cb := event.Get("content_block")
			item["block_type"] = cb.Get("type").String()
			switch cb.Get("type").String() {
			case "server_tool_use":
				item["id"] = normalizeCodexWebSearchID(cb.Get("id").String())
				item["name"] = cb.Get("name").String()
				item["input"] = compactJSONRaw(cb.Get("input").Raw)
			case "web_search_tool_result":
				item["tool_use_id"] = normalizeCodexWebSearchID(cb.Get("tool_use_id").String())
				item["content"] = normalizeCodexWebSearchResultContent(cb.Get("content"))
			case "text", "thinking", "tool_use":
				item["id"] = normalizeCodexWebSearchID(cb.Get("id").String())
				item["name"] = cb.Get("name").String()
			}
		case "content_block_delta":
			delta := event.Get("delta")
			item["delta_type"] = delta.Get("type").String()
			item["partial_json"] = compactJSONRaw(delta.Get("partial_json").String())
			item["text"] = delta.Get("text").String()
			item["thinking"] = delta.Get("thinking").String()
			item["signature"] = delta.Get("signature").String()
		case "message_delta":
			item["stop_reason"] = event.Get("delta.stop_reason").String()
		}
		out = append(out, item)
	}
	return out
}

func normalizeCodexWebSearchNonStream(raw []byte) []map[string]any {
	root := gjson.ParseBytes(raw)
	out := []map[string]any{{"stop_reason": root.Get("stop_reason").String()}}
	for _, block := range root.Get("content").Array() {
		item := map[string]any{"type": block.Get("type").String()}
		switch block.Get("type").String() {
		case "server_tool_use":
			item["id"] = normalizeCodexWebSearchID(block.Get("id").String())
			item["name"] = block.Get("name").String()
			item["input"] = compactJSONRaw(block.Get("input").Raw)
		case "web_search_tool_result":
			item["tool_use_id"] = normalizeCodexWebSearchID(block.Get("tool_use_id").String())
			item["content"] = normalizeCodexWebSearchResultContent(block.Get("content"))
		case "text":
			item["text"] = block.Get("text").String()
		case "thinking":
			item["thinking"] = block.Get("thinking").String()
			item["signature"] = block.Get("signature").String()
		case "tool_use":
			item["id"] = normalizeCodexWebSearchID(block.Get("id").String())
			item["name"] = block.Get("name").String()
			item["input"] = compactJSONRaw(block.Get("input").Raw)
		}
		out = append(out, item)
	}
	return out
}

func normalizeCodexWebSearchResultContent(content gjson.Result) []map[string]any {
	var out []map[string]any
	for _, result := range content.Array() {
		out = append(out, map[string]any{
			"type":     result.Get("type").String(),
			"title":    result.Get("title").String(),
			"url":      result.Get("url").String(),
			"page_age": result.Get("page_age").Raw,
		})
	}
	return out
}

func normalizeCodexWebSearchID(id string) string {
	if strings.HasPrefix(id, "web_search_") {
		return "web_search_generated"
	}
	return id
}

func compactJSONRaw(raw string) string {
	if raw == "" {
		return ""
	}
	if !gjson.Valid(raw) {
		return raw
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(compact)
}

func codexWebSearchContentTypes(items []gjson.Result) []string {
	types := make([]string, 0, len(items))
	for _, item := range items {
		types = append(types, item.Get("type").String())
	}
	return types
}

func countStreamTypes(events []gjson.Result, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Get("type").String() == eventType {
			count++
		}
	}
	return count
}

func firstCodexWebSearchStreamIDs(t *testing.T, events []gjson.Result) (string, string) {
	t.Helper()
	var invocationID, resultID string
	for _, event := range events {
		if event.Get("type").String() != "content_block_start" {
			continue
		}
		block := event.Get("content_block")
		switch block.Get("type").String() {
		case "server_tool_use":
			if invocationID == "" {
				invocationID = block.Get("id").String()
			}
		case "web_search_tool_result":
			if resultID == "" {
				resultID = block.Get("tool_use_id").String()
			}
		}
	}
	if invocationID == "" || resultID == "" {
		t.Fatalf("missing web_search ids in events=%v", events)
	}
	return invocationID, resultID
}

func countThinkingDeltaText(events []gjson.Result, text string) int {
	count := 0
	for _, event := range events {
		if event.Get("type").String() == "content_block_delta" &&
			event.Get("delta.type").String() == "thinking_delta" &&
			event.Get("delta.thinking").String() == text {
			count++
		}
	}
	return count
}

func firstEventIndex(events []gjson.Result, pred func(gjson.Result) bool) int {
	for i, event := range events {
		if pred(event) {
			return i
		}
	}
	return -1
}

func collectCodexWebSearchResponsesStream(t *testing.T, original []byte, chunks [][]byte) []gjson.Result {
	t.Helper()
	var param any
	var events []gjson.Result
	for _, chunk := range chunks {
		for _, output := range TranslateStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, chunk, &param) {
			events = append(events, parseCodexWebSearchSSEData(t, output)...)
		}
	}
	return events
}

func responseDoneOutputItems(events []gjson.Result) []gjson.Result {
	var out []gjson.Result
	for _, event := range events {
		if event.Get("type").String() == "response.output_item.done" {
			out = append(out, event.Get("item"))
		}
	}
	return out
}

func lastResponseCompletedOutput(events []gjson.Result) []gjson.Result {
	var out []gjson.Result
	for _, event := range events {
		if event.Get("type").String() == "response.completed" {
			out = event.Get("response.output").Array()
		}
	}
	return out
}

func countDoneSignal(events []gjson.Result) int {
	return countStreamTypes(events, "[DONE]")
}

func countItemsOfType(items []gjson.Result, itemType string) int {
	count := 0
	for _, item := range items {
		if item.Get("type").String() == itemType {
			count++
		}
	}
	return count
}
