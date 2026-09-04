package oagmsg

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	claude_to_responses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	chat_to_responses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

func TestCustomToolResponse_OpenAIChatStreamOracle(t *testing.T) {
	tests := []struct {
		name           string
		original       []byte
		translated     []byte
		chunks         []string
		wantCustom     bool
		wantType       string
		wantName       string
		wantInput      string
		wantCallID     string
		wantNoFunction bool
	}{
		{
			name:           "direct custom late name",
			original:       []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"}]}`),
			chunks:         []string{`data: {"id":"chatcmpl_custom_late_name","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"arguments":""}}]},"finish_reason":null}]}`, `data: {"id":"chatcmpl_custom_late_name","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"exec","arguments":""}}]},"finish_reason":null}]}`, `data: {"id":"chatcmpl_custom_late_name","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantCustom:     true,
			wantType:       "custom_tool_call",
			wantName:       "exec",
			wantInput:      "pwd",
			wantCallID:     "call_exec",
			wantNoFunction: true,
		},
		{
			name:           "custom name and id missing",
			original:       []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"}]}`),
			chunks:         []string{`data: {"id":"chatcmpl_custom_missing_fields","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantCustom:     true,
			wantType:       "custom_tool_call",
			wantName:       "exec",
			wantInput:      "pwd",
			wantCallID:     "call_chatcmpl_custom_missing_fields_0_0",
			wantNoFunction: true,
		},
		{
			name:           "custom call id arrives late",
			original:       []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"}]}`),
			chunks:         []string{`data: {"id":"chatcmpl_custom_late_id","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"exec","arguments":"{\"input\":\"p"}}]},"finish_reason":null}]}`, `data: {"id":"chatcmpl_custom_late_id","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late_id","function":{"arguments":"wd\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantCustom:     true,
			wantType:       "custom_tool_call",
			wantName:       "exec",
			wantInput:      "pwd",
			wantCallID:     "call_late_id",
			wantNoFunction: true,
		},
		{
			name:           "additional namespace custom",
			original:       []byte(`{"model":"gpt-5.4","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec"}]}]}]}`),
			chunks:         []string{`data: {"id":"chatcmpl_additional_namespace_custom_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"terminal__exec","arguments":""}}]},"finish_reason":null}]}`, `data: {"id":"chatcmpl_additional_namespace_custom_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantCustom:     true,
			wantType:       "custom_tool_call",
			wantName:       "exec",
			wantInput:      "pwd",
			wantCallID:     "call_exec",
			wantNoFunction: true,
		},
		{
			name:     "namespace function isolation",
			original: []byte(`{"model":"gpt-5.4","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"terminal","tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{}}}]}]}]}`),
			chunks:   []string{`data: {"id":"chatcmpl_namespace_function","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"terminal__exec","arguments":"{\"cmd\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantType: "function_call",
			wantName: "exec",
		},
		{
			name:       "valid original without tools beats translated custom",
			original:   []byte(`{"model":"gpt-5.4"}`),
			translated: []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"}]}`),
			chunks:     []string{`data: {"id":"chatcmpl_original_no_tools","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantType:   "function_call",
			wantName:   "exec",
		},
		{
			name:     "lone function missing name is not inferred",
			original: []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{}}}]}`),
			chunks:   []string{`data: {"id":"chatcmpl_missing_lone_function","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"path\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantType: "function_call",
			wantName: "",
		},
		{
			name:     "mixed function custom missing name is not inferred",
			original: []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{}}},{"type":"custom","name":"exec"}]}`),
			chunks:   []string{`data: {"id":"chatcmpl_missing_mixed_tools","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`, `data: [DONE]`},
			wantType: "function_call",
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEvents := collectT31OpenAIStreamEvents(t, tt.original, tt.translated, tt.chunks)
			wantEvents := collectT31OpenAIUpstreamEvents(t, tt.original, tt.translated, tt.chunks)
			assertT31StreamProjectionEqual(t, gotEvents, wantEvents)
			assertT31CompletedTool(t, gotEvents, tt.wantType, tt.wantName, tt.wantInput, tt.wantCallID)
			if tt.wantCustom {
				assertT31CustomWireContract(t, gotEvents, tt.wantNoFunction)
			}
		})
	}
}

