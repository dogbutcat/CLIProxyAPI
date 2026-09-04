package conformance_test

import (
	"reflect"
	"testing"

	openaiClaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

type openAICompatThinkingProjection struct {
	reasoningExists bool
	reasoning       string
	contentText     string
	toolCallCount   int
	toolCallID      string
	toolName        string
	toolArguments   string
}

func TestOracleOpenAICompatClaudeThinkingWithToolCalls(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "empty signature",
			payload: `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""},{"type":"text","text":"Reading files."},{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"main.go"}}]}]}`,
		},
		{
			name:    "opaque signature",
			payload: `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"claude#opaque"},{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "tool call without thinking",
			payload: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(test.payload)

			oracleCompat := openaiClaude.ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
			oagCompat := oagmsg.TranslateRequestWithOptions(oagmsg.FormatAnthropic, oagmsg.FormatOpenAI, "deepseek-v4", payload, false, oagmsg.RequestTranslationOptions{PreserveThinkingBlocks: true})
			assertOpenAICompatThinkingProjection(t, oracleCompat, oagCompat)

			oracleDefault := openaiClaude.ConvertClaudeRequestToOpenAI("deepseek-v4", payload, false)
			oagDefault := oagmsg.TranslateRequest(oagmsg.FormatAnthropic, oagmsg.FormatOpenAI, "deepseek-v4", payload, false)
			assertOpenAICompatThinkingProjection(t, oracleDefault, oagDefault)
		})
	}
}

func assertOpenAICompatThinkingProjection(t *testing.T, oracle, got []byte) {
	t.Helper()
	oracleProjection := projectOpenAICompatThinking(oracle)
	gotProjection := projectOpenAICompatThinking(got)
	if !reflect.DeepEqual(gotProjection, oracleProjection) {
		t.Fatalf("compat thinking projection mismatch\noracle: %+v\noagmsg: %+v\noracle JSON: %s\noagmsg JSON: %s", oracleProjection, gotProjection, oracle, got)
	}
}

func projectOpenAICompatThinking(payload []byte) openAICompatThinkingProjection {
	assistant := gjson.GetBytes(payload, "messages.0")
	reasoning := assistant.Get("reasoning_content")
	toolCall := assistant.Get("tool_calls.0")
	return openAICompatThinkingProjection{
		reasoningExists: reasoning.Exists(),
		reasoning:       reasoning.String(),
		contentText:     projectOpenAICompatContentText(assistant.Get("content")),
		toolCallCount:   int(assistant.Get("tool_calls.#").Int()),
		toolCallID:      toolCall.Get("id").String(),
		toolName:        toolCall.Get("function.name").String(),
		toolArguments:   toolCall.Get("function.arguments").String(),
	}
}

func projectOpenAICompatContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	return content.Get("#(type==\"text\").text").String()
}
