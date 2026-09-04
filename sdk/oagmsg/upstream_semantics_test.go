package oagmsg

import (
	"bytes"
	"context"
	"strings"
	"testing"

	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

func TestUpstreamCodexResponsesStripsNestedPromptCacheBreakpoints(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.2",
		"input":[
			{"type":"message","role":"system","content":[
				{"type":"input_text","text":"System prompt","prompt_cache_breakpoint":{"mode":"explicit"}}
			]},
			{"type":"message","role":"user","content":[
				{"type":"input_text","text":"Hello world","prompt_cache_breakpoint":{"mode":"explicit"}},
				{"type":"input_text","text":"Second part"}
			]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatCodex, "gpt-5.2", raw, false)
	if strings.Contains(string(out), "prompt_cache_breakpoint") {
		t.Fatalf("prompt_cache_breakpoint survived Codex finalization: %s", out)
	}
	if got := gjson.GetBytes(out, "input.0.role").String(); got != "developer" {
		t.Fatalf("system role = %q, want developer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "System prompt" {
		t.Fatalf("system text = %q, want preserved; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "input.1.content.0.text").String(); got != "Hello world" {
		t.Fatalf("user text = %q, want preserved; output=%s", got, out)
	}
	if !codexRequestAlreadyFinalized(out) {
		t.Fatalf("request is not recognized as finalized after cleanup: %s", out)
	}
}

func TestUpstreamCodexStrictJSONSchemaDowngradesOptionalProperties(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.2",
		"input":"return JSON",
		"text":{"format":{
			"type":"json_schema",
			"name":"result",
			"strict":true,
			"schema":{
				"type":"object",
				"properties":{
					"required_value":{"type":"string"},
					"optional_value":{"type":"string"}
				},
				"required":["required_value"]
			}
		}}
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatCodex, "gpt-5.2", raw, false)
	if got := gjson.GetBytes(out, "text.format.strict").Bool(); got {
		t.Fatalf("strict remained true for schema with optional property: %s", out)
	}
	if !codexRequestAlreadyFinalized(out) {
		t.Fatalf("request is not recognized as finalized after strict downgrade: %s", out)
	}
}

func TestUpstreamClaudeRefusalSensitiveStopReasonsMapToContentFilter(t *testing.T) {
	for _, reason := range []string{"refusal", "sensitive"} {
		t.Run("nonstream_"+reason, func(t *testing.T) {
			raw := []byte(`{"id":"msg_1","model":"claude-test","stop_reason":"` + reason + `","content":[]}`)
			out := TranslateNonStream(context.Background(), FormatAnthropic, FormatOpenAI, "gpt-test", nil, nil, raw, nil)
			if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "content_filter" {
				t.Fatalf("finish_reason = %q, want content_filter; output=%s", got, out)
			}
		})
		t.Run("stream_"+reason, func(t *testing.T) {
			var state any
			chunks := [][]byte{
				[]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test"}}`),
				[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"` + reason + `"},"usage":{"output_tokens":1}}`),
				[]byte(`data: {"type":"message_stop"}`),
			}
			var out [][]byte
			for _, chunk := range chunks {
				out = append(out, TranslateStream(context.Background(), FormatAnthropic, FormatOpenAI, "gpt-test", nil, nil, chunk, &state)...)
			}
			joined := bytes.Join(out, nil)
			if !bytes.Contains(joined, []byte(`"finish_reason":"content_filter"`)) {
				t.Fatalf("stream finish_reason did not map to content_filter: %s", joined)
			}
		})
	}
}

func TestUpstreamOpenAIChatToResponsesNonStreamIncompleteFinishReasons(t *testing.T) {
	tests := []struct {
		name       string
		finish     string
		wantReason string
	}{
		{name: "length", finish: "length", wantReason: "max_output_tokens"},
		{name: "content filter", finish: "content_filter", wantReason: "content_filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1773896263,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"` + tt.finish + `"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			out := TranslateNonStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "gpt-test", nil, nil, raw, nil)
			if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
				t.Fatalf("status = %q, want incomplete; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "incomplete_details.reason").String(); got != tt.wantReason {
				t.Fatalf("incomplete reason = %q, want %q; output=%s", got, tt.wantReason, out)
			}
			if got := gjson.GetBytes(out, "output.0.status").String(); got != "incomplete" {
				t.Fatalf("output status = %q, want incomplete; output=%s", got, out)
			}
		})
	}
}

func TestUpstreamOpenAIChatToResponsesStreamPartialToolDoesNotFinalizeOnBareDone(t *testing.T) {
	var state any
	chunks := [][]byte{
		[]byte(`data: {"id":"resp_interrupted_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":"{\"filePath\":\"foo"}}]},"finish_reason":null}]}`),
		[]byte(`data: [DONE]`),
	}

	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, TranslateStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "gpt-test", nil, nil, chunk, &state)...)
	}
	joined := bytes.Join(out, nil)
	for _, forbidden := range []string{"response.completed", "response.output_item.done", "response.function_call_arguments.done"} {
		if bytes.Contains(joined, []byte(forbidden)) {
			t.Fatalf("partial tool stream emitted %s: %s", forbidden, joined)
		}
	}
}

func TestUpstreamOpenAIChatToResponsesStreamLengthAndFilterEmitIncomplete(t *testing.T) {
	tests := []struct {
		name       string
		finish     string
		wantReason string
	}{
		{name: "length", finish: "length", wantReason: "max_output_tokens"},
		{name: "content_filter", finish: "content_filter", wantReason: "content_filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var state any
			chunks := [][]byte{
				[]byte(`data: {"id":"resp_incomplete_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":""}}]},"finish_reason":null}]}`),
				[]byte(`data: {"id":"resp_incomplete_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"` + tt.finish + `"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`),
				[]byte(`data: [DONE]`),
			}
			var out [][]byte
			for _, chunk := range chunks {
				out = append(out, TranslateStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "gpt-test", nil, nil, chunk, &state)...)
			}
			joined := bytes.Join(out, nil)
			if bytes.Contains(joined, []byte("response.completed")) {
				t.Fatalf("incomplete finish emitted response.completed: %s", joined)
			}
			if !bytes.Contains(joined, []byte("response.incomplete")) {
				t.Fatalf("missing response.incomplete: %s", joined)
			}
			if !bytes.Contains(joined, []byte(`"status":"incomplete"`)) {
				t.Fatalf("missing incomplete item status: %s", joined)
			}
			if !bytes.Contains(joined, []byte(`"reason":"`+tt.wantReason+`"`)) {
				t.Fatalf("missing incomplete reason %q: %s", tt.wantReason, joined)
			}
		})
	}
}

