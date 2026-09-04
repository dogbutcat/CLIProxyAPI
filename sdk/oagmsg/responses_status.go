package oagmsg

import "strings"

func responsesIncompleteReasonForFinishReason(reason string) (string, bool) {
	switch strings.TrimSpace(reason) {
	case "length", "max_tokens", "max_output_tokens":
		return "max_output_tokens", true
	case "content_filter", "refusal", "sensitive":
		return "content_filter", true
	default:
		return "", false
	}
}

func responsesStatusForFinishReason(reason string) (string, string, bool) {
	incompleteReason, incomplete := responsesIncompleteReasonForFinishReason(reason)
	if incomplete {
		return "incomplete", incompleteReason, true
	}
	return "completed", "", false
}

func applyResponsesOutputStatus(items []any, status string) {
	if status == "" || status == "completed" {
		return
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(m["type"]) {
		case "message", "function_call", "custom_tool_call":
			m["status"] = status
		}
	}
}
