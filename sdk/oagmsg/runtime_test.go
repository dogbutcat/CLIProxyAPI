package oagmsg

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type runtimeTestFormat string

func TestRuntimeTranslateRequestAcceptsStringFormats(t *testing.T) {
	output := TranslateRequest(runtimeTestFormat(FormatOpenAI), runtimeTestFormat(FormatGemini), "target-model", []byte(`{
		"model":"source-model","messages":[{"role":"user","content":"hello"}]
	}`), true)
	if gjson.GetBytes(output, "model").String() != "target-model" || !gjson.GetBytes(output, "contents").Exists() {
		t.Fatalf("translated request = %s", output)
	}
}

func TestRuntimeTransformerCapabilities(t *testing.T) {
	if !HasRequestTransformer(FormatOpenAI, FormatAnthropic) {
		t.Fatal("OpenAI to Anthropic request conversion is unavailable")
	}
	if !HasResponseTransformer(FormatAnthropic, FormatOpenAI) {
		t.Fatal("Anthropic to OpenAI response conversion is unavailable")
	}
	if !HasStreamResponseTransformer(FormatAnthropic, FormatOpenAI) {
		t.Fatal("Anthropic to OpenAI stream conversion is unavailable")
	}
	if HasResponseTransformer(Format("plugin-output"), FormatOpenAI) {
		t.Fatal("custom plugin output was reported as a native response conversion")
	}
	if HasStreamResponseTransformer(Format("plugin-output"), FormatOpenAI) {
		t.Fatal("custom plugin output was reported as a native stream conversion")
	}
}

func TestRuntimeTranslateRequestPreservesSameFormatPayload(t *testing.T) {
	input := []byte(`{"model":"source","input":[{"type":"agent_message","extension":{"opaque":true}}]}`)
	output := TranslateRequest(runtimeTestFormat(FormatOpenAIResponse), runtimeTestFormat(FormatOpenAIResponse), "target", input, false)
	if gjson.GetBytes(output, "model").String() != "target" || !gjson.GetBytes(output, "input.0.extension.opaque").Bool() {
		t.Fatalf("same-format output = %s", output)
	}
}

func TestRuntimeTranslateRequestIdentityPath(t *testing.T) {
	input := []byte(`{
		"model":"same-model",
		"messages":[{"role":"user","content":"  spaced value  "}],
		"custom":"identity"
	}`)
	output := TranslateRequest(runtimeTestFormat(FormatOpenAI), runtimeTestFormat(FormatOpenAI), "", input, false)
	if string(output) != string(input) {
		t.Fatalf("identity output changed payload = %s", output)
	}
	if &output[0] != &input[0] {
		t.Fatalf("identity output did not preserve slice: %q", output)
	}
}

func TestRuntimeTranslateRequestFinalizesOpenAINonStreamOptions(t *testing.T) {
	input := []byte(`{"model":"same-model","messages":[{"role":"user","content":"hello"}],"stream_options":{"include_usage":true}}`)
	output := TranslateRequest(FormatOpenAI, FormatOpenAI, "", input, false)
	if gjson.GetBytes(output, "stream_options").Exists() {
		t.Fatalf("non-stream OpenAI request kept stream_options: %s", output)
	}
}

func TestRuntimeTranslateRequestKeepsOpenAIStreamOptionsForStream(t *testing.T) {
	input := []byte(`{"model":"same-model","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":true}}`)
	output := TranslateRequest(FormatOpenAI, FormatOpenAI, "", input, true)
	if !gjson.GetBytes(output, "stream_options.include_usage").Bool() {
		t.Fatalf("stream OpenAI request lost stream_options: %s", output)
	}
}

func TestRuntimeTranslateRequestFinalizesAntigravityNonStreamOptions(t *testing.T) {
	input := []byte(`{"model":"gpt-oss-120b-medium","messages":[{"role":"user","content":"hello"}],"stream_options":{"include_usage":true}}`)
	output := TranslateRequest(FormatOpenAI, FormatAntigravity, "gpt-oss-120b-medium", input, false)
	if gjson.GetBytes(output, "request.stream_options").Exists() || gjson.GetBytes(output, "stream_options").Exists() {
		t.Fatalf("non-stream Antigravity request kept stream_options: %s", output)
	}
}

func TestRuntimeTranslateRequestSameWireLightPath(t *testing.T) {
	input := []byte(`{"model":"source","messages":[{"role":"user","content":"hello"}]}`)
	output := TranslateRequest(runtimeTestFormat(FormatOpenAI), runtimeTestFormat(FormatOpenAI), "target-model", input, false)
	if string(output) == string(input) {
		t.Fatalf("light path did not rewrite model: %s", output)
	}
	if gjson.GetBytes(output, "model").String() != "target-model" {
		t.Fatalf("light path model rewrite missing: %s", output)
	}
}