func TestUpstreamGeminiRequestToolIDsAreDeterministicAndMatched(t *testing.T) {
	raw := []byte(`{
		"contents":[
			{"role":"model","parts":[
				{"functionCall":{"name":"read_file","args":{"path":"main.go"}}},
				{"functionCall":{"name":"grep","args":{"pattern":"TODO"}}}
			]},
			{"role":"function","parts":[
				{"functionResponse":{"name":"read_file","response":{"result":"code"}}},
				{"functionResponse":{"name":"grep","response":{"result":"matches"}}}
			]}
		]
	}`)

	first := TranslateRequest(FormatGemini, FormatOpenAI, "gpt-test", raw, false)
	firstCall0 := gjson.GetBytes(first, "messages.0.tool_calls.0.id").String()
	firstCall1 := gjson.GetBytes(first, "messages.0.tool_calls.1.id").String()
	if !strings.HasPrefix(firstCall0, "call_") || !strings.HasPrefix(firstCall1, "call_") {
		t.Fatalf("generated IDs lack call_ prefix: %s", first)
	}
	if firstCall0 == firstCall1 {
		t.Fatalf("generated IDs are not distinct: %s", first)
	}
	if got := gjson.GetBytes(first, "messages.1.tool_call_id").String(); got != firstCall0 {
		t.Fatalf("first tool response ID = %q, want %q; output=%s", got, firstCall0, first)
	}
	if got := gjson.GetBytes(first, "messages.2.tool_call_id").String(); got != firstCall1 {
		t.Fatalf("second tool response ID = %q, want %q; output=%s", got, firstCall1, first)
	}

	for i := 0; i < 20; i++ {
		out := TranslateRequest(FormatGemini, FormatOpenAI, "gpt-test", raw, false)
		if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != firstCall0 {
			t.Fatalf("iteration %d call 0 ID = %q, want %q", i, got, firstCall0)
		}
		if got := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String(); got != firstCall1 {
			t.Fatalf("iteration %d call 1 ID = %q, want %q", i, got, firstCall1)
		}
	}
}

