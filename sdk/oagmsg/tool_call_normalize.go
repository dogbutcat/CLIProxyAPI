package oagmsg

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// NormalizeToolCallToOpenAI converts any known tool call shape to OpenAI chat
// completions response format.
func NormalizeToolCallToOpenAI(call map[string]any) map[string]any {
	id, name, args := extractToolCallFields(call)
	if name == "" {
		return call
	}
	if _, ok := call["function"]; ok {
		return call
	}
	if id == "" {
		id = generateToolCallID(name)
	}
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": marshalArgsToString(args),
		},
	}
}

// NormalizeToolCallToAnthropic converts any known tool call shape to Anthropic
// content block format.
func NormalizeToolCallToAnthropic(call map[string]any) map[string]any {
	id, name, args := extractToolCallFields(call)
	if name == "" {
		return call
	}
	if typ, _ := call["type"].(string); typ == "tool_use" {
		return call
	}
	if id == "" {
		id = generateToolCallID(name)
	}
	return map[string]any{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": unmarshalArgsToObject(args),
	}
}

// NormalizeToolCallToInteractions converts any known tool call shape to
// Responses API function_call format.
func NormalizeToolCallToInteractions(call map[string]any) map[string]any {
	id, name, args := extractToolCallFields(call)
	if name == "" {
		return call
	}
	if typ, _ := call["type"].(string); typ == "function_call" {
		if _, ok := call["call_id"]; ok {
			return call
		}
	}
	if id == "" {
		id = generateToolCallID(name)
	}
	return map[string]any{
		"type":      "function_call",
		"call_id":   id,
		"name":      name,
		"arguments": marshalArgsToString(args),
	}
}

// NormalizeToolCallToGemini converts any known tool call shape to Gemini
// functionCall part format.
func NormalizeToolCallToGemini(call map[string]any) map[string]any {
	id, name, args := extractToolCallFields(call)
	if name == "" {
		return call
	}
	if _, ok := call["functionCall"]; ok {
		return call
	}
	functionCall := map[string]any{
		"name": name,
		"args": unmarshalArgsToObject(args),
	}
	if id != "" {
		functionCall["id"] = id
	}
	return map[string]any{"functionCall": functionCall}
}

func extractToolCallFields(call map[string]any) (id string, name string, args any) {
	if fnRaw, ok := call["function"]; ok {
		if fn, ok := fnRaw.(map[string]any); ok {
			id, _ = call["id"].(string)
			name, _ = fn["name"].(string)
			args = fn["arguments"]
			return
		}
	}
	if typ, _ := call["type"].(string); typ == "tool_use" {
		id, _ = call["id"].(string)
		name, _ = call["name"].(string)
		args = call["input"]
		return
	}
	if typ, _ := call["type"].(string); typ == "function_call" {
		id, _ = call["call_id"].(string)
		name, _ = call["name"].(string)
		args = call["arguments"]
		return
	}
	if fcRaw, ok := call["functionCall"]; ok {
		if fc, ok := fcRaw.(map[string]any); ok {
			id, _ = fc["id"].(string)
			name, _ = fc["name"].(string)
			args = fc["args"]
			return
		}
	}
	return
}

func marshalArgsToString(args any) string {
	if args == nil {
		return "{}"
	}
	switch v := args.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return "{}"
		}
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

func unmarshalArgsToObject(args any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	switch v := args.(type) {
	case map[string]any:
		return v
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return map[string]any{}
		}
		return m
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return map[string]any{}
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return map[string]any{}
		}
		return m
	}
}

func generateToolCallID(name string) string {
	safeName := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	return fmt.Sprintf("call_%s_%d", safeName, time.Now().UnixNano())
}
