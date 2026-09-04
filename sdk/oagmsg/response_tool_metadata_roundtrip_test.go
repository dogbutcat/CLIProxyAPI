package oagmsg

import (
	"context"
	"reflect"
	"strings"
	"testing"

	codexchat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/tidwall/gjson"
)

func TestToolMetadataCodexResponseRoundTrip_RestoresShortNamesAcrossResponseModes(t *testing.T) {
	const limit = codexToolNameLimitBytes
	functionLong := strings.Repeat("a", limit) + "_function"
	customLong := strings.Repeat("a", limit) + "_custom"
	original := []byte(`{"model":"gpt-5.4","tools":[` +
		`{"type":"function","name":"` + functionLong + `","parameters":{"type":"object","properties":{}}},` +
		`{"type":"custom","name":"` + customLong + `"}` +
		`]}`)
	metadata := buildRequestToolMetadataFromRequests(original)
	shortCustom := metadata.toolNameForward[customLong]
	if shortCustom == "" || shortCustom == customLong {
		t.Fatalf("fixture did not allocate a Codex short custom name: %+v", metadata.toolNameForward)
	}

	directRaw := []byte(`{"id":"resp_direct","object":"response","status":"completed","model":"codex","output":[` +
		`{"type":"function_call","call_id":"call_custom","name":"` + shortCustom + `","arguments":"{\"input\":\"pwd\"}"}` +
		`]}`)
	direct := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, directRaw, nil)
	assertT31OutputTool(t, gjson.GetBytes(direct, "output.0"), "custom_tool_call", customLong, "pwd", "call_custom")

	streamChunks := []string{
		`data: {"type":"response.created","response":{"id":"resp_stream","model":"codex","created_at":1773896263}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_call_custom","type":"function_call","status":"in_progress","call_id":"call_custom","name":"` + shortCustom + `"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"call_id":"call_custom","delta":"{\"input\":\"p"}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"call_id":"call_custom","delta":"wd\"}"}`,
		`data: {"type":"response.function_call_arguments.done","output_index":0,"call_id":"call_custom","arguments":""}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_call_custom","type":"function_call","status":"completed","call_id":"call_custom","name":"` + shortCustom + `","arguments":"{\"input\":\"pwd\"}"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_stream","status":"completed","model":"codex","output":[]}}`,
	}
	aggregate := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, []byte(strings.Join(streamChunks, "\n")), nil)
	assertT31OutputTool(t, gjson.GetBytes(aggregate, "output.0"), "custom_tool_call", customLong, "pwd", "call_custom")

	events := collectT42CodexStreamEvents(t, original, nil, streamChunks)
	assertT31CompletedCount(t, events, 1)
	assertT31CompletedTool(t, events, "custom_tool_call", customLong, "pwd", "call_custom")
	if gotProjection, wantProjection := t31OutputProjection(gjson.GetBytes(aggregate, "output")), t31OutputProjection(gjson.GetBytes(direct, "output")); !reflect.DeepEqual(gotProjection, wantProjection) {
		t.Fatalf("Codex aggregate/direct projection mismatch\ngot:  %#v\nwant: %#v\naggregate: %s\ndirect:    %s", gotProjection, wantProjection, aggregate, direct)
	}

	terminalChunk := `data: {"type":"response.completed","response":{"id":"resp_terminal","object":"response","status":"completed","model":"codex","output":[` +
		`{"type":"function_call","call_id":"call_custom","name":"` + shortCustom + `","arguments":"{\"input\":\"pwd\"}"}` +
		`]}}`
	terminalAggregate := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, []byte(terminalChunk), nil)
	assertT31OutputTool(t, gjson.GetBytes(terminalAggregate, "output.0"), "custom_tool_call", customLong, "pwd", "call_custom")
	terminalEvents := collectT42CodexStreamEvents(t, original, nil, []string{terminalChunk})
	assertT31CompletedCount(t, terminalEvents, 1)
	assertT31CompletedTool(t, terminalEvents, "custom_tool_call", customLong, "pwd", "call_custom")
}