func TestUpstreamGeminiRequestSupportsCamelCallIDAndOutOfOrderExplicitResponses(t *testing.T) {
	raw := []byte(`{
		"contents":[
			{"role":"model","parts":[
				{"functionCall":{"name":"foo","callId":"call_1","args":{"n":1}}},
				{"functionCall":{"name":"foo","id":"call_2","args":{"n":2}}},
				{"functionCall":{"name":"foo","call_id":"call_3","args":{"n":3}}}
			]},
			{"role":"function","parts":[
				{"functionResponse":{"name":"foo","callId":"call_2","response":{"r":2}}},
				{"functionResponse":{"name":"foo","response":{"r":1}}},
				{"functionResponse":{"name":"foo","response":{"r":3}}}
			]}
		]
	}`)

	out := TranslateRequest(FormatGemini, FormatOpenAI, "gpt-test", raw, false)
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("callId functionCall ID = %q, want call_1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_2" {
		t.Fatalf("explicit response ID = %q, want call_2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "call_1" {
		t.Fatalf("first implicit response ID = %q, want call_1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.3.tool_call_id").String(); got != "call_3" {
		t.Fatalf("second implicit response ID = %q, want call_3; output=%s", got, out)
	}
}

func TestUpstreamResponsesStringInputToGeminiContent(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3.7-flash-high",
		"instructions":"Be exact.",
		"input":"Reply exactly: responses-string-ok"
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3.7-flash-high", raw, false)
	if got := gjson.GetBytes(out, "systemInstruction.parts.0.text").String(); got != "Be exact." {
		t.Fatalf("system instruction = %q, want Be exact.; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "Reply exactly: responses-string-ok" {
		t.Fatalf("user text = %q, want string input text; output=%s", got, out)
	}
}

func TestUpstreamResponsesToGeminiStructuredFunctionOutputUsesResultEnvelope(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3.7-flash-high",
		"input":[
			{"type":"function_call","call_id":"call_1","name":"query","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":[
				{"type":"input_text","text":"summary header"},
				{"id":1,"status":"active"}
			]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3.7-flash-high", raw, false)
	part := gjson.GetBytes(out, "contents.2.parts.0.functionResponse")
	result := part.Get("response.result")
	if !result.IsArray() || len(result.Array()) != 2 {
		t.Fatalf("response.result = %s, want two-item array; output=%s", result.Raw, out)
	}
	if got := result.Array()[1].Get("status").String(); got != "active" {
		t.Fatalf("structured result status = %q, want active; output=%s", got, out)
	}
	if part.Get("response.0").Exists() {
		t.Fatalf("functionResponse.response leaked raw array instead of result envelope: %s", out)
	}
}

func TestUpstreamResponsesToGeminiToolResultImageNestsInlineData(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3.7-flash-high",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"read image"}]},
			{"type":"function_call","call_id":"call_read_1","name":"read","arguments":"{\"path\":\"/tmp/image.png\"}"},
			{"type":"function_call_output","call_id":"call_read_1","output":[
				{"type":"input_text","text":"Read image file [image/png]"},
				{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,QUJD"}
			]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3.7-flash-high", raw, false)
	funcContent := gjson.GetBytes(out, "contents.2")
	if got := funcContent.Get("role").String(); got != "user" {
		t.Fatalf("function response role = %q, want user; output=%s", got, out)
	}
	parts := funcContent.Get("parts").Array()
	if len(parts) != 1 {
		t.Fatalf("parts length = %d, want functionResponse with nested inlineData; output=%s", len(parts), out)
	}
	if got := parts[0].Get("functionResponse.response.result").String(); got != "Read image file [image/png]" {
		t.Fatalf("functionResponse result = %q, want text result; output=%s", got, out)
	}
	if got := parts[0].Get("functionResponse.parts.0.inlineData.mimeType").String(); got != "image/png" {
		t.Fatalf("nested inlineData mimeType = %q, want image/png; output=%s", got, out)
	}
	if got := parts[0].Get("functionResponse.parts.0.inlineData.data").String(); got != "QUJD" {
		t.Fatalf("nested inlineData data = %q, want QUJD; output=%s", got, out)
	}
	if gjson.GetBytes(out, "contents.2.parts.1.inlineData").Exists() {
		t.Fatalf("Gemini image remained as sibling instead of functionResponse.parts: %s", out)
	}
}

func TestUpstreamResponsesToAntigravityToolResultImageAttachesToFunctionResponse(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"read image"}]},
			{"type":"function_call","call_id":"call_read_1","name":"read","arguments":"{\"path\":\"/tmp/image.png\"}"},
			{"type":"function_call_output","call_id":"call_read_1","output":[
				{"type":"input_text","text":"Read image file [image/png]"},
				{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,QUJD"}
			]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "gemini-3-flash", raw, false)
	funcResp := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatalf("functionResponse missing; output=%s", out)
	}
	if got := funcResp.Get("id").String(); got != "call_read_1" {
		t.Fatalf("functionResponse id = %q, want call_read_1; output=%s", got, out)
	}
	if got := funcResp.Get("name").String(); got != "read" {
		t.Fatalf("functionResponse name = %q, want read; output=%s", got, out)
	}
	if got := funcResp.Get("response.result").String(); got != "Read image file [image/png]" {
		t.Fatalf("functionResponse result = %q, want text result; output=%s", got, out)
	}
	if got := funcResp.Get("parts.0.inlineData.mimeType").String(); got != "image/png" {
		t.Fatalf("functionResponse image mimeType = %q, want image/png; output=%s", got, out)
	}
	if got := funcResp.Get("parts.0.inlineData.data").String(); got != "QUJD" {
		t.Fatalf("functionResponse image data = %q, want QUJD; output=%s", got, out)
	}
	if gjson.GetBytes(out, "request.contents.2.parts.1.inlineData").Exists() {
		t.Fatalf("Antigravity image remained as sibling instead of functionResponse.parts: %s", out)
	}
}

func TestUpstreamResponsesToAntigravityParallelToolImagesAttachNearestResponse(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"read both"}]},
			{"type":"function_call","call_id":"call_a","name":"read","arguments":"{\"path\":\"/tmp/a.png\"}"},
			{"type":"function_call","call_id":"call_b","name":"read","arguments":"{\"path\":\"/tmp/b.png\"}"},
			{"type":"function_call_output","call_id":"call_a","output":[
				{"type":"input_text","text":"file A"},
				{"type":"input_image","image_url":"data:image/png;base64,AAA"}
			]},
			{"type":"function_call_output","call_id":"call_b","output":[
				{"type":"input_text","text":"file B"},
				{"type":"input_image","image_url":"data:image/jpeg;base64,BBB"}
			]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "gemini-3-flash", raw, false)
	parts := gjson.GetBytes(out, "request.contents.2.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("function response parts = %d, want 2; output=%s", len(parts), out)
	}
	got := map[string]string{}
	for _, part := range parts {
		fr := part.Get("functionResponse")
		got[fr.Get("id").String()] = fr.Get("parts.0.inlineData.data").String()
	}
	if got["call_a"] != "AAA" {
		t.Fatalf("call_a image = %q, want AAA; output=%s", got["call_a"], out)
	}
	if got["call_b"] != "BBB" {
		t.Fatalf("call_b image = %q, want BBB; output=%s", got["call_b"], out)
	}
}

func TestUpstreamResponsesToAntigravityAvoidsIntrinsicToolNameCollisions(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-pro-agent",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"read then grep"}]},
			{"type":"function_call","call_id":"call_read","name":"read_file","arguments":"{\"path\":\"a.txt\"}"},
			{"type":"function_call_output","call_id":"call_read","output":"file content"}
		],
		"tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}},
			{"type":"function","name":"grep","parameters":{"type":"object"}}
		],
		"tool_choice":{"type":"function","name":"read_file"}
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "gemini-pro-agent", raw, false)
	if got := gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.name").String(); got != "external_read_file" {
		t.Fatalf("colliding tool declaration = %q, want external_read_file; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.tools.0.functionDeclarations.1.name").String(); got != "grep" {
		t.Fatalf("non-colliding tool declaration = %q, want grep; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != "external_read_file" {
		t.Fatalf("tool choice name = %q, want external_read_file; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String(); got != "external_read_file" {
		t.Fatalf("history functionCall name = %q, want external_read_file; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String(); got != "external_read_file" {
		t.Fatalf("history functionResponse name = %q, want external_read_file; output=%s", got, out)
	}
}

func TestUpstreamAntigravityResponseRestoresIntrinsicToolName(t *testing.T) {
	raw := []byte(`{"response":{
		"responseId":"resp_ag_collision",
		"modelVersion":"gemini-pro-agent",
		"candidates":[{
			"content":{"role":"model","parts":[
				{"functionCall":{"id":"call_read","name":"external_read_file","args":{"path":"a.txt"}}}
			]},
			"finishReason":"STOP"
		}]
	}}`)

	out := TranslateNonStream(context.Background(), FormatAntigravity, FormatOpenAIResponse, "gemini-pro-agent", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "output.0.name").String(); got != "read_file" {
		t.Fatalf("response function_call name = %q, want read_file; output=%s", got, out)
	}
}

func TestUpstreamAntigravityStreamRestoresIntrinsicToolName(t *testing.T) {
	var state any
	chunk := []byte(`data: {"response":{
		"responseId":"resp_ag_stream_collision",
		"modelVersion":"gemini-pro-agent",
		"candidates":[{
			"content":{"role":"model","parts":[
				{"functionCall":{"id":"call_read","name":"external_read_file","args":{"path":"a.txt"}}}
			]}
		}]
	}}`)

	out := bytes.Join(TranslateStream(context.Background(), FormatAntigravity, FormatOpenAIResponse, "gemini-pro-agent", nil, nil, chunk, &state), nil)
	if !bytes.Contains(out, []byte(`"name":"read_file"`)) {
		t.Fatalf("stream function_call name was not restored: %s", out)
	}
	if bytes.Contains(out, []byte(`external_read_file`)) {
		t.Fatalf("stream leaked upstream-prefixed tool name: %s", out)
	}
}

func TestUpstreamResponsesToGeminiMidSessionDeveloperDoesNotMutateSystemInstruction(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"instructions":"Be a helpful assistant",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Turn 1 user"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Turn 1 assistant"}]},
			{"type":"message","role":"developer","content":"<image_resize_notice>Image 1 was resized</image_resize_notice>"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Turn 2 user"}]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3-flash", raw, false)
	systemParts := gjson.GetBytes(out, "systemInstruction.parts").Array()
	if len(systemParts) != 1 {
		t.Fatalf("systemInstruction parts = %d, want 1; output=%s", len(systemParts), out)
	}
	if got := systemParts[0].Get("text").String(); got != "Be a helpful assistant" {
		t.Fatalf("systemInstruction = %q, want instructions only; output=%s", got, out)
	}
	contents := gjson.GetBytes(out, "contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents length = %d, want 3; output=%s", len(contents), out)
	}
	turn2Parts := contents[2].Get("parts").Array()
	if len(turn2Parts) != 2 {
		t.Fatalf("turn 2 parts = %d, want developer notice plus user text; output=%s", len(turn2Parts), out)
	}
	if got := turn2Parts[0].Get("text").String(); got != "<image_resize_notice>Image 1 was resized</image_resize_notice>" {
		t.Fatalf("developer notice = %q; output=%s", got, out)
	}
	if got := turn2Parts[1].Get("text").String(); got != "Turn 2 user" {
		t.Fatalf("user text = %q; output=%s", got, out)
	}
}

