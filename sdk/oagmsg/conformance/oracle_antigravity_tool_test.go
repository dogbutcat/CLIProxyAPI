package conformance_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

func TestOracleAntigravityWrappedToolRequestSemantics(t *testing.T) {
	original := t85ClaudeToolRequest()
	model := "claude-sonnet-4-20250514"

	oracle := agclaude.ConvertClaudeRequestToAntigravity(model, original, false)
	got := oagmsg.TranslateRequest(oagmsg.FormatAnthropic, oagmsg.FormatAntigravity, model, original, false)

	paths := []string{
		"model",
		"request.contents",
		"request.tools",
		"request.toolConfig",
	}
	t85AssertProjectedJSONEqual(t, "wrapped tool request", got, oracle, paths)
}

func TestOracleAntigravityWrappedToolResponseSemantics(t *testing.T) {
	original := t85ClaudeToolRequest()
	model := "claude-sonnet-4-20250514"
	translated := agclaude.ConvertClaudeRequestToAntigravity(model, original, false)
	raw := t85AntigravityWrappedToolResponse()

	oracle := agclaude.ConvertAntigravityResponseToClaudeNonStream(context.Background(), model, original, translated, raw, nil)
	got := oagmsg.TranslateNonStream(context.Background(), oagmsg.FormatAntigravity, oagmsg.FormatAnthropic, model, original, translated, raw, nil)

	paths := []string{
		"id",
		"model",
		"content",
		"stop_reason",
		"usage",
	}
	t85AssertProjectedJSONEqual(t, "wrapped tool response", got, oracle, paths)
}

func t85AssertProjectedJSONEqual(t *testing.T, name string, got, want []byte, paths []string) {
	t.Helper()
	gotProjected := t82ProjectJSON(t, got, paths, false)
	wantProjected := t82ProjectJSON(t, want, paths, false)
	if reflect.DeepEqual(gotProjected, wantProjected) {
		return
	}
	gotPretty, _ := json.MarshalIndent(gotProjected, "", "  ")
	wantPretty, _ := json.MarshalIndent(wantProjected, "", "  ")
	t.Fatalf("%s mismatch\n got: %s\nwant: %s\nraw got: %s\nraw want: %s", name, gotPretty, wantPretty, got, want)
}

func t85ClaudeToolRequest() []byte {
	return []byte(`{
		"model":"claude-sonnet-4-20250514",
		"messages":[
			{"role":"user","content":"run both tools"},
			{"role":"assistant","content":[
				{"type":"text","text":"calling tools"},
				{"type":"tool_use","id":"call_function","name":"read_file","input":{"path":"a.txt"}},
				{"type":"tool_use","id":"call_custom","name":"shell_exec","input":{"input":"pwd"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_function","content":[{"type":"text","text":"file content"}]},
				{"type":"tool_result","tool_use_id":"call_custom","content":"done"}
			]}
		],
		"tools":[
			{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"name":"shell_exec","description":"Run a shell command","input_schema":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}
		],
		"tool_choice":{"type":"tool","name":"shell_exec"}
	}`)
}

func t85AntigravityWrappedToolResponse() []byte {
	return []byte(`{"response":{
		"responseId":"resp_ag_tools",
		"modelVersion":"gemini-2.5-pro",
		"candidates":[{
			"content":{"role":"model","parts":[
				{"text":"I need both tools."},
				{"functionCall":{"id":"call_function","name":"read_file","args":{"path":"a.txt"}}},
				{"functionCall":{"id":"call_custom","name":"shell_exec","args":{"input":"pwd"}}}
			]},
			"finishReason":"STOP"
		}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":15,"cachedContentTokenCount":4},
		"cpaUsageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":15,"cachedContentTokenCount":4}
	}}`)
}
