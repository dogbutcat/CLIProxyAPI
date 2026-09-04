package conformance

import (
	"fmt"
	"strings"
	"testing"

	chat_completions "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/chat-completions"
	codex_openai "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	openai_claude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
	"github.com/tidwall/gjson"
)

// requestFixture is a test case that exercises a specific protocol translation path.
type requestFixture struct {
	Name  string
	Input string
	Model string
}

// semanticDiff compares two translated JSONs on key structural fields.
// Returns a list of human-readable difference descriptions.
func semanticDiff(upstream, oagResult []byte) []string {
	var diffs []string

	// Model
	upModel := gjson.GetBytes(upstream, "model").String()
	oagModel := gjson.GetBytes(oagResult, "model").String()
	if upModel != oagModel {
		// Not counted when ToWithModel is used; kept for diagnostic.
		diffs = append(diffs, fmt.Sprintf("model: upstream=%q oagmsg=%q", upModel, oagModel))
	}

	// Message count
	upMsgCount := len(gjson.GetBytes(upstream, "messages").Array())
	oagMsgCount := len(gjson.GetBytes(oagResult, "messages").Array())
	if upMsgCount != oagMsgCount {
		diffs = append(diffs, fmt.Sprintf("msg_count: upstream=%d oagmsg=%d", upMsgCount, oagMsgCount))
	}

	// System field
	upSys := gjson.GetBytes(upstream, "system")
	oagSys := gjson.GetBytes(oagResult, "system")
	if upSys.Exists() != oagSys.Exists() {
		diffs = append(diffs, fmt.Sprintf("has_system: upstream=%v oagmsg=%v", upSys.Exists(), oagSys.Exists()))
	} else if upSys.Exists() && oagSys.Exists() {
		if upSys.Type != oagSys.Type {
			diffs = append(diffs, fmt.Sprintf("system_type: upstream=%v oagmsg=%v", upSys.Type, oagSys.Type))
		}
	}

	// Tools count
	upToolCount := len(gjson.GetBytes(upstream, "tools").Array())
	oagToolCount := len(gjson.GetBytes(oagResult, "tools").Array())
	if upToolCount != oagToolCount {
		diffs = append(diffs, fmt.Sprintf("tools_count: upstream=%d oagmsg=%d", upToolCount, oagToolCount))
	}

	// Thinking config
	upThinking := gjson.GetBytes(upstream, "thinking")
	oagThinking := gjson.GetBytes(oagResult, "thinking")
	if upThinking.Exists() != oagThinking.Exists() {
		diffs = append(diffs, fmt.Sprintf("has_thinking: upstream=%v oagmsg=%v", upThinking.Exists(), oagThinking.Exists()))
	}

	// max_tokens
	upMax := gjson.GetBytes(upstream, "max_tokens")
	oagMax := gjson.GetBytes(oagResult, "max_tokens")
	if upMax.Exists() != oagMax.Exists() {
		diffs = append(diffs, fmt.Sprintf("has_max_tokens: upstream=%v oagmsg=%v", upMax.Exists(), oagMax.Exists()))
	}

	// Stream
	upStream := gjson.GetBytes(upstream, "stream")
	oagStream := gjson.GetBytes(oagResult, "stream")
	if upStream.Bool() != oagStream.Bool() {
		diffs = append(diffs, fmt.Sprintf("stream: upstream=%v oagmsg=%v", upStream.Bool(), oagStream.Bool()))
	}

	// Per-message content block counts
	upMsgs := gjson.GetBytes(upstream, "messages").Array()
	oagMsgs := gjson.GetBytes(oagResult, "messages").Array()
	minMsgs := len(upMsgs)
	if len(oagMsgs) < minMsgs {
		minMsgs = len(oagMsgs)
	}
	for i := 0; i < minMsgs; i++ {
		upRole := upMsgs[i].Get("role").String()
		oagRole := oagMsgs[i].Get("role").String()
		if upRole != oagRole {
			diffs = append(diffs, fmt.Sprintf("msg[%d].role: upstream=%q oagmsg=%q", i, upRole, oagRole))
		}
		upBlockCount := len(upMsgs[i].Get("content").Array())
		oagBlockCount := len(oagMsgs[i].Get("content").Array())
		if upBlockCount != oagBlockCount {
			diffs = append(diffs, fmt.Sprintf("msg[%d].content_blocks: upstream=%d oagmsg=%d", i, upBlockCount, oagBlockCount))
		}
		// Compare content block types
		minBlocks := upBlockCount
		if oagBlockCount < minBlocks {
			minBlocks = oagBlockCount
		}
		for j := 0; j < minBlocks; j++ {
			upType := upMsgs[i].Get(fmt.Sprintf("content.%d.type", j)).String()
			oagType := oagMsgs[i].Get(fmt.Sprintf("content.%d.type", j)).String()
			if upType != oagType {
				diffs = append(diffs, fmt.Sprintf("msg[%d].content[%d].type: upstream=%q oagmsg=%q", i, j, upType, oagType))
			}
		}
	}

	return diffs
}