func TestCustomToolResponse_OpenAIChatNonStreamOracle(t *testing.T) {
	tests := []struct {
		name       string
		original   []byte
		translated []byte
		raw        []byte
		wantType   string
		wantName   string
		wantInput  string
	}{
		{
			name:      "additional namespace custom",
			original:  []byte(`{"model":"gpt-5.4","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"terminal","tools":[{"type":"custom","name":"exec"}]}]}]}`),
			raw:       []byte(`{"id":"chatcmpl_additional_namespace_custom_nonstream","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_exec","type":"function","function":{"name":"terminal__exec","arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`),
			wantType:  "custom_tool_call",
			wantName:  "exec",
			wantInput: "pwd",
		},
		{
			name:     "first expanded function wins custom collision",
			original: []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","parameters":{"type":"object","properties":{}}}]},{"type":"custom","name":"n__x"}]}`),
			raw:      []byte(`{"id":"chatcmpl_custom_winner","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_x","type":"function","function":{"name":"n__x","arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`),
			wantType: "function_call",
			wantName: "x",
		},
		{
			name:       "translated request fallback",
			original:   []byte(`{"model":"gpt-5.4","tools":[`),
			translated: []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"}]}`),
			raw:        []byte(`{"id":"chatcmpl_translated_fallback","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`),
			wantType:   "custom_tool_call",
			wantName:   "exec",
			wantInput:  "pwd",
		},
		{
			name:       "valid original without tools beats translated custom",
			original:   []byte(`{"model":"gpt-5.4"}`),
			translated: []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"}]}`),
			raw:        []byte(`{"id":"chatcmpl_original_no_tools_nonstream","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`),
			wantType:   "function_call",
			wantName:   "exec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranslateNonStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "model", tt.original, tt.translated, tt.raw, nil)
			want := chat_to_responses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "model", tt.original, tt.translated, tt.raw, nil)
			if gotProjection, wantProjection := t31OutputProjection(gjson.GetBytes(got, "output")), t31OutputProjection(gjson.GetBytes(want, "output")); !reflect.DeepEqual(gotProjection, wantProjection) {
				t.Fatalf("non-stream projection mismatch\ngot:  %#v\nwant: %#v\ngotJSON: %s\nwantJSON:%s", gotProjection, wantProjection, got, want)
			}
			if gotType := gjson.GetBytes(want, "output.0.type").String(); gotType != tt.wantType {
				t.Fatalf("upstream tool type = %q, want %q; upstream=%s", gotType, tt.wantType, want)
			}
			if gotName := gjson.GetBytes(want, "output.0.name").String(); gotName != tt.wantName {
				t.Fatalf("upstream tool name = %q, want %q; upstream=%s", gotName, tt.wantName, want)
			}
			assertT31OutputTool(t, gjson.GetBytes(got, "output.0"), tt.wantType, tt.wantName, tt.wantInput, "")
		})
	}
}