func TestUpstreamResponsesToGeminiInterveningDeveloperAndUserFlushesBeforeToolResult(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"instructions":"Be exact",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"test\"}"},
			{"type":"message","role":"developer","content":"<permissions instructions>\nApproved: test\n</permissions instructions>"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Wait, also check this"}]},
			{"type":"function_call_output","call_id":"call-1","output":"done"}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3-flash", raw, false)
	if errPair := internalsignature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v; output=%s", errPair, out)
	}
	contents := gjson.GetBytes(out, "contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want leading user, model call, notice/user, result; output=%s", len(contents), out)
	}
	midParts := contents[2].Get("parts").Array()
	if len(midParts) != 2 {
		t.Fatalf("mid user parts = %d, want developer notice plus user text; output=%s", len(midParts), out)
	}
	if !strings.Contains(midParts[0].Get("text").String(), "permissions instructions") {
		t.Fatalf("mid part 0 should be developer notice; got %s", midParts[0].Raw)
	}
	if midParts[1].Get("text").String() != "Wait, also check this" {
		t.Fatalf("mid part 1 should be user text; got %s", midParts[1].Raw)
	}
	if !contents[3].Get("parts.0.functionResponse").Exists() {
		t.Fatalf("last turn should be functionResponse; got %s", contents[3].Raw)
	}
}