// isExpectedDrift returns true for diffs that are known design choices, not bugs.
func isExpectedDrift(diff string) bool {
	// Content simplification: oagmsg simplifies single-text-block to string.
	// Both are valid protocol formats; this is an intentional optimization.
	if strings.Contains(diff, "content[0].type:") && strings.Contains(diff, `oagmsg=""`) {
		return true
	}
	// System simplification: oagmsg may simplify single-text system to string.
	if strings.Contains(diff, "system_type:") {
		return true
	}
	return false
}

// openAIToClaudeFixtures covers OpenAI Chat Completions → Anthropic /v1/messages
var openAIToClaudeFixtures = []requestFixture{
	{
		Name:  "basic_user_message",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "system_message",
		Input: `{"model":"gpt-4","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Hi"}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "developer_role",
		Input: `{"model":"gpt-4","messages":[{"role":"developer","content":"Be concise"},{"role":"user","content":"Hi"}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "multipart_content",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "tool_calls_and_results",
		Input: `{"model":"gpt-4","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"file content"}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "parallel_tool_results",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"Use both tools."},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"tool_a","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"tool_b","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"one"},{"role":"tool","tool_call_id":"call_2","content":"two"},{"role":"assistant","content":"Done."}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "reasoning_effort",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"think hard"}],"reasoning_effort":"high"}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "tool_definitions",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","description":"read file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "tool_choice_auto",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"tool_choice":"auto"}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "tool_choice_required",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"tool_choice":"required"}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "temperature_and_top_p",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"temperature":0.7,"top_p":0.9}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "max_tokens",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"max_tokens":1000}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "stop_sequences",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stop":["END","STOP"]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "stream_true",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "file_content_block",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"text","text":"read this"},{"type":"file","file":{"filename":"test.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "cache_control_on_content_part",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"text","text":"cached text","cache_control":{"type":"ephemeral"}}]}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "multiple_system_messages",
		Input: `{"model":"gpt-4","messages":[{"role":"system","content":"first"},{"role":"system","content":"second"},{"role":"user","content":"hi"}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "assistant_with_text_and_tool_calls",
		Input: `{"model":"gpt-4","messages":[{"role":"assistant","content":"I will call a tool","tool_calls":[{"id":"call_x","type":"function","function":{"name":"run","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_x","content":"ok"}]}`,
		Model: "claude-sonnet-4-20250514",
	},
	{
		Name:  "tool_call_id_sanitization",
		Input: `{"model":"gpt-4","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"call.with space:1","type":"function","function":{"name":"Read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call.with space:1","content":"ok"}]}`,
		Model: "claude-sonnet-4-20250514",
	},
}

func TestConformance_OpenAIToClaude_Request(t *testing.T) {
	pass, drift, fail := 0, 0, 0

	for _, f := range openAIToClaudeFixtures {
		t.Run(f.Name, func(t *testing.T) {
			// Oracle: upstream translator
			upstream := chat_completions.ConvertOpenAIRequestToClaude(f.Model, []byte(f.Input), false)

			// Subject: oagmsg — use ToWithModel to match upstream model override
			oagResult, err := oagmsg.From(oagmsg.FormatOpenAI).Request([]byte(f.Input)).ToWithModel(oagmsg.FormatAnthropic, f.Model)
			if err != nil {
				fail++
				t.Fatalf("oagmsg error: %v", err)
			}

			diffs := semanticDiff(upstream, oagResult)
			// Filter out expected design-choice differences
			var realDiffs []string
			for _, d := range diffs {
				if !isExpectedDrift(d) {
					realDiffs = append(realDiffs, d)
				}
			}
			if len(realDiffs) > 0 {
				drift++
				t.Errorf("semantic drift:\n  %s", strings.Join(realDiffs, "\n  "))
			} else {
				pass++
			}
		})
	}

	t.Logf("\n=== OpenAI→Claude Request Conformance ===")
	t.Logf("  ✅ Match: %d", pass)
	t.Logf("  🔄 Drift: %d", drift)
	t.Logf("  ❌ Fail:  %d", fail)
	t.Logf("  Total:   %d", pass+drift+fail)
}

// claudeToOpenAIFixtures covers Anthropic /v1/messages → OpenAI response
var claudeToOpenAIFixtures = []requestFixture{
	{
		Name:  "basic_claude_request",
		Input: `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hello"}],"max_tokens":1024}`,
		Model: "gpt-4",
	},
	{
		Name:  "claude_with_system",
		Input: `{"model":"claude-sonnet-4-20250514","system":"You are a helper","messages":[{"role":"user","content":"Hi"}],"max_tokens":1024}`,
		Model: "gpt-4",
	},
	{
		Name:  "claude_with_thinking",
		Input: `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"think"}],"thinking":{"type":"enabled","budget_tokens":5000},"max_tokens":1024}`,
		Model: "gpt-4",
	},
	{
		Name:  "claude_with_tools",
		Input: `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","description":"read file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"max_tokens":1024}`,
		Model: "gpt-4",
	},
}

func TestConformance_ClaudeToOpenAI_Request(t *testing.T) {
	pass, drift, fail := 0, 0, 0

	for _, f := range claudeToOpenAIFixtures {
		t.Run(f.Name, func(t *testing.T) {
			// Oracle: upstream translator
			upstream := openai_claude.ConvertClaudeRequestToOpenAI(f.Model, []byte(f.Input), false)

			// Subject: oagmsg — use ToWithModel to match upstream model override
			oagResult, err := oagmsg.From(oagmsg.FormatAnthropic).Request([]byte(f.Input)).ToWithModel(oagmsg.FormatOpenAI, f.Model)
			if err != nil {
				fail++
				t.Fatalf("oagmsg error: %v", err)
			}

			diffs := semanticDiff(upstream, oagResult)
			var realDiffs []string
			for _, d := range diffs {
				if !isExpectedDrift(d) {
					realDiffs = append(realDiffs, d)
				}
			}
			if len(realDiffs) > 0 {
				drift++
				t.Errorf("semantic drift:\n  %s", strings.Join(realDiffs, "\n  "))
			} else {
				pass++
			}
		})
	}

	t.Logf("\n=== Claude→OpenAI Request Conformance ===")
	t.Logf("  ✅ Match: %d", pass)
	t.Logf("  🔄 Drift: %d", drift)
	t.Logf("  ❌ Fail:  %d", fail)
	t.Logf("  Total:   %d", pass+drift+fail)
}

// openAIToCodexFixtures covers OpenAI Chat Completions → Codex Responses API
var openAIToCodexFixtures = []requestFixture{
	{
		Name:  "basic_message",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}`,
		Model: "codex-mini-latest",
	},
	{
		Name:  "with_system",
		Input: `{"model":"gpt-4","messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"Hi"}]}`,
		Model: "codex-mini-latest",
	},
	{
		Name:  "with_tools",
		Input: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","description":"read file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`,
		Model: "codex-mini-latest",
	},
	{
		Name:  "tool_calls_and_results",
		Input: `{"model":"gpt-4","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"file content"}]}`,
		Model: "codex-mini-latest",
	},
}

func TestConformance_OpenAIToCodex_Request(t *testing.T) {
	pass, drift, fail := 0, 0, 0

	for _, f := range openAIToCodexFixtures {
		t.Run(f.Name, func(t *testing.T) {
			// Oracle: upstream translator
			upstream := codex_openai.ConvertOpenAIRequestToCodex(f.Model, []byte(f.Input), false)

			// Subject: oagmsg — use ToWithModel
			oagResult, err := oagmsg.From(oagmsg.FormatOpenAI).Request([]byte(f.Input)).ToWithModel(oagmsg.FormatCodex, f.Model)
			if err != nil {
				fail++
				t.Fatalf("oagmsg error: %v", err)
			}

			// Codex uses input[] instead of messages[]
			upInputCount := len(gjson.GetBytes(upstream, "input").Array())
			oagInputCount := len(gjson.GetBytes(oagResult, "input").Array())

			var diffs []string
			upModel := gjson.GetBytes(upstream, "model").String()
			oagModel := gjson.GetBytes(oagResult, "model").String()
			if upModel != oagModel {
				diffs = append(diffs, fmt.Sprintf("model: upstream=%q oagmsg=%q", upModel, oagModel))
			}
			if upInputCount != oagInputCount {
				diffs = append(diffs, fmt.Sprintf("input_count: upstream=%d oagmsg=%d", upInputCount, oagInputCount))
			}

			if len(diffs) > 0 {
				drift++
				t.Errorf("semantic drift:\n  %s", strings.Join(diffs, "\n  "))
			} else {
				pass++
			}
		})
	}

	t.Logf("\n=== OpenAI→Codex Request Conformance ===")
	t.Logf("  ✅ Match: %d", pass)
	t.Logf("  🔄 Drift: %d", drift)
	t.Logf("  ❌ Fail:  %d", fail)
	t.Logf("  Total:   %d", pass+drift+fail)
}
