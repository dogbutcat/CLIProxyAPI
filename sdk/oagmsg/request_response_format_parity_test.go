package oagmsg

import (
	"encoding/json"
	"reflect"
	"testing"

	chat_to_responses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/interactions/responses"
	responses_to_chat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesTextFormatToChatResponseFormatParity(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "json_schema",
			raw: []byte(`{
				"model":"gpt-test",
				"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
				"text":{"format":{
					"type":"json_schema",
					"name":"answer",
					"description":"Structured answer",
					"strict":true,
					"schema":{
						"type":"object",
						"properties":{"ok":{"type":"boolean"}},
						"required":["ok"],
						"additionalProperties":false
					}
				}}
			}`),
		},
		{
			name: "json_object",
			raw:  []byte(`{"model":"gpt-test","input":"hi","text":{"format":{"type":"json_object"}}}`),
		},
		{
			name: "text",
			raw:  []byte(`{"model":"gpt-test","input":"hi","text":{"format":{"type":"text"}}}`),
		},
		{
			name: "default omitted",
			raw:  []byte(`{"model":"gpt-test","input":"hi"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-test", tt.raw, false)
			want := responses_to_chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-test", tt.raw, false)
			assertResponseFormatEqual(t, gjson.GetBytes(got, "response_format"), gjson.GetBytes(want, "response_format"))
		})
	}
}

func TestOpenAIChatResponseFormatToResponsesTextFormatParity(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "json_schema",
			raw: []byte(`{
				"model":"gpt-test",
				"messages":[{"role":"user","content":"hi"}],
				"response_format":{
					"type":"json_schema",
					"json_schema":{
						"name":"answer",
						"description":"Structured answer",
						"strict":true,
						"schema":{
							"type":"object",
							"properties":{"ok":{"type":"boolean"}},
							"required":["ok"],
							"additionalProperties":false
						}
					}
				}
			}`),
		},
		{
			name: "json_object",
			raw:  []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`),
		},
		{
			name: "text",
			raw:  []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"text"}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranslateRequest(FormatOpenAI, FormatOpenAIResponse, "gpt-test", tt.raw, false)
			want := chat_to_responses.ConvertInteractionsRequestToOpenAIResponses("gpt-test", tt.raw, false)
			assertResponseFormatEqual(t, gjson.GetBytes(got, "text.format"), gjson.GetBytes(want, "text.format"))
		})
	}
}

func TestOpenAIResponseFormatRoundTripPreservation(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"text":{"format":{
			"type":"json_schema",
			"name":"answer",
			"description":"Structured answer",
			"strict":true,
			"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}
		}}
	}`)

	req, err := (&InteractionsHandler{}).ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest error = %v", err)
	}
	out, err := (&InteractionsHandler{}).SerializeRequest(req)
	if err != nil {
		t.Fatalf("SerializeRequest error = %v", err)
	}

	assertResponseFormatEqual(t, gjson.GetBytes(out, "text.format"), gjson.GetBytes(raw, "text.format"))
}

func TestOpenAIChatResponseFormatRoundTripPreservation(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"messages":[{"role":"user","content":"hi"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"answer",
				"description":"Structured answer",
				"strict":true,
				"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}
			}
		}
	}`)

	responses := TranslateRequest(FormatOpenAI, FormatOpenAIResponse, "gpt-test", raw, false)
	chat := TranslateRequest(FormatOpenAIResponse, FormatOpenAI, "gpt-test", responses, false)

	assertResponseFormatEqual(t, gjson.GetBytes(chat, "response_format"), gjson.GetBytes(raw, "response_format"))
}

func TestOpenAIChatMaxCompletionTokensToResponses(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"messages":[{"role":"user","content":"hi"}],
		"max_completion_tokens":321
	}`)

	responses := TranslateRequest(FormatOpenAI, FormatOpenAIResponse, "gpt-test", raw, false)
	if got := gjson.GetBytes(responses, "max_output_tokens").Int(); got != 321 {
		t.Fatalf("max_output_tokens = %d, want 321; body=%s", got, responses)
	}
}

func assertResponseFormatEqual(t *testing.T, got, want gjson.Result) {
	t.Helper()
	if !got.Exists() || !want.Exists() {
		if got.Exists() != want.Exists() {
			t.Fatalf("format presence mismatch: got %s want %s", got.Raw, want.Raw)
		}
		return
	}
	gotValue := decodeResponseFormatResult(t, got)
	wantValue := decodeResponseFormatResult(t, want)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("format mismatch:\ngot  %s\nwant %s", got.Raw, want.Raw)
	}
}

func decodeResponseFormatResult(t *testing.T, result gjson.Result) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(result.Raw), &value); err != nil {
		t.Fatalf("decode JSON %s: %v", result.Raw, err)
	}
	return value
}