func TestToolMetadataCodexResponseRoundTrip_RestoresNamespaceFunctionAndCustomWinners(t *testing.T) {
	original := []byte(`{"model":"gpt-5.4","tools":[` +
		`{"type":"namespace","name":"shell","tools":[{"type":"function","name":"run","parameters":{"type":"object","properties":{}}}]},` +
		`{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","parameters":{"type":"object","properties":{}}}]},` +
		`{"type":"custom","name":"n__x"}` +
		`]}`)
	raw := []byte(`{"id":"resp_namespace","object":"response","status":"completed","model":"codex","output":[` +
		`{"type":"function_call","call_id":"call_func","name":"shell__run","arguments":"{\"cmd\":\"pwd\"}"},` +
		`{"type":"function_call","call_id":"call_custom","name":"n__x","arguments":"{\"input\":\"pwd\"}"}` +
		`]}`)

	got := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, raw, nil)
	functionCall := gjson.GetBytes(got, "output.0")
	assertT31OutputTool(t, functionCall, "function_call", "run", "", "call_func")
	if namespace := functionCall.Get("namespace").String(); namespace != "shell" {
		t.Fatalf("namespace function namespace = %q, want shell; output=%s", namespace, got)
	}
	assertT31OutputTool(t, gjson.GetBytes(got, "output.1"), "custom_tool_call", "n__x", "pwd", "call_custom")

	streamChunks := []string{
		`data: {"type":"response.created","response":{"id":"resp_namespace_stream","model":"codex","created_at":1773896263}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_call_custom","type":"function_call","status":"completed","call_id":"call_custom","name":"n__x","arguments":"{\"input\":\"pwd\"}"}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_call_func","type":"function_call","status":"completed","call_id":"call_func","name":"shell__run","arguments":"{\"cmd\":\"pwd\"}"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_namespace_stream","status":"completed","model":"codex","output":[]}}`,
	}
	events := collectT42CodexStreamEvents(t, original, nil, streamChunks)
	output := completedOutputFromEvents(t, events)
	if len(output) != 2 {
		t.Fatalf("stream completed output length = %d, want 2; output=%v", len(output), output)
	}
	assertT31OutputTool(t, output[0], "custom_tool_call", "n__x", "pwd", "call_custom")
	assertT31OutputTool(t, output[1], "function_call", "run", "", "call_func")
	if namespace := output[1].Get("namespace").String(); namespace != "shell" {
		t.Fatalf("stream namespace function namespace = %q, want shell; output=%s", namespace, output[1].Raw)
	}
}

