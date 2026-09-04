package oagmsg

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	responses_to_claude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	"github.com/tidwall/gjson"
)

func TestAnthropicConsecutiveRoleAccumulationPreservesAssistantBlockOrder(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"x"}},
				{"type":"text","text":"after first call"}
			]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"reason","signature":"sig_1"},
				{"type":"text","text":"final text"},
				{"type":"tool_use","id":"call_2","name":"write","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_2","content":"ok"}]},
			{"role":"user","content":[{"type":"text","text":"continue"}]}
		]
	}`)

	handler := &AnthropicHandler{}
	req, err := handler.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := handler.SerializeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3; output=%s", len(messages), out)
	}
	if got := claudeContentTypes(messages[1]); !reflect.DeepEqual(got, []string{"tool_use", "text", "thinking", "text", "tool_use"}) {
		t.Fatalf("assistant content types = %v, want source order; output=%s", got, out)
	}
	if got := claudeContentTypes(messages[2]); !reflect.DeepEqual(got, []string{"tool_result", "text"}) {
		t.Fatalf("user content types = %v, want accumulated tool result and text; output=%s", got, out)
	}
}

func TestResponsesToClaudeTurnToolAlignmentSkipsEmptyUserItems(t *testing.T) {
	raw := responsesClaudeTurnAlignmentFixture()
	upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", raw, false)

	if got, want := claudeTurnSignature(oag), claudeTurnSignature(upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("turn signature mismatch\noag:      %v\nupstream: %v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
	}

	messages := gjson.GetBytes(oag, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4; output=%s", len(messages), oag)
	}
	if got := claudeContentTypes(messages[2]); !reflect.DeepEqual(got, []string{"tool_result"}) {
		t.Fatalf("tool result turn content types = %v, want only tool_result; output=%s", got, oag)
	}
}

func TestResponsesToClaudeParsesMessageShorthandWithInstructions(t *testing.T) {
	raw := []byte(`{
		"model":"claude-haiku-4-5-20251001",
		"instructions":"Top rule",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]
	}`)
	upstream := responses_to_claude.ConvertOpenAIResponsesRequestToClaude("claude-haiku-4-5-20251001", raw, true)
	oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-haiku-4-5-20251001", raw, true)

	if got, want := claudeTurnSignature(oag), claudeTurnSignature(upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("turn signature mismatch\noag:      %v\nupstream: %v\noagJSON: %s\nupJSON:  %s", got, want, oag, upstream)
	}
	messages := gjson.GetBytes(oag, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1; output=%s", len(messages), oag)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("message role = %q, want user; output=%s", got, oag)
	}
	if got := claudeMessageText(messages[0]); got != "hi" {
		t.Fatalf("message text = %q, want hi; output=%s", got, oag)
	}
	if gjson.GetBytes(oag, `messages.#(role=="system")`).Exists() {
		t.Fatalf("system role leaked into Claude messages; output=%s", oag)
	}
}

func TestResponsesToClaudeServiceTierPriorityMapsSpeed(t *testing.T) {
	tests := []struct {
		name       string
		tier       string
		wantSpeed  string
		wantExists bool
	}{
		{name: "absent"},
		{name: "default", tier: "default"},
		{name: "standard", tier: "standard"},
		{name: "flex", tier: "flex"},
		{name: "priority", tier: "priority", wantSpeed: "fast", wantExists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"claude-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]`
			if tt.tier != "" {
				raw += `,"service_tier":` + strconv.Quote(tt.tier)
			}
			raw += `}`

			oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", []byte(raw), false)
			speed := gjson.GetBytes(oag, "speed")
			if speed.Exists() != tt.wantExists {
				t.Fatalf("speed exists = %v, want %v; output=%s", speed.Exists(), tt.wantExists, oag)
			}
			if tt.wantExists && speed.String() != tt.wantSpeed {
				t.Fatalf("speed = %q, want %q; output=%s", speed.String(), tt.wantSpeed, oag)
			}
		})
	}
}