func TestUpstreamResponsesToGeminiPendingDeveloperFlushesAfterToolResult(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"Run tool"}]},
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"},
			{"type":"message","role":"developer","content":"Tool permission granted"},
			{"type":"function_call_output","call_id":"call-1","output":"done"}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3-flash", raw, false)
	if errPair := internalsignature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v; output=%s", errPair, out)
	}
	contents := gjson.GetBytes(out, "contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want user, model call, result, developer notice; output=%s", len(contents), out)
	}
	if !contents[2].Get("parts.0.functionResponse").Exists() {
		t.Fatalf("tool result turn missing before developer notice: %s", out)
	}
	if got := contents[3].Get("parts.0.text").String(); got != "Tool permission granted" {
		t.Fatalf("developer notice = %q, want Tool permission granted; output=%s", got, out)
	}
}

func TestUpstreamResponsesToGeminiParallelFunctionCallsShareModelTurn(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"read both"}]},
			{"type":"function_call","call_id":"call_a","name":"read","arguments":"{\"path\":\"/tmp/a.png\"}"},
			{"type":"function_call","call_id":"call_b","name":"read","arguments":"{\"path\":\"/tmp/b.png\"}"},
			{"type":"function_call_output","call_id":"call_a","output":"file A"},
			{"type":"function_call_output","call_id":"call_b","output":"file B"}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3-flash", raw, false)
	if got := len(gjson.GetBytes(out, "contents").Array()); got != 3 {
		t.Fatalf("contents length = %d, want 3; output=%s", got, out)
	}
	modelParts := gjson.GetBytes(out, "contents.1.parts").Array()
	if len(modelParts) != 2 {
		t.Fatalf("model parts = %d, want two function calls; output=%s", len(modelParts), out)
	}
	if got := modelParts[0].Get("functionCall.id").String(); got != "call_a" {
		t.Fatalf("first functionCall.id = %q, want call_a; output=%s", got, out)
	}
	if got := modelParts[1].Get("functionCall.id").String(); got != "call_b" {
		t.Fatalf("second functionCall.id = %q, want call_b; output=%s", got, out)
	}
}