func TestToolMetadataCodexResponseRoundTrip_UnknownFallbackAndNonCodexIsolation(t *testing.T) {
	const limit = codexToolNameLimitBytes
	knownLong := strings.Repeat("mcp_server_", 8) + "__known"
	translated := []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"` + knownLong + `"}]}`)
	metadata := buildRequestToolMetadataFromRequests(translated)
	shortKnown := metadata.toolNameForward[knownLong]
	if shortKnown == "" || shortKnown == knownLong {
		t.Fatalf("fixture did not allocate short name: %+v", metadata.toolNameForward)
	}

	t.Run("invalid original falls back to translated request", func(t *testing.T) {
		raw := []byte(`{"id":"resp_fallback","object":"response","status":"completed","model":"codex","output":[` +
			`{"type":"function_call","call_id":"call_known","name":"` + shortKnown + `","arguments":"{\"input\":\"pwd\"}"}` +
			`]}`)
		got := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", []byte(`{"tools":[`), translated, raw, nil)
		assertT31OutputTool(t, gjson.GetBytes(got, "output.0"), "custom_tool_call", knownLong, "pwd", "call_known")
	})

	t.Run("unknown Codex names pass through unchanged", func(t *testing.T) {
		raw := []byte(`{"id":"resp_unknown","object":"response","status":"completed","model":"codex","output":[` +
			`{"type":"function_call","call_id":"call_unknown","name":"unknown_short","arguments":"{}"}` +
			`]}`)
		got := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", translated, nil, raw, nil)
		assertT31OutputTool(t, gjson.GetBytes(got, "output.0"), "function_call", "unknown_short", "", "call_unknown")
	})

	t.Run("non-Codex responses do not reverse generated short names", func(t *testing.T) {
		raw := []byte(`{"id":"chatcmpl_false_positive","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_known","type":"function","function":{"name":"` + shortKnown + `","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		got := TranslateNonStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "model", translated, nil, raw, nil)
		assertT31OutputTool(t, gjson.GetBytes(got, "output.0"), "function_call", shortKnown, "", "call_known")
	})

	t.Run("x_search only Responses family remains pass-through", func(t *testing.T) {
		original := []byte(`{"model":"grok-4.5","tools":[{"type":"x_search"}]}`)
		raw := []byte(`{"id":"resp_xsearch_only","object":"response","status":"completed","model":"grok-4.5","output":[` +
			`{"type":"function_call","call_id":"call_internal","name":"x_keyword_search","arguments":"{}"}` +
			`]}`)
		if got := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, raw, nil); string(got) != string(raw) {
			t.Fatalf("x_search-only non-stream changed\ngot:  %s\nwant: %s", got, raw)
		}

		var param any
		event := []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_internal","type":"function_call","call_id":"call_internal","name":"x_keyword_search","arguments":"{}","status":"completed"}}`)
		outputs := TranslateStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, event, &param)
		if len(outputs) != 1 || string(outputs[0]) != string(event) {
			t.Fatalf("x_search-only stream changed\ngot:  %q\nwant: %q", outputs, event)
		}
	})

	t.Run("x_search plus long tools restores declared tools only", func(t *testing.T) {
		longFunction := strings.Repeat("function_tool_", 6) + "run"
		original := []byte(`{"model":"grok-4.5","tools":[` +
			`{"type":"x_search"},` +
			`{"type":"function","name":"` + longFunction + `","parameters":{"type":"object","properties":{}}},` +
			`{"type":"custom","name":"` + knownLong + `"}` +
			`]}`)
		xSearchMetadata := buildRequestToolMetadataFromRequests(original)
		shortFunction := xSearchMetadata.toolNameForward[longFunction]
		shortCustom := xSearchMetadata.toolNameForward[knownLong]
		if shortFunction == "" || shortFunction == longFunction || shortCustom == "" || shortCustom == knownLong {
			t.Fatalf("fixture did not allocate Codex short names: %+v", xSearchMetadata.toolNameForward)
		}
		raw := []byte(`{"id":"resp_xsearch_mixed","object":"response","status":"completed","model":"grok-4.5","output":[` +
			`{"type":"function_call","call_id":"call_internal","name":"x_keyword_search","arguments":"{}"},` +
			`{"type":"function_call","call_id":"call_function","name":"` + shortFunction + `","arguments":"{\"q\":\"ok\"}"},` +
			`{"type":"function_call","call_id":"call_custom","name":"` + shortCustom + `","arguments":"{\"input\":\"pwd\"}"}` +
			`]}`)
		got := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, raw, nil)
		assertT31OutputTool(t, gjson.GetBytes(got, "output.0"), "function_call", "x_keyword_search", "", "call_internal")
		assertT31OutputTool(t, gjson.GetBytes(got, "output.1"), "function_call", longFunction, "", "call_function")
		assertT31OutputTool(t, gjson.GetBytes(got, "output.2"), "custom_tool_call", knownLong, "pwd", "call_custom")
	})

	t.Run("x_search identity custom function call restores to custom", func(t *testing.T) {
		original := []byte(`{"model":"grok-4.5","tools":[{"type":"x_search"},{"type":"custom","name":"x_keyword_search"}]}`)
		raw := []byte(`{"id":"resp_xsearch_identity","object":"response","status":"completed","model":"grok-4.5","output":[` +
			`{"type":"function_call","call_id":"call_custom","name":"x_keyword_search","arguments":"{}"}` +
			`]}`)
		got := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, nil, raw, nil)
		assertT31OutputTool(t, gjson.GetBytes(got, "output.0"), "custom_tool_call", "x_keyword_search", "{}", "call_custom")
	})

	t.Run("Responses message done does not duplicate streamed text", func(t *testing.T) {
		original := []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"` + knownLong + `"}]}`)
		chunks := []string{
			`data: {"type":"response.created","response":{"id":"resp_text","model":"codex","created_at":1773896263}}`,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_text","status":"completed","model":"codex","output":[]}}`,
		}
		events := collectT42CodexStreamEvents(t, original, nil, chunks)
		var streamedText string
		for _, event := range events {
			if event.event == "response.output_text.delta" {
				streamedText += event.data.Get("delta").String()
			}
		}
		if streamedText != "hello" {
			t.Fatalf("streamed text = %q, want hello; events=%v", streamedText, events)
		}
		output := completedOutputFromEvents(t, events)
		if len(output) != 1 {
			t.Fatalf("completed output length = %d, want 1; output=%v", len(output), output)
		}
		if text := output[0].Get("content.0.text").String(); text != "hello" {
			t.Fatalf("completed message text = %q, want hello; output=%s", text, output[0].Raw)
		}
	})
}