func TestCustomToolResponse_ClaudeStreamAndNonStreamOracle(t *testing.T) {
	tests := []struct {
		name       string
		original   []byte
		chunks     []string
		wantType   string
		wantName   string
		wantInput  string
		wantCustom bool
	}{
		{
			name:       "additional custom",
			original:   []byte(`{"model":"gpt-test","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]}]}`),
			chunks:     []string{`data: {"type":"message_start","message":{"id":"msg_custom","usage":{"input_tokens":1,"output_tokens":0}}}`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom","name":"exec","input":{}}}`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`, `data: {"type":"content_block_stop","index":0}`, `data: {"type":"message_stop"}`},
			wantType:   "custom_tool_call",
			wantName:   "exec",
			wantInput:  "pwd",
			wantCustom: true,
		},
		{
			name:       "direct custom wins namespace collision",
			original:   []byte(`{"model":"gpt-test","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x"}]},{"type":"custom","name":"n__x"}]}`),
			chunks:     []string{`data: {"type":"message_start","message":{"id":"msg_collision","usage":{"input_tokens":1,"output_tokens":0}}}`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_collision","name":"n__x","input":{}}}`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`, `data: {"type":"content_block_stop","index":0}`, `data: {"type":"message_stop"}`},
			wantType:   "custom_tool_call",
			wantName:   "n__x",
			wantInput:  "pwd",
			wantCustom: true,
		},
		{
			name:       "empty custom input",
			original:   []byte(`{"model":"gpt-test","tools":[{"type":"custom","name":"exec"}]}`),
			chunks:     []string{`data: {"type":"message_start","message":{"id":"msg_custom_empty","usage":{"input_tokens":1,"output_tokens":0}}}`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_empty","name":"exec","input":{}}}`, `data: {"type":"content_block_stop","index":0}`, `data: {"type":"message_stop"}`},
			wantType:   "custom_tool_call",
			wantName:   "exec",
			wantInput:  "",
			wantCustom: true,
		},
		{
			name:      "namespace function isolation",
			original:  []byte(`{"model":"gpt-test","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","parameters":{"type":"object","properties":{}}}]}]}`),
			chunks:    []string{`data: {"type":"message_start","message":{"id":"msg_func","usage":{"input_tokens":1,"output_tokens":0}}}`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_func","name":"n__x","input":{}}}`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"arg\":1}"}}`, `data: {"type":"content_block_stop","index":0}`, `data: {"type":"message_stop"}`},
			wantType:  "function_call",
			wantName:  "x",
			wantInput: "",
		},
		{
			name:      "custom missing name is not inferred for claude",
			original:  []byte(`{"model":"gpt-test","tools":[{"type":"custom","name":"exec"}]}`),
			chunks:    []string{`data: {"type":"message_start","message":{"id":"msg_missing_name","usage":{"input_tokens":1,"output_tokens":0}}}`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_missing_name","input":{}}}`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`, `data: {"type":"content_block_stop","index":0}`, `data: {"type":"message_stop"}`},
			wantType:  "function_call",
			wantName:  "",
			wantInput: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEvents := collectT31ClaudeStreamEvents(t, tt.original, tt.chunks)
			wantEvents := collectT31ClaudeUpstreamEvents(t, tt.original, tt.chunks)
			assertT31StreamProjectionEqual(t, gotEvents, wantEvents)
			assertT31CompletedTool(t, gotEvents, tt.wantType, tt.wantName, tt.wantInput, "")
			if tt.wantCustom {
				assertT31CustomWireContract(t, gotEvents, true)
			}

			raw := []byte(strings.Join(tt.chunks, "\n"))
			got := TranslateNonStream(context.Background(), FormatAnthropic, FormatOpenAIResponse, "model", tt.original, nil, raw, nil)
			want := claude_to_responses.ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "model", tt.original, nil, raw, nil)
			if gotProjection, wantProjection := t31OutputProjection(gjson.GetBytes(got, "output")), t31OutputProjection(gjson.GetBytes(want, "output")); !reflect.DeepEqual(gotProjection, wantProjection) {
				t.Fatalf("claude non-stream projection mismatch\ngot:  %#v\nwant: %#v\ngotJSON: %s\nwantJSON:%s", gotProjection, wantProjection, got, want)
			}
		})
	}
}

func TestCustomToolResponse_OpenAIChatStreamMultipleCallOrderingOracle(t *testing.T) {
	original := []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"},{"type":"function","name":"read","parameters":{"type":"object","properties":{}}}]}`)
	chunks := []string{
		`data: {"id":"chatcmpl_multi_tool","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"exec","arguments":""}},{"index":1,"id":"call_read","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_multi_tool","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"pwd\"}"}},{"index":1,"function":{"arguments":"\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
	gotEvents := collectT31OpenAIStreamEvents(t, original, nil, chunks)
	wantEvents := collectT31OpenAIUpstreamEvents(t, original, nil, chunks)
	assertT31StreamProjectionEqual(t, gotEvents, wantEvents)
	assertT31CompletedCount(t, gotEvents, 1)
	assertT31ToolEvent(t, gotEvents, "response.output_item.added", "ctc_call_exec", "custom_tool_call", "0")
	assertT31ToolEvent(t, gotEvents, "response.custom_tool_call_input.done", "ctc_call_exec", "", "0")
	assertT31ToolEvent(t, gotEvents, "response.output_item.done", "ctc_call_exec", "custom_tool_call", "0")
	assertT31ToolEvent(t, gotEvents, "response.output_item.added", "fc_call_read", "function_call", "1")
	assertT31ToolEvent(t, gotEvents, "response.function_call_arguments.done", "fc_call_read", "", "1")
	assertT31ToolEvent(t, gotEvents, "response.output_item.done", "fc_call_read", "function_call", "1")
	output := completedOutputFromEvents(t, gotEvents)
	if len(output) != 2 {
		t.Fatalf("completed output length = %d, want 2: %v", len(output), output)
	}
	assertT31OutputTool(t, output[0], "custom_tool_call", "exec", "pwd", "call_exec")
	assertT31OutputTool(t, output[1], "function_call", "read", "", "call_read")
}

func TestCustomToolResponse_OpenAIChatFunctionWinnerBeforeCustom(t *testing.T) {
	original := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"n__x","parameters":{"type":"object","properties":{}}},{"type":"custom","name":"n__x"}]}`)
	raw := []byte(`{"id":"chatcmpl_function_winner","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_x","type":"function","function":{"name":"n__x","arguments":"{\"arg\":1}"}}]},"finish_reason":"tool_calls"}]}`)
	got := TranslateNonStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "model", original, nil, raw, nil)
	assertT31OutputTool(t, gjson.GetBytes(got, "output.0"), "function_call", "n__x", "", "call_x")
}

func TestCustomToolResponse_OpenAIMissingIDNonResponsesIsolation(t *testing.T) {
	var state any
	out := TranslateStream(context.Background(), FormatOpenAI, FormatAnthropic, "model", nil, nil,
		[]byte(`data: {"id":"chatcmpl_missing_id_isolation","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"exec","arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`), &state)
	joined := string(joinRuntimeOutputs(out))
	if strings.Contains(joined, "call_chatcmpl_missing_id_isolation") {
		t.Fatalf("non-Responses target received synthesized tool id: %s", joined)
	}
}

func TestCustomToolResponse_ClaudeStreamMessageDeltaUsageThenStopOracle(t *testing.T) {
	original := []byte(`{"model":"gpt-test","tools":[{"type":"custom","name":"exec"}]}`)
	chunks := []string{
		`data: {"type":"message_start","message":{"id":"msg_usage_stop","usage":{"input_tokens":3,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_exec","name":"exec","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`data: {"type":"message_stop"}`,
	}
	gotEvents := collectT31ClaudeStreamEvents(t, original, chunks)
	wantEvents := collectT31ClaudeUpstreamEvents(t, original, chunks)
	assertT31StreamProjectionEqual(t, gotEvents, wantEvents)
	assertT31CompletedCount(t, gotEvents, 1)
	completed := lastT31CompletedEvent(t, gotEvents)
	if got := completed.Get("response.usage.output_tokens").Int(); got != 7 {
		t.Fatalf("completed output_tokens = %d, want 7; event=%s", got, completed.Raw)
	}
}

func collectT31OpenAIStreamEvents(t *testing.T, original, translated []byte, chunks []string) []signatureStreamEvent {
	t.Helper()
	var param any
	var events []signatureStreamEvent
	for _, chunk := range chunks {
		for _, output := range TranslateStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "model", original, translated, []byte(chunk), &param) {
			event, data := parseSignatureSSE(t, output)
			events = append(events, signatureStreamEvent{event: event, data: data})
		}
	}
	return events
}

func collectT31OpenAIUpstreamEvents(t *testing.T, original, translated []byte, chunks []string) []signatureStreamEvent {
	t.Helper()
	var param any
	var events []signatureStreamEvent
	for _, chunk := range chunks {
		for _, output := range chat_to_responses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", original, translated, []byte(chunk), &param) {
			event, data := parseSignatureSSE(t, output)
			events = append(events, signatureStreamEvent{event: event, data: data})
		}
	}
	return events
}

func collectT31ClaudeStreamEvents(t *testing.T, original []byte, chunks []string) []signatureStreamEvent {
	t.Helper()
	var param any
	var events []signatureStreamEvent
	for _, chunk := range chunks {
		for _, output := range TranslateStream(context.Background(), FormatAnthropic, FormatOpenAIResponse, "model", original, nil, []byte(chunk), &param) {
			event, data := parseSignatureSSE(t, output)
			events = append(events, signatureStreamEvent{event: event, data: data})
		}
	}
	return events
}

func collectT31ClaudeUpstreamEvents(t *testing.T, original []byte, chunks []string) []signatureStreamEvent {
	t.Helper()
	var param any
	var events []signatureStreamEvent
	for _, chunk := range chunks {
		for _, output := range claude_to_responses.ConvertClaudeResponseToOpenAIResponses(context.Background(), "model", original, nil, []byte(chunk), &param) {
			event, data := parseSignatureSSE(t, output)
			events = append(events, signatureStreamEvent{event: event, data: data})
		}
	}
	return events
}

func assertT31StreamProjectionEqual(t *testing.T, got, want []signatureStreamEvent) {
	t.Helper()
	gotProjection := t31StreamProjection(got)
	wantProjection := t31StreamProjection(want)
	if !reflect.DeepEqual(gotProjection, wantProjection) {
		t.Fatalf("stream projection mismatch\ngot:  %#v\nwant: %#v", gotProjection, wantProjection)
	}
}

func t31StreamProjection(events []signatureStreamEvent) []map[string]string {
	var projection []map[string]string
	completedCount := 0
	for _, evt := range events {
		switch evt.event {
		case "response.output_item.added":
			projection = append(projection, t31EventToolProjection(evt.event, evt.data.Get("item"), evt.data.Get("output_index").String()))
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			projection = append(projection, map[string]string{
				"event":      evt.event,
				"item_id":    evt.data.Get("item_id").String(),
				"output_idx": evt.data.Get("output_index").String(),
			})
		case "response.custom_tool_call_input.done":
			projection = append(projection, map[string]string{
				"event":      evt.event,
				"item_id":    evt.data.Get("item_id").String(),
				"output_idx": evt.data.Get("output_index").String(),
				"input":      evt.data.Get("input").String(),
			})
		case "response.output_item.done":
			projection = append(projection, t31EventToolProjection(evt.event, evt.data.Get("item"), evt.data.Get("output_index").String()))
		case "response.completed":
			completedCount++
			for outputIndex, item := range evt.data.Get("response.output").Array() {
				projection = append(projection, t31EventToolProjection(evt.event, item, strconv.Itoa(outputIndex)))
			}
		}
	}
	projection = append(projection, map[string]string{
		"event": "response.completed.count",
		"count": strconv.Itoa(completedCount),
	})
	return projection
}

func t31OutputProjection(output gjson.Result) []map[string]string {
	var projection []map[string]string
	for outputIndex, item := range output.Array() {
		projection = append(projection, t31EventToolProjection("output", item, strconv.Itoa(outputIndex)))
	}
	return projection
}

func t31EventToolProjection(event string, item gjson.Result, outputIndex string) map[string]string {
	return map[string]string{
		"event":      event,
		"output_idx": outputIndex,
		"type":       item.Get("type").String(),
		"id":         item.Get("id").String(),
		"call_id":    item.Get("call_id").String(),
		"name":       item.Get("name").String(),
		"input":      item.Get("input").String(),
		"arguments":  item.Get("arguments").String(),
	}
}

func assertT31CustomWireContract(t *testing.T, events []signatureStreamEvent, wantNoFunction bool) {
	t.Helper()
	var sawInputDone bool
	for _, evt := range events {
		switch evt.event {
		case "response.output_item.added", "response.output_item.done":
			if item := evt.data.Get("item"); item.Get("type").String() == "custom_tool_call" {
				if !strings.HasPrefix(item.Get("id").String(), "ctc_") {
					t.Fatalf("custom item id = %q, want ctc_ prefix", item.Get("id").String())
				}
			}
		case "response.custom_tool_call_input.done":
			sawInputDone = true
			if !strings.HasPrefix(evt.data.Get("item_id").String(), "ctc_") {
				t.Fatalf("custom input item_id = %q, want ctc_ prefix", evt.data.Get("item_id").String())
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if wantNoFunction {
				t.Fatalf("unexpected function argument lifecycle for custom tool: %s", evt.event)
			}
		}
	}
	if !sawInputDone {
		t.Fatal("missing response.custom_tool_call_input.done")
	}
}

func assertT31CompletedCount(t *testing.T, events []signatureStreamEvent, want int) {
	t.Helper()
	got := 0
	for _, evt := range events {
		if evt.event == "response.completed" {
			got++
		}
	}
	if got != want {
		t.Fatalf("response.completed count = %d, want %d", got, want)
	}
}

func assertT31ToolEvent(t *testing.T, events []signatureStreamEvent, eventType, itemID, itemType, outputIndex string) {
	t.Helper()
	for _, evt := range events {
		if evt.event != eventType || evt.data.Get("output_index").String() != outputIndex {
			continue
		}
		if eventType == "response.custom_tool_call_input.done" || strings.HasPrefix(eventType, "response.function_call_arguments.") {
			if evt.data.Get("item_id").String() == itemID {
				return
			}
			continue
		}
		item := evt.data.Get("item")
		if item.Get("id").String() == itemID && item.Get("type").String() == itemType {
			return
		}
	}
	t.Fatalf("missing %s item_id=%s type=%s output_index=%s", eventType, itemID, itemType, outputIndex)
}

func lastT31CompletedEvent(t *testing.T, events []signatureStreamEvent) gjson.Result {
	t.Helper()
	var completed gjson.Result
	for _, evt := range events {
		if evt.event == "response.completed" {
			completed = evt.data
		}
	}
	if !completed.Exists() {
		t.Fatal("response.completed missing")
	}
	return completed
}

func assertT31CompletedTool(t *testing.T, events []signatureStreamEvent, wantType, wantName, wantInput, wantCallID string) {
	t.Helper()
	output := completedOutputFromEvents(t, events)
	if len(output) == 0 {
		t.Fatal("completed output is empty")
	}
	assertT31OutputTool(t, output[0], wantType, wantName, wantInput, wantCallID)
}

func assertT31OutputTool(t *testing.T, item gjson.Result, wantType, wantName, wantInput, wantCallID string) {
	t.Helper()
	if got := item.Get("type").String(); got != wantType {
		t.Fatalf("tool type = %q, want %q; item=%s", got, wantType, item.Raw)
	}
	if got := item.Get("name").String(); got != wantName {
		t.Fatalf("tool name = %q, want %q; item=%s", got, wantName, item.Raw)
	}
	if wantInput != "" || wantType == "custom_tool_call" {
		if got := item.Get("input").String(); got != wantInput {
			t.Fatalf("tool input = %q, want %q; item=%s", got, wantInput, item.Raw)
		}
	}
	if wantCallID != "" {
		if got := item.Get("call_id").String(); got != wantCallID {
			t.Fatalf("tool call_id = %q, want %q; item=%s", got, wantCallID, item.Raw)
		}
	}
}