func TestUpstreamResponsesToGeminiKeepsVisibleModelTurnBoundary(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"input":[
			{"role":"assistant","content":[{"type":"output_text","text":"preface"}]},
			{"type":"function_call","call_id":"call_run","name":"run","arguments":"{}"}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3-flash", raw, false)
	if got := gjson.GetBytes(out, "contents.1.parts.0.text").String(); got != "preface" {
		t.Fatalf("visible model text = %q, want preface; output=%s", got, out)
	}
	if !gjson.GetBytes(out, "contents.2.parts.0.functionCall").Exists() {
		t.Fatalf("function call model turn was merged into visible text turn: %s", out)
	}
}

func TestUpstreamResponsesToOpenAICombinesAssistantReasoningContentAndToolCalls(t *testing.T) {
	raw := []byte(`{
		"model":"k3",
		"input":[
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"inspect the next step"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Step 3 completed; continue to step 4."}]},
			{"type":"function_call","call_id":"call_4","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_4","output":"ok"}
		]
	}`)

	out := TranslateRequestWithOptions(FormatOpenAIResponse, FormatOpenAI, "k3", raw, false, RequestTranslationOptions{
		PreserveThinkingBlocks: true,
	})
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2; output=%s", len(messages), out)
	}
	assistant := messages[0]
	if got := assistant.Get("reasoning_content").String(); got != "inspect the next step" {
		t.Fatalf("assistant reasoning_content = %q, want inspect the next step; output=%s", got, out)
	}
	if got := assistant.Get("content.0.text").String(); got != "Step 3 completed; continue to step 4." {
		t.Fatalf("assistant content = %q, want preserved text; output=%s", got, out)
	}
	if got := assistant.Get("tool_calls.0.id").String(); got != "call_4" {
		t.Fatalf("assistant tool call id = %q, want call_4; output=%s", got, out)
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call_4" {
		t.Fatalf("tool output call id = %q, want call_4; output=%s", got, out)
	}
}

func TestUpstreamResponsesToOpenAIKeepsUserItemBoundaries(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"delegation envelope"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"visible user prompt"}]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-test", raw, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want two preserved user turns; output=%s", len(messages), out)
	}
	if got := messages[0].Get("content.0.text").String(); got != "delegation envelope" {
		t.Fatalf("first user text = %q; output=%s", got, out)
	}
	if got := messages[1].Get("content.0.text").String(); got != "visible user prompt" {
		t.Fatalf("second user text = %q; output=%s", got, out)
	}
}

