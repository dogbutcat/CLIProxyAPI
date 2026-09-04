package conformance_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

type protocolPair struct {
	client   oagmsg.Format
	upstream oagmsg.Format
}

var runtimeProtocolPairs = []protocolPair{
	{oagmsg.FormatOpenAI, oagmsg.FormatOpenAI},
	{oagmsg.FormatOpenAIResponse, oagmsg.FormatOpenAI},
	{oagmsg.FormatAnthropic, oagmsg.FormatOpenAI},
	{oagmsg.FormatGemini, oagmsg.FormatOpenAI},

	{oagmsg.FormatOpenAI, oagmsg.FormatAnthropic},
	{oagmsg.FormatOpenAIResponse, oagmsg.FormatAnthropic},
	{oagmsg.FormatGemini, oagmsg.FormatAnthropic},
	{oagmsg.FormatInteractions, oagmsg.FormatAnthropic},
	{oagmsg.FormatAnthropic, oagmsg.FormatAnthropic},
	{oagmsg.FormatAnthropic, oagmsg.FormatInteractions},

	{oagmsg.FormatOpenAI, oagmsg.FormatGemini},
	{oagmsg.FormatOpenAIResponse, oagmsg.FormatGemini},
	{oagmsg.FormatAnthropic, oagmsg.FormatGemini},
	{oagmsg.FormatGemini, oagmsg.FormatGemini},
	{oagmsg.FormatInteractions, oagmsg.FormatGemini},

	{oagmsg.FormatInteractions, oagmsg.FormatInteractions},
	{oagmsg.FormatOpenAI, oagmsg.FormatInteractions},
	{oagmsg.FormatInteractions, oagmsg.FormatOpenAI},
	{oagmsg.FormatOpenAIResponse, oagmsg.FormatInteractions},
	{oagmsg.FormatInteractions, oagmsg.FormatOpenAIResponse},
	{oagmsg.FormatGemini, oagmsg.FormatInteractions},
	{oagmsg.FormatOpenAIResponse, oagmsg.FormatOpenAIResponse},

	{oagmsg.FormatOpenAI, oagmsg.FormatCodex},
	{oagmsg.FormatOpenAIResponse, oagmsg.FormatCodex},
	{oagmsg.FormatAnthropic, oagmsg.FormatCodex},
	{oagmsg.FormatGemini, oagmsg.FormatCodex},
	{oagmsg.FormatInteractions, oagmsg.FormatCodex},

	{oagmsg.FormatOpenAI, oagmsg.FormatAntigravity},
	{oagmsg.FormatOpenAIResponse, oagmsg.FormatAntigravity},
	{oagmsg.FormatAnthropic, oagmsg.FormatAntigravity},
	{oagmsg.FormatGemini, oagmsg.FormatAntigravity},
	{oagmsg.FormatInteractions, oagmsg.FormatAntigravity},
}

var requestFixtures = map[oagmsg.Format][]byte{
	oagmsg.FormatOpenAI: []byte(`{
		"model":"source-model","messages":[
			{"role":"system","content":"be concise"},
			{"role":"user","content":"hello"}
		],"temperature":0.4,"max_tokens":64,"stream":true
	}`),
	oagmsg.FormatOpenAIResponse: []byte(`{
		"model":"source-model","instructions":"be concise","input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		],"temperature":0.4,"max_output_tokens":64,"stream":true
	}`),
	oagmsg.FormatAnthropic: []byte(`{
		"model":"source-model","system":"be concise","messages":[
			{"role":"user","content":"hello"}
		],"temperature":0.4,"max_tokens":64,"stream":true
	}`),
	oagmsg.FormatGemini: []byte(`{
		"model":"source-model","systemInstruction":{"parts":[{"text":"be concise"}]},
		"contents":[{"role":"user","parts":[{"text":"hello"}]}],
		"generationConfig":{"temperature":0.4,"maxOutputTokens":64}
	}`),
	oagmsg.FormatInteractions: []byte(`{
		"model":"source-model","system_instruction":"be concise","input":[
			{"type":"user_input","content":[{"type":"text","text":"hello"}]}
		],"generation_config":{"temperature":0.4,"max_output_tokens":64},"stream":true
	}`),
}