func TestRuntimeTranslateRequestPreservesResponsesFamilyPayload(t *testing.T) {
	input := []byte(`{"model":"source","input":[{"type":"agent_message","extension":{"opaque":true}}]}`)
	output := TranslateRequest(runtimeTestFormat(FormatOpenAIResponse), runtimeTestFormat(FormatCodex), "target", input, false)
	if gjson.GetBytes(output, "model").String() != "target" || !gjson.GetBytes(output, "input.0.extension.opaque").Bool() {
		t.Fatalf("responses-family output = %s", output)
	}
}

func TestRuntimeTranslateRequestCodexFinalizePathFromResponsesFamily(t *testing.T) {
	input := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]}],"stream":false}`)
	output := TranslateRequest(runtimeTestFormat(FormatOpenAIResponse), FormatCodex, "gpt-5.4", input, false)
	root := gjson.ParseBytes(output)
	if root.Get("stream").Bool() != true {
		t.Fatalf("Codex finalizer not applied: %s", output)
	}
	if root.Get("store").Bool() != false {
		t.Fatalf("Codex finalizer not applied: %s", output)
	}
}

func TestRuntimeTranslateNonStreamUnwrapsResponsesFamilyEvent(t *testing.T) {
	input := []byte(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"item_1","type":"message"}]}}`)
	output := TranslateNonStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", nil, nil, input, nil)
	if gjson.GetBytes(output, "id").String() != "resp_1" || gjson.GetBytes(output, "output.0.id").String() != "item_1" {
		t.Fatalf("responses-family response = %s", output)
	}
}

func TestRuntimeTranslateNonStreamAndTokenCount(t *testing.T) {
	input := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`)
	output := TranslateNonStream(context.Background(), runtimeTestFormat(FormatGemini), runtimeTestFormat(FormatOpenAI), "model", nil, nil, input, nil)
	if gjson.GetBytes(output, "choices.0.message.content").String() != "hello" {
		t.Fatalf("translated response = %s", output)
	}
	count := TranslateTokenCount(context.Background(), runtimeTestFormat(FormatOpenAI), runtimeTestFormat(FormatAnthropic), 12, nil)
	if gjson.GetBytes(count, "input_tokens").Int() != 12 {
		t.Fatalf("token count = %s", count)
	}
}

func TestRuntimeTranslateStreamReusesSession(t *testing.T) {
	var state any
	created := TranslateStream(context.Background(), runtimeTestFormat(FormatCodex), runtimeTestFormat(FormatOpenAI), "model", nil, nil,
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"model"}}`), &state)
	textOutput := TranslateStream(context.Background(), runtimeTestFormat(FormatCodex), runtimeTestFormat(FormatOpenAI), "model", nil, nil,
		[]byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &state)
	if state == nil || len(created) == 0 || len(textOutput) == 0 || !strings.Contains(string(textOutput[0]), "hello") {
		t.Fatalf("state=%T created=%q text=%q", state, created, textOutput)
	}
}

func TestRuntimeTranslateStreamPreservesResponsesFamilyEvent(t *testing.T) {
	input := []byte(`data: {"type":"response.output_item.done","item":{"name":"collaboration"}}`)
	outputs := TranslateStream(context.Background(), FormatCodex, FormatOpenAIResponse, "model", nil, nil, input, nil)
	if len(outputs) != 1 || string(outputs[0]) != string(input) {
		t.Fatalf("responses-family stream = %q", outputs)
	}
}

func TestRuntimeTranslateStreamAutoFlushesTerminalProtocols(t *testing.T) {
	var state any
	usageOutputs := TranslateStream(context.Background(), runtimeTestFormat(FormatAnthropic), runtimeTestFormat(FormatOpenAI), "model", nil, nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`), &state)
	if strings.Contains(string(joinRuntimeOutputs(usageOutputs)), "[DONE]") {
		t.Fatalf("message_delta closed stream early: %q", usageOutputs)
	}
	outputs := TranslateStream(context.Background(), runtimeTestFormat(FormatAnthropic), runtimeTestFormat(FormatOpenAI), "model", nil, nil,
		[]byte(`data: {"type":"message_stop"}`), &state)
	joined := string(joinRuntimeOutputs(outputs))
	if !strings.Contains(joined, "[DONE]") {
		t.Fatalf("terminal outputs = %q", outputs)
	}
}

func TestRuntimeTranslateStreamWaitsForOpenAIDoneAfterFinishReason(t *testing.T) {
	var state any
	finish := TranslateStream(context.Background(), runtimeTestFormat(FormatOpenAI), runtimeTestFormat(FormatAnthropic), "model", nil, nil,
		[]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`), &state)
	if strings.Contains(string(joinRuntimeOutputs(finish)), "message_stop") {
		t.Fatalf("finish output closed stream early: %q", finish)
	}
	usage := TranslateStream(context.Background(), runtimeTestFormat(FormatOpenAI), runtimeTestFormat(FormatAnthropic), "model", nil, nil,
		[]byte(`data: {"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`), &state)
	if len(usage) != 0 {
		t.Fatalf("usage output = %q", usage)
	}
	done := TranslateStream(context.Background(), runtimeTestFormat(FormatOpenAI), runtimeTestFormat(FormatAnthropic), "model", nil, nil,
		[]byte(`data: [DONE]`), &state)
	joined := string(joinRuntimeOutputs(done))
	if !strings.Contains(joined, "message_stop") || !strings.Contains(joined, `"output_tokens":3`) {
		t.Fatalf("done output = %q", done)
	}
}