func TestUpstreamResponsesToGeminiAndAntigravityPrependEmptyUserForLeadingModel(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"input":[
			{"type":"function_call","call_id":"call_1","name":"run","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]
	}`)

	geminiOut := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3-flash", raw, false)
	if got := gjson.GetBytes(geminiOut, "contents.0.role").String(); got != "user" {
		t.Fatalf("Gemini leading role = %q, want user; output=%s", got, geminiOut)
	}
	if got := gjson.GetBytes(geminiOut, "contents.0.parts.0.text").String(); got != "" {
		t.Fatalf("Gemini leading user text = %q, want empty; output=%s", got, geminiOut)
	}
	if got := gjson.GetBytes(geminiOut, "contents.1.role").String(); got != "model" {
		t.Fatalf("Gemini second role = %q, want model; output=%s", got, geminiOut)
	}

	antigravityOut := TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "gemini-3-flash", raw, false)
	if got := gjson.GetBytes(antigravityOut, "request.contents.0.role").String(); got != "user" {
		t.Fatalf("Antigravity leading role = %q, want user; output=%s", got, antigravityOut)
	}
	if got := gjson.GetBytes(antigravityOut, "request.contents.0.parts.0.text").String(); got != "" {
		t.Fatalf("Antigravity leading user text = %q, want empty; output=%s", got, antigravityOut)
	}
	if got := gjson.GetBytes(antigravityOut, "request.contents.1.role").String(); got != "model" {
		t.Fatalf("Antigravity second role = %q, want model; output=%s", got, antigravityOut)
	}
}

func TestUpstreamResponsesToAntigravityClaudeTargetPreservesLeadingModel(t *testing.T) {
	raw := []byte(`{
		"model":"claude-sonnet-4-6",
		"input":[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "claude-sonnet-4-6", raw, false)
	if got := gjson.GetBytes(out, "request.contents.0.role").String(); got != "model" {
		t.Fatalf("Antigravity Claude leading role = %q, want model; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.text").String(); got != "prior answer" {
		t.Fatalf("Antigravity Claude leading model text = %q, want prior answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.role").String(); got != "user" {
		t.Fatalf("Antigravity Claude second role = %q, want user; output=%s", got, out)
	}
}

func TestUpstreamGeminiInvalidRoleWithFunctionResponseNormalizesToUser(t *testing.T) {
	raw := []byte(`{
		"contents":[
			{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{}}}]},
			{"role":"invalid","parts":[{"functionResponse":{"id":"call_1","name":"lookup","response":{"result":"ok"}}}]}
		]
	}`)

	out := TranslateRequest(FormatGemini, FormatAntigravity, "gemini-3-flash", raw, false)
	if got := gjson.GetBytes(out, "request.contents.2.role").String(); got != "user" {
		t.Fatalf("functionResponse role = %q, want user; output=%s", got, out)
	}
}

func TestUpstreamGeminiInvalidTextRoleAlternatesFromPreviousTurn(t *testing.T) {
	raw := []byte(`{
		"contents":[
			{"role":"user","parts":[{"text":"first"}]},
			{"role":"invalid","parts":[{"text":"second"}]}
		]
	}`)

	out := TranslateRequest(FormatGemini, FormatAntigravity, "gemini-3-flash", raw, false)
	if got := gjson.GetBytes(out, "request.contents.1.role").String(); got != "model" {
		t.Fatalf("invalid text role = %q, want model; output=%s", got, out)
	}
}

func TestUpstreamGeminiToolSchemaStripsEncryptedMetadata(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-3-flash",
		"input":"hi",
		"tools":[{
			"type":"function",
			"name":"store_secret",
			"parameters":{
				"type":"object",
				"properties":{
					"secret":{"type":"string","encrypted":true},
					"encrypted":{"type":"boolean","description":"flag","encrypted":true}
				},
				"required":["secret","encrypted"]
			}
		}]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3-flash", raw, false)
	schema := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parameters")
	if schema.Get("properties.secret.encrypted").Exists() {
		t.Fatalf("secret encrypted metadata survived: %s", out)
	}
	if !schema.Get("properties.encrypted").Exists() {
		t.Fatalf("property named encrypted was removed: %s", out)
	}
	if schema.Get("properties.encrypted.encrypted").Exists() {
		t.Fatalf("inner encrypted metadata survived on encrypted property: %s", out)
	}
	if got := schema.Get("properties.encrypted.type").String(); got != "boolean" {
		t.Fatalf("encrypted property type = %q, want boolean; output=%s", got, out)
	}
}

func TestUpstreamResponsesToGeminiStripsSpoofedInternalSignatureFields(t *testing.T) {
	raw := []byte(`{
		"model":"gemini-test",
		"input":[
			{
				"type":"function_call",
				"call_id":"call_1",
				"name":"run",
				"arguments":"{}",
				"_cpa_reason\u0069ng_signature":"spoofed-signature"
			}
		]
	}`)

	out := TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-test", raw, false)
	joined := string(out)
	for _, forbidden := range []string{"_cpa_reasoning_signature", "spoofed-signature", "thoughtSignature"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("spoofed internal signature field leaked %q: %s", forbidden, out)
		}
	}
	if !gjson.GetBytes(out, "contents.1.parts.0.functionCall").Exists() {
		t.Fatalf("ordinary function call was dropped: %s", out)
	}
	if got := gjson.GetBytes(out, "contents.1.parts.0.functionCall.name").String(); got != "run" {
		t.Fatalf("functionCall.name = %q, want run; output=%s", got, out)
	}
}

func TestUpstreamOpenAICompatibleBlankToolCallsAreDropped(t *testing.T) {
	raw := []byte(`{
		"id":"msg_blank_tool",
		"object":"chat.completion",
		"model":"qwen-test",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"qwen-ok",
				"reasoning_content":"The user requested qwen-ok",
					"tool_calls":[
						{"id":"","type":"function","function":{"name":"","arguments":""}},
						{"id":"fc_","type":"function_call","call_id":"","name":"","arguments":""}
					]
				},
				"finish_reason":"tool_calls"
			}]
		}`)

	var handler OpenAIHandler
	resp, err := handler.ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("blank tool call survived parse: %#v", resp.ToolCalls)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop after blank tool call drop", resp.FinishReason)
	}
	out, err := handler.FormatResponse(resp, "qwen-test")
	if err != nil {
		t.Fatalf("FormatResponse() error = %v", err)
	}
	if gjson.GetBytes(out, "choices.0.message.tool_calls").Exists() {
		t.Fatalf("blank tool_calls survived format: %s", out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "qwen-ok" {
		t.Fatalf("content = %q, want qwen-ok; output=%s", got, out)
	}

	identity := TranslateNonStream(context.Background(), FormatOpenAI, FormatOpenAI, "qwen-test", nil, nil, raw, nil)
	if gjson.GetBytes(identity, "choices.0.message.tool_calls").Exists() {
		t.Fatalf("blank tool_calls survived OpenAI identity translation: %s", identity)
	}
	if got := gjson.GetBytes(identity, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("identity finish_reason = %q, want stop; output=%s", got, identity)
	}

	responses := TranslateNonStream(context.Background(), FormatOpenAI, FormatOpenAIResponse, "qwen-test", nil, nil, raw, nil)
	if gjson.GetBytes(responses, `output.#(type=="function_call")`).Exists() {
		t.Fatalf("blank tool call survived Responses translation: %s", responses)
	}
	if got := gjson.GetBytes(responses, "status").String(); got != "completed" {
		t.Fatalf("responses status = %q, want completed; output=%s", got, responses)
	}

	var responsesHandler InteractionsHandler
	direct, err := responsesHandler.FormatResponse(&UnifiedResponse{
		ID:           "resp_blank_tool",
		Model:        "qwen-test",
		Content:      "qwen-ok",
		FinishReason: "stop",
		ToolCalls: []map[string]any{{
			"id":        "fc_",
			"type":      "function_call",
			"call_id":   "",
			"name":      "",
			"arguments": "",
		}},
	}, "qwen-test")
	if err != nil {
		t.Fatalf("Responses FormatResponse() error = %v", err)
	}
	if gjson.GetBytes(direct, `output.#(type=="function_call")`).Exists() {
		t.Fatalf("blank direct Responses tool call survived format: %s", direct)
	}
}

func TestUpstreamAnthropicBlankToolUseDoesNotBecomeOpenAIToolCall(t *testing.T) {
	raw := []byte(`{
		"id":"msg_blank_tool",
		"model":"qwen-anthropic",
		"stop_reason":"tool_use",
		"content":[
			{"type":"text","text":"qwen-ok"},
			{"type":"tool_use","id":"","name":"","input":{}}
		]
	}`)

	out := TranslateNonStream(context.Background(), FormatAnthropic, FormatOpenAI, "qwen-test", nil, nil, raw, nil)
	if gjson.GetBytes(out, "choices.0.message.tool_calls").Exists() {
		t.Fatalf("blank Anthropic tool_use became OpenAI tool_calls: %s", out)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "qwen-ok" {
		t.Fatalf("content = %q, want qwen-ok; output=%s", got, out)
	}
}

func TestUpstreamBlankToolCallFilterKeepsNamedEmptyArgumentTool(t *testing.T) {
	raw := []byte(`{
		"id":"msg_valid_tool",
		"object":"chat.completion",
		"model":"tool-test",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]
			},
			"finish_reason":"tool_calls"
		}]
	}`)

	var handler OpenAIHandler
	resp, err := handler.ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", resp.FinishReason)
	}
}