func TestResponsesToClaudeDeduplicatesDuplicateToolOutputsWithFinalPayload(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},
			{"type":"function_call","call_id":"call.same:1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call.same:1","output":"first"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"between"}]},
			{"type":"function_call_output","call_id":"call.same:1","output":"final","cache_control":{"type":"ephemeral"}}
		]
	}`)

	oag := TranslateRequest(FormatOpenAIResponse, FormatAnthropic, "claude-test", raw, false)
	messages := gjson.GetBytes(oag, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3; output=%s", len(messages), oag)
	}
	content := messages[2].Get("content")
	if got := claudeContentTypes(messages[2]); !reflect.DeepEqual(got, []string{"tool_result", "text"}) {
		t.Fatalf("final user content types = %v, want tool_result,text; output=%s", got, oag)
	}
	toolResult := content.Get("0")
	if got := toolResult.Get("tool_use_id").String(); got != "call_same_1" {
		t.Fatalf("tool_use_id = %q, want sanitized id; output=%s", got, oag)
	}
	if got := toolResult.Get("content").String(); got != "final" {
		t.Fatalf("tool_result content = %q, want final; output=%s", got, oag)
	}
	if got := toolResult.Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tool_result cache_control.type = %q, want ephemeral; output=%s", got, oag)
	}
}

func claudeMessageText(message gjson.Result) string {
	content := message.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	return content.Get("0.text").String()
}

func TestCodexToClaudeTurnToolAlignmentSkipsEmptyUserItems(t *testing.T) {
	raw := responsesClaudeTurnAlignmentFixture()
	oag := TranslateRequest(FormatCodex, FormatAnthropic, "claude-test", raw, false)

	messages := gjson.GetBytes(oag, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4; output=%s", len(messages), oag)
	}
	if got := claudeContentTypes(messages[1]); !reflect.DeepEqual(got, []string{"text", "tool_use"}) {
		t.Fatalf("assistant content types = %v, want text then tool_use; output=%s", got, oag)
	}
	if got := claudeContentTypes(messages[2]); !reflect.DeepEqual(got, []string{"tool_result"}) {
		t.Fatalf("tool result turn content types = %v, want only tool_result; output=%s", got, oag)
	}
}

func TestResponsesAndCodexToClaudePreserveAssistantBlockOrderAroundTools(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"after call"}]},
			{"type":"function_call","call_id":"call_2","name":"write","arguments":"{}"}
		]
	}`)

	for _, source := range []Format{FormatOpenAIResponse, FormatCodex} {
		t.Run(string(source), func(t *testing.T) {
			out := TranslateRequest(source, FormatAnthropic, "claude-test", raw, false)
			messages := gjson.GetBytes(out, "messages").Array()
			if len(messages) != 2 {
				t.Fatalf("message count = %d, want 2; output=%s", len(messages), out)
			}
			if got := claudeContentTypes(messages[1]); !reflect.DeepEqual(got, []string{"tool_use", "text", "tool_use"}) {
				t.Fatalf("assistant content types = %v, want source order; output=%s", got, out)
			}
		})
	}
}

func responsesClaudeTurnAlignmentFixture() []byte {
	return []byte(`{
		"model":"gpt-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"before call"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},
			{"type":"message","role":"user","content":[]},
			{"type":"message","role":"user","content":""},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
		]
	}`)
}

func claudeTurnSignature(raw []byte) []string {
	var signature []string
	for _, message := range gjson.GetBytes(raw, "messages").Array() {
		signature = append(signature, message.Get("role").String()+"|"+strings.Join(claudeContentTypes(message), ","))
	}
	return signature
}

func claudeContentTypes(message gjson.Result) []string {
	content := message.Get("content")
	if content.Type == gjson.String {
		if content.String() == "" {
			return nil
		}
		return []string{"text"}
	}
	var types []string
	for _, block := range content.Array() {
		types = append(types, block.Get("type").String())
	}
	return types
}
