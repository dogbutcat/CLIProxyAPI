package oagmsg

import (
	"encoding/base64"
	"testing"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestRequestThinkingCompatClaudeToCodexPreservesEmptySignature(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := TranslateRequest(FormatAnthropic, FormatCodex, "deepseek-v4", payload, false)
	if got := gjson.GetBytes(withoutCompat, "input.#").Int(); got != 0 {
		t.Fatalf("default preserved empty-signature thinking: %s", withoutCompat)
	}

	withCompat := TranslateRequestWithOptions(FormatAnthropic, FormatCodex, "deepseek-v4", payload, false, RequestTranslationOptions{PreserveThinkingBlocks: true})
	reasoning := gjson.GetBytes(withCompat, "input.0")
	if reasoning.Get("type").String() != "reasoning" || !reasoning.Get("encrypted_content").Exists() || reasoning.Get("encrypted_content").String() != "" {
		t.Fatalf("compat missing reasoning with explicit empty encrypted_content: %s", withCompat)
	}
}

func TestRequestThinkingCompatClaudeToGeminiPreservesEmptySignature(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := TranslateRequest(FormatAnthropic, FormatGemini, "deepseek-v4", payload, false)
	if got := gjson.GetBytes(withoutCompat, "contents.1.parts.#").Int(); got != 0 {
		t.Fatalf("default preserved empty-signature thinking: %s", withoutCompat)
	}

	withCompat := TranslateRequestWithOptions(FormatAnthropic, FormatGemini, "deepseek-v4", payload, false, RequestTranslationOptions{PreserveThinkingBlocks: true})
	part := gjson.GetBytes(withCompat, "contents.1.parts.0")
	if !part.Get("thought").Bool() || part.Get("text").String() != "reason" {
		t.Fatalf("compat missing Gemini thought text: %s", withCompat)
	}
	if !part.Get("thoughtSignature").Exists() || part.Get("thoughtSignature").String() != "" {
		t.Fatalf("compat missing explicit empty thoughtSignature: %s", withCompat)
	}
}

func TestRequestThinkingCompatClaudeToGoogleInteractionsPreservesEmptyThought(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":""}]}]}`)

	withoutCompat := TranslateRequest(FormatAnthropic, FormatInteractions, "deepseek-v4", payload, false)
	if got := gjson.GetBytes(withoutCompat, "input.#").Int(); got != 0 {
		t.Fatalf("default preserved empty thought step: %s", withoutCompat)
	}

	withCompat := TranslateRequestWithOptions(FormatAnthropic, FormatInteractions, "deepseek-v4", payload, false, RequestTranslationOptions{PreserveThinkingBlocks: true})
	if got := gjson.GetBytes(withCompat, "input.0.type").String(); got != "thought" {
		t.Fatalf("compat missing thought step: %s", withCompat)
	}
}

func TestRequestThinkingCompatClaudeToOpenAIChatPreservesEmptySignature(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := TranslateRequest(FormatAnthropic, FormatOpenAI, "deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "messages.0.reasoning_content").Exists() {
		t.Fatalf("default preserved empty-signature reasoning_content: %s", withoutCompat)
	}

	withCompat := TranslateRequestWithOptions(FormatAnthropic, FormatOpenAI, "deepseek-v4", payload, false, RequestTranslationOptions{PreserveThinkingBlocks: true})
	if got := gjson.GetBytes(withCompat, "messages.0.reasoning_content").String(); got != "reason" {
		t.Fatalf("compat missing reasoning_content: %s", withCompat)
	}
}

func TestRequestThinkingCompatClaudeToOpenAIChatPreservesThinkingWithToolCalls(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		wantReasoning string
	}{
		{
			name:          "empty signature",
			payload:       `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""},{"type":"text","text":"Reading files."},{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"main.go"}}]}]}`,
			wantReasoning: "reason",
		},
		{
			name:          "opaque signature",
			payload:       `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"claude#opaque"},{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
			wantReasoning: "reason",
		},
		{
			name:    "tool call without thinking",
			payload: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(test.payload)
			withCompat := TranslateRequestWithOptions(FormatAnthropic, FormatOpenAI, "deepseek-v4", payload, false, RequestTranslationOptions{PreserveThinkingBlocks: true})
			assistant := gjson.GetBytes(withCompat, "messages.0")
			if got := assistant.Get("reasoning_content").String(); got != test.wantReasoning {
				t.Fatalf("reasoning_content = %q, want %q; output: %s", got, test.wantReasoning, withCompat)
			}
			if got := assistant.Get("tool_calls.0.function.name").String(); got != "Read" {
				t.Fatalf("tool call name = %q, want Read; output: %s", got, withCompat)
			}

			withoutCompat := TranslateRequest(FormatAnthropic, FormatOpenAI, "deepseek-v4", payload, false)
			if gjson.GetBytes(withoutCompat, "messages.0.reasoning_content").Exists() {
				t.Fatalf("default translation added reasoning_content: %s", withoutCompat)
			}
			if got := gjson.GetBytes(withoutCompat, "messages.0.tool_calls.0.function.name").String(); got != "Read" {
				t.Fatalf("default translation dropped tool call: %s", withoutCompat)
			}
		})
	}
}

func TestRequestThinkingCompatResponsesToClaudePreservesEmptyAndOpaqueSignatures(t *testing.T) {
	for _, source := range []Format{FormatOpenAIResponse, FormatCodex} {
		t.Run(source.String()+"/empty", func(t *testing.T) {
			payload := []byte(`{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"reason"}],"encrypted_content":""}]}`)

			withoutCompat := TranslateRequest(source, FormatAnthropic, "deepseek-v4", payload, false)
			if got := gjson.GetBytes(withoutCompat, "messages.#").Int(); got != 0 {
				t.Fatalf("default preserved empty reasoning: %s", withoutCompat)
			}

			withCompat := TranslateRequestWithOptions(source, FormatAnthropic, "deepseek-v4", payload, false, RequestTranslationOptions{PreserveThinkingBlocks: true})
			part := gjson.GetBytes(withCompat, "messages.0.content.0")
			if part.Get("type").String() != "thinking" || part.Get("thinking").String() != "reason" {
				t.Fatalf("compat missing Claude thinking: %s", withCompat)
			}
			if !part.Get("signature").Exists() || part.Get("signature").String() != "" {
				t.Fatalf("compat missing explicit empty Claude signature: %s", withCompat)
			}
		})

		t.Run(source.String()+"/opaque", func(t *testing.T) {
			payload := []byte(`{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"reason"}],"encrypted_content":"opaque-deepseek-id"}]}`)

			withoutCompat := TranslateRequest(source, FormatAnthropic, "deepseek-v4", payload, false)
			if got := gjson.GetBytes(withoutCompat, "messages.#").Int(); got != 0 {
				t.Fatalf("default preserved opaque reasoning: %s", withoutCompat)
			}

			withCompat := TranslateRequestWithOptions(source, FormatAnthropic, "deepseek-v4", payload, false, RequestTranslationOptions{PreserveThinkingBlocks: true})
			part := gjson.GetBytes(withCompat, "messages.0.content.0")
			if part.Get("type").String() != "thinking" || part.Get("thinking").String() != "reason" || part.Get("signature").String() != "opaque-deepseek-id" {
				t.Fatalf("compat dropped opaque Claude thinking signature: %s", withCompat)
			}
		})
	}
}

func TestRequestThinkingDefaultPreservesTargetCompatibleSignatures(t *testing.T) {
	gptSignature := testRequestThinkingGPTSignature()
	geminiSignature := testRequestThinkingGeminiSignature()

	codexPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"gpt reason","signature":"` + gptSignature + `"}]}]}`)
	codexOut := TranslateRequest(FormatAnthropic, FormatCodex, "gpt-5", codexPayload, false)
	if got := gjson.GetBytes(codexOut, "input.0.encrypted_content").String(); got != gptSignature {
		t.Fatalf("default dropped GPT-compatible Codex signature: %s", codexOut)
	}

	openAIOut := TranslateRequest(FormatAnthropic, FormatOpenAI, "gpt-5", codexPayload, false)
	if got := gjson.GetBytes(openAIOut, "messages.0.reasoning_content").String(); got != "gpt reason" {
		t.Fatalf("default dropped GPT-compatible OpenAI reasoning_content: %s", openAIOut)
	}

	geminiPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"gemini reason","signature":"` + geminiSignature + `"}]}]}`)
	geminiOut := TranslateRequest(FormatAnthropic, FormatGemini, "gemini-3-pro", geminiPayload, false)
	part := gjson.GetBytes(geminiOut, "contents.1.parts.0")
	if part.Get("text").String() != "gemini reason" || part.Get("thoughtSignature").String() != geminiSignature {
		t.Fatalf("default dropped Gemini-compatible thought signature: %s", geminiOut)
	}
}

func testRequestThinkingGPTSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	payload[8] = 1
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func testRequestThinkingGeminiSignature() string {
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39})

	var outer []byte
	outer = protowire.AppendTag(outer, 2, protowire.BytesType)
	outer = protowire.AppendBytes(outer, inner)
	return base64.StdEncoding.EncodeToString(outer)
}