func TestToolMetadataCodexResponseRoundTrip_OpenAIChatUpstreamOracle(t *testing.T) {
	longFunction := strings.Repeat("oracle_function_", 5) + "run"
	original := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"run"}],"tools":[` +
		`{"type":"function","function":{"name":"` + longFunction + `","parameters":{"type":"object","properties":{}}}}` +
		`]}`)
	metadata := buildRequestToolMetadataFromRequests(original)
	shortFunction := metadata.toolNameForward[longFunction]
	if shortFunction == "" || shortFunction == longFunction {
		t.Fatalf("fixture did not allocate Codex short function name: %+v", metadata.toolNameForward)
	}
	chunk := []byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_oracle","type":"function_call","status":"in_progress","call_id":"call_oracle","name":"` + shortFunction + `"}}`)

	var upstreamParam any
	upstream := codexchat.ConvertCodexResponseToOpenAI(context.Background(), "model", original, nil, chunk, &upstreamParam)
	var oagParam any
	got := TranslateStream(context.Background(), FormatCodex, FormatOpenAI, "model", original, nil, chunk, &oagParam)
	if len(upstream) != 1 || len(got) != 1 {
		t.Fatalf("oracle output count mismatch: upstream=%d oag=%d", len(upstream), len(got))
	}
	upstreamName := gjson.GetBytes(upstream[0], "choices.0.delta.tool_calls.0.function.name").String()
	oagName := gjson.GetBytes(got[0], "choices.0.delta.tool_calls.0.function.name").String()
	if upstreamName != longFunction || oagName != upstreamName {
		t.Fatalf("Codex->OpenAI Chat oracle name mismatch: upstream=%q oag=%q\nupstreamJSON=%s\noagJSON=%s", upstreamName, oagName, upstream[0], got[0])
	}
}

func collectT42CodexStreamEvents(t *testing.T, original, translated []byte, chunks []string) []signatureStreamEvent {
	t.Helper()
	var param any
	var events []signatureStreamEvent
	for _, chunk := range chunks {
		for _, output := range TranslateStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", original, translated, []byte(chunk), &param) {
			event, data := parseSignatureSSE(t, output)
			events = append(events, signatureStreamEvent{event: event, data: data})
		}
	}
	return events
}