func TestRuntimeTranslateStreamPreservesCompleteToolArgsToAnthropic(t *testing.T) {
	original := []byte(`{
		"model":"gemini-pro-agent",
		"max_tokens":4096,
		"messages":[{"role":"user","content":"inspect current page"}],
		"tools":[{
			"name":"browser_get_dom",
			"description":"Read the page DOM",
			"input_schema":{
				"type":"object",
				"properties":{"pageId":{"type":"string"}},
				"required":["pageId"]
			}
		}]
	}`)
	tests := []struct {
		name   string
		source Format
		chunks [][]byte
	}{
		{
			name:   "antigravity gemini wrapped function call",
			source: FormatAntigravity,
			chunks: [][]byte{
				[]byte(`data: {"response":{"responseId":"resp_ag_tool","modelVersion":"gemini-pro-default","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"browser_get_dom","args":{"pageId":"page-9"},"id":"call_541623"}}]}}]}}`),
			},
		},
		{
			name:   "native gemini function call",
			source: FormatGemini,
			chunks: [][]byte{
				[]byte(`data: {"responseId":"resp_gemini_tool","modelVersion":"gemini-pro-default","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"browser_get_dom","args":{"pageId":"page-9"},"id":"call_541623"}}]}}]}`),
			},
		},
		{
			name:   "openai compatible first chunk carries arguments",
			source: FormatOpenAI,
			chunks: [][]byte{
				[]byte(`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","created":1773896263,"model":"openai-compatible","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"browser_get_dom","arguments":"{\"pageId\":\"page-9\"}"}}]},"finish_reason":"tool_calls"}]}`),
				[]byte(`data: [DONE]`),
			},
		},
		{
			name:   "responses function arguments done",
			source: FormatOpenAIResponse,
			chunks: [][]byte{
				[]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_resp","name":"browser_get_dom","arguments":""}}`),
				[]byte(`data: {"type":"response.function_call_arguments.done","output_index":0,"call_id":"call_resp","arguments":"{\"pageId\":\"page-9\"}"}`),
				[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_resp","name":"browser_get_dom","arguments":"{\"pageId\":\"page-9\"}"}}`),
				[]byte(`data: {"type":"response.completed","response":{"id":"resp_done","status":"completed","output":[]}}`),
			},
		},
		{
			name:   "codex responses-family function arguments done",
			source: FormatCodex,
			chunks: [][]byte{
				[]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_codex","name":"browser_get_dom","arguments":""}}`),
				[]byte(`data: {"type":"response.function_call_arguments.done","output_index":0,"call_id":"call_codex","arguments":"{\"pageId\":\"page-9\"}"}`),
				[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_codex","name":"browser_get_dom","arguments":"{\"pageId\":\"page-9\"}"}}`),
				[]byte(`data: {"type":"response.completed","response":{"id":"resp_done","status":"completed","output":[]}}`),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translated := TranslateRequest(FormatAnthropic, tt.source, "gemini-pro-agent", original, true)
			var state any
			var outputs [][]byte
			for _, chunk := range tt.chunks {
				outputs = append(outputs, TranslateStream(context.Background(), tt.source, FormatAnthropic, "gemini-pro-agent", original, translated, chunk, &state)...)
			}
			joined := string(joinRuntimeOutputs(outputs))
			assertContains(t, joined, `"type":"input_json_delta"`)
			assertContains(t, joined, `\"pageId\":\"page-9\"`)
			startIndex := strings.Index(joined, `"type":"content_block_start"`)
			deltaIndex := strings.Index(joined, `"type":"content_block_delta"`)
			stopIndex := strings.Index(joined, `"type":"content_block_stop"`)
			if startIndex < 0 || deltaIndex < 0 || stopIndex < 0 || !(startIndex < deltaIndex && deltaIndex < stopIndex) {
				t.Fatalf("tool argument lifecycle order malformed: %s", joined)
			}
		})
	}
}

func joinRuntimeOutputs(values [][]byte) []byte {
	joined := make([]string, len(values))
	for index := range values {
		joined[index] = string(values[index])
	}
	return []byte(strings.Join(joined, "\n"))
}
