package oagmsg

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func appendResponseToolCall(calls []map[string]any, call map[string]any) []map[string]any {
	if isBlankResponseToolCall(call) {
		return calls
	}
	return append(calls, call)
}

func normalizeResponseToolCallFinish(resp *UnifiedResponse) {
	if resp == nil {
		return
	}
	if resp.FinishReason == "tool_calls" && len(resp.ToolCalls) == 0 {
		resp.FinishReason = "stop"
	}
}

// SanitizeOpenAICompatibleResponse removes invalid blank tool calls from
// OpenAI-compatible non-stream responses without touching genuine tool calls.
func SanitizeOpenAICompatibleResponse(body []byte) []byte {
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body
	}
	out := body
	for index := range choices.Array() {
		path := fmt.Sprintf("choices.%d.message.tool_calls", index)
		toolCalls := gjson.GetBytes(out, path)
		if !toolCalls.IsArray() {
			continue
		}
		changed := false
		kept := make([][]byte, 0, len(toolCalls.Array()))
		for _, toolCall := range toolCalls.Array() {
			var call map[string]any
			if err := json.Unmarshal([]byte(toolCall.Raw), &call); err != nil || isBlankResponseToolCall(call) {
				changed = true
				continue
			}
			kept = append(kept, []byte(toolCall.Raw))
		}
		if !changed {
			continue
		}
		if len(kept) == 0 {
			if updated, errDelete := sjson.DeleteBytes(out, path); errDelete == nil {
				out = updated
			}
			if gjson.GetBytes(out, fmt.Sprintf("choices.%d.finish_reason", index)).String() == "tool_calls" {
				if updated, errSet := sjson.SetBytes(out, fmt.Sprintf("choices.%d.finish_reason", index), "stop"); errSet == nil {
					out = updated
				}
			}
			continue
		}
		if updated, errSet := sjson.SetRawBytes(out, path, joinRawArray(kept)); errSet == nil {
			out = updated
		}
	}
	return out
}

func isBlankResponseToolCall(call map[string]any) bool {
	stripped := stripResponsesOutputItemMarker(call)
	if len(stripped) == 0 {
		return true
	}
	if !hasRecognizedToolCallShape(stripped) {
		return false
	}
	_, name, args := extractToolCallFields(stripped)
	if name == "" {
		name = toolCallName(stripped)
	}
	if args == nil {
		args = firstToolCallArgValue(stripped, "arguments", "input")
	}
	return strings.TrimSpace(name) == "" && toolCallArgsBlank(args)
}

func hasRecognizedToolCallShape(call map[string]any) bool {
	if _, ok := call["function"]; ok {
		return true
	}
	if _, ok := call["functionCall"]; ok {
		return true
	}
	switch strings.TrimSpace(stringValue(call["type"])) {
	case "tool_use", "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func firstToolCallArgValue(call map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := call[key]; ok {
			return value
		}
	}
	return nil
}

func toolCallArgsBlank(args any) bool {
	switch value := args.(type) {
	case nil:
		return true
	case string:
		trimmed := strings.TrimSpace(value)
		return trimmed == "" || trimmed == "{}" || trimmed == "null"
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	default:
		return false
	}
}