var nonStreamFixtures = map[oagmsg.Format][]byte{
	oagmsg.FormatOpenAI: []byte(`{
		"id":"chat_1","model":"model","created":1,
		"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hello"}}],
		"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
	}`),
	oagmsg.FormatOpenAIResponse: []byte(`{
		"id":"resp_1","model":"model","status":"completed",
		"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],
		"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}
	}`),
	oagmsg.FormatAnthropic: []byte(`{
		"id":"msg_1","model":"model","type":"message","role":"assistant",
		"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":3,"output_tokens":2}
	}`),
	oagmsg.FormatGemini: []byte(`{
		"responseId":"gem_1","modelVersion":"model",
		"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}
	}`),
	oagmsg.FormatInteractions: []byte(`{
		"id":"int_1","model":"model","status":"completed",
		"steps":[{"type":"model_output","content":[{"type":"text","text":"hello"}]}],
		"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}
	}`),
	oagmsg.FormatCodex: []byte(`{
		"id":"resp_1","model":"model","status":"completed",
		"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],
		"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}
	}`),
	oagmsg.FormatAntigravity: []byte(`{
		"response":{"responseId":"ag_1","modelVersion":"model",
		"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}
	}`),
}

var streamFixtures = map[oagmsg.Format][][]byte{
	oagmsg.FormatOpenAI: {
		[]byte(`data: {"id":"chat_1","model":"model","created":1,"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`),
	},
	oagmsg.FormatOpenAIResponse: {
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"model","created_at":1}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"hello"}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","model":"model","status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`),
	},
	oagmsg.FormatAnthropic: {
		[]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"model","usage":{"input_tokens":3,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`),
		[]byte(`data: {"type":"message_stop"}`),
	},
	oagmsg.FormatGemini: {
		[]byte(`data: {"responseId":"gem_1","modelVersion":"model","candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`),
	},
	oagmsg.FormatInteractions: {
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"int_1","model":"model","status":"in_progress"}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hello"}}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"int_1","model":"model","status":"completed","usage":{"total_input_tokens":3,"total_output_tokens":2,"total_tokens":5}}}`),
	},
	oagmsg.FormatCodex: {
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"model","created_at":1}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"hello"}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","model":"model","status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`),
	},
	oagmsg.FormatAntigravity: {
		[]byte(`data: {"response":{"responseId":"ag_1","modelVersion":"model","candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}}`),
	},
}

func TestDirectMatrixPairInventory(t *testing.T) {
	if len(runtimeProtocolPairs) != 32 {
		t.Fatalf("pair count = %d, want 32", len(runtimeProtocolPairs))
	}
	seen := make(map[string]bool, len(runtimeProtocolPairs))
	for _, pair := range runtimeProtocolPairs {
		key := pairName(pair)
		if seen[key] {
			t.Fatalf("duplicate pair %s", key)
		}
		seen[key] = true
		if _, ok := oagmsg.DefaultRegistry().Get(pair.client); !ok {
			t.Fatalf("missing client handler for %s", key)
		}
		if _, ok := oagmsg.DefaultRegistry().Get(pair.upstream); !ok {
			t.Fatalf("missing upstream handler for %s", key)
		}
	}
}

func TestDirectMatrixRequests(t *testing.T) {
	registry := oagmsg.DefaultRegistry()
	for _, pair := range runtimeProtocolPairs {
		pair := pair
		t.Run(pairName(pair), func(t *testing.T) {
			input, ok := requestFixtures[pair.client]
			if !ok {
				t.Fatalf("missing request fixture for %s", pair.client)
			}
			output, err := registry.Translate(pair.client, pair.upstream, input)
			if err != nil {
				t.Fatal(err)
			}
			assertRequestShape(t, pair.upstream, output)
		})
	}
}

func TestDirectMatrixNonStreamResponses(t *testing.T) {
	registry := oagmsg.DefaultRegistry()
	for _, pair := range runtimeProtocolPairs {
		pair := pair
		t.Run(pairName(pair), func(t *testing.T) {
			input, ok := nonStreamFixtures[pair.upstream]
			if !ok {
				t.Fatalf("missing response fixture for %s", pair.upstream)
			}
			output, err := registry.TranslateResponse(pair.upstream, pair.client, "target-model", input)
			if err != nil {
				t.Fatal(err)
			}
			assertResponseShape(t, pair.client, output)
		})
	}
}

