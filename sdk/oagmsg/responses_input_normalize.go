package oagmsg

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeResponsesInputToolCallOutputs(items []gjson.Result) []gjson.Result {
	if len(items) == 0 {
		return items
	}
	pending := make([]responsesPendingToolCall, 0)
	explicitCounts := responsesExplicitOutputCounts(items)
	normalized := make([]gjson.Result, 0, len(items))
	for _, item := range items {
		switch responsesInputItemType(item) {
		case "function_call", "custom_tool_call":
			callID := responsesInputCallID(item)
			if callID != "" {
				pending = append(pending, responsesPendingToolCall{
					id:   callID,
					name: strings.TrimSpace(item.Get("name").String()),
				})
				if !item.Get("call_id").Exists() {
					item = responsesInputItemWithCallID(item, callID)
				}
			}
		case "function_call_output", "custom_tool_call_output":
			callID := responsesInputCallID(item)
			if callID != "" {
				explicitCounts[callID]--
				normalized = append(normalized, responsesInputItemWithCallID(item, callID))
				continue
			}
			if matchIdx := matchResponsesPendingOutput(pending, item, explicitCounts); matchIdx >= 0 {
				callID = pending[matchIdx].id
				pending = append(pending[:matchIdx], pending[matchIdx+1:]...)
				item = responsesInputItemWithCallID(item, callID)
			}
		}
		normalized = append(normalized, item)
	}
	return normalized
}

type responsesPendingToolCall struct {
	id   string
	name string
}

func responsesExplicitOutputCounts(items []gjson.Result) map[string]int {
	counts := make(map[string]int)
	for _, item := range items {
		itemType := responsesInputItemType(item)
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			continue
		}
		if callID := responsesInputCallID(item); callID != "" {
			counts[callID]++
		}
	}
	return counts
}

func matchResponsesPendingOutput(pending []responsesPendingToolCall, item gjson.Result, explicitCounts map[string]int) int {
	outputName := strings.TrimSpace(item.Get("name").String())
	if outputName != "" {
		for idx, call := range pending {
			if explicitCounts[call.id] > 0 {
				continue
			}
			if call.name == outputName {
				return idx
			}
		}
	}
	for idx, call := range pending {
		if explicitCounts[call.id] > 0 {
			continue
		}
		if outputName == "" || call.name == "" || call.name == outputName {
			return idx
		}
	}
	return -1
}

func responsesInputCallID(item gjson.Result) string {
	for _, path := range []string{"call_id", "tool_call_id", "callId", "id"} {
		if value := item.Get(path); value.Type == gjson.String {
			if id := strings.TrimSpace(value.String()); id != "" {
				return id
			}
		}
	}
	return ""
}

func responsesInputItemWithCallID(item gjson.Result, callID string) gjson.Result {
	if callID == "" {
		return item
	}
	updated, err := sjson.Set(item.Raw, "call_id", callID)
	if err != nil {
		return item
	}
	return gjson.Parse(updated)
}