func TestDirectMatrixStreams(t *testing.T) {
	for _, pair := range runtimeProtocolPairs {
		pair := pair
		t.Run(pairName(pair), func(t *testing.T) {
			session, err := oagmsg.NewStreamSession(pair.upstream, pair.client, "target-model")
			if err != nil {
				t.Fatal(err)
			}
			var output [][]byte
			for _, chunk := range streamFixtures[pair.upstream] {
				translated, errTranslate := session.Translate(chunk)
				if errTranslate != nil {
					t.Fatal(errTranslate)
				}
				output = append(output, translated...)
			}
			output = append(output, session.Flush()...)
			assertStreamShape(t, pair.client, output)
		})
	}
}

func TestDirectMatrixErrors(t *testing.T) {
	registry := oagmsg.DefaultRegistry()
	input := []byte(`{"error":{"message":"request failed","type":"invalid_request_error","code":400}}`)
	clients := []oagmsg.Format{
		oagmsg.FormatOpenAI,
		oagmsg.FormatOpenAIResponse,
		oagmsg.FormatAnthropic,
		oagmsg.FormatGemini,
		oagmsg.FormatInteractions,
	}
	for _, client := range clients {
		client := client
		t.Run(string(client), func(t *testing.T) {
			output, err := registry.TranslateResponse(oagmsg.FormatOpenAI, client, "target-model", input)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(output) || gjson.GetBytes(output, "error.message").String() != "request failed" {
				t.Fatalf("%s error output = %s", client, output)
			}
		})
	}
}

func assertRequestShape(t *testing.T, format oagmsg.Format, output []byte) {
	t.Helper()
	if !json.Valid(output) {
		t.Fatalf("invalid JSON output: %s", output)
	}
	path := map[oagmsg.Format]string{
		oagmsg.FormatOpenAI:         "messages",
		oagmsg.FormatOpenAIResponse: "input",
		oagmsg.FormatAnthropic:      "messages",
		oagmsg.FormatGemini:         "contents",
		oagmsg.FormatInteractions:   "input",
		oagmsg.FormatCodex:          "input",
		oagmsg.FormatAntigravity:    "request.contents",
	}[format]
	if path == "" || !gjson.GetBytes(output, path).Exists() {
		t.Fatalf("%s request missing %q: %s", format, path, output)
	}
}

func assertResponseShape(t *testing.T, format oagmsg.Format, output []byte) {
	t.Helper()
	if !json.Valid(output) {
		t.Fatalf("invalid JSON output: %s", output)
	}
	path := map[oagmsg.Format]string{
		oagmsg.FormatOpenAI:         "choices",
		oagmsg.FormatOpenAIResponse: "output",
		oagmsg.FormatAnthropic:      "content",
		oagmsg.FormatGemini:         "candidates",
		oagmsg.FormatInteractions:   "steps",
	}[format]
	if path == "" || !gjson.GetBytes(output, path).Exists() {
		t.Fatalf("%s response missing %q: %s", format, path, output)
	}
}

func assertStreamShape(t *testing.T, format oagmsg.Format, chunks [][]byte) {
	t.Helper()
	if len(chunks) == 0 {
		t.Fatalf("%s stream produced no output", format)
	}
	var combined strings.Builder
	for _, chunk := range chunks {
		combined.Write(chunk)
	}
	marker := map[oagmsg.Format]string{
		oagmsg.FormatOpenAI:         `"choices"`,
		oagmsg.FormatOpenAIResponse: `response.`,
		oagmsg.FormatAnthropic:      `event: message_`,
		oagmsg.FormatGemini:         `"candidates"`,
		oagmsg.FormatInteractions:   `"event_type"`,
	}[format]
	if marker == "" || !strings.Contains(combined.String(), marker) {
		t.Fatalf("%s stream missing %q: %s", format, marker, combined.String())
	}
}

func pairName(pair protocolPair) string {
	return fmt.Sprintf("%s_to_%s", pair.client, pair.upstream)
}
