package oagmsg

import (
	"strings"

	"github.com/tidwall/gjson"
)

func selectResponseToolDescriptorIndex(candidates ...[]byte) toolDescriptorIndex {
	for _, raw := range candidates {
		if len(strings.TrimSpace(string(raw))) == 0 || validateJSONObject(raw) != nil {
			continue
		}
		return buildToolDescriptorIndex(raw)
	}
	return toolDescriptorIndex{}
}

func (c *TranslationContext) responseToolDescriptor(name string) (toolDescriptor, bool) {
	if c == nil || !c.responseToolMetadataApplicable() {
		return toolDescriptor{}, false
	}
	index := c.responseToolDescriptorIndex()
	name = strings.TrimSpace(c.RestoreToolName(name))
	if name != "" {
		if descriptor, ok := index.lookup(name); ok && responseToolDescriptorConvertible(descriptor) {
			return descriptor, true
		}
		return toolDescriptor{}, false
	}
	return c.singleUnnamedCustomToolDescriptor()
}

func (c *TranslationContext) singleUnnamedCustomToolDescriptor() (toolDescriptor, bool) {
	if resolveFormat(c.TargetFormat) == FormatAnthropic {
		return toolDescriptor{}, false
	}
	var single toolDescriptor
	found := false
	seen := make(map[string]bool)
	index := c.responseToolDescriptorIndex()
	for _, descriptor := range index.descriptors {
		if descriptor.name == "" || seen[descriptor.name] {
			continue
		}
		seen[descriptor.name] = true
		winner, ok := index.winner(descriptor.name)
		if !ok || !responseToolDescriptorConvertible(winner) {
			continue
		}
		if winner.toolType != "custom" || found {
			return toolDescriptor{}, false
		}
		single = winner
		found = true
	}
	return single, found
}

func (c *TranslationContext) responseToolMetadataApplicable() bool {
	if c == nil || resolveFormat(c.SourceFormat) != FormatOpenAIResponse {
		return false
	}
	switch resolveFormat(c.TargetFormat) {
	case FormatOpenAI, FormatAnthropic, FormatCodex:
		return true
	default:
		return false
	}
}

func (c *TranslationContext) responseToolDescriptorIndex() toolDescriptorIndex {
	if c != nil && resolveFormat(c.TargetFormat) == FormatOpenAI {
		return firstOrderToolDescriptorIndex(c.responseTools)
	}
	return c.responseTools
}

func responseToolDescriptorConvertible(descriptor toolDescriptor) bool {
	return descriptor.toolType == "function" || descriptor.toolType == "custom"
}

func restoreUnifiedResponseToolNames(resp *UnifiedResponse, ctx *TranslationContext) {
	if resp == nil || ctx == nil {
		return
	}
	for i, call := range resp.ToolCalls {
		if name, ok := call["name"].(string); ok {
			call["name"] = ctx.RestoreToolName(name)
		}
		if function, ok := call["function"].(map[string]any); ok {
			if name, okName := function["name"].(string); okName {
				function["name"] = ctx.RestoreToolName(name)
			}
		}
		if functionCall, ok := call["functionCall"].(map[string]any); ok {
			if name, okName := functionCall["name"].(string); okName {
				functionCall["name"] = ctx.RestoreToolName(name)
			}
		}
		if descriptor, ok := ctx.responseToolDescriptorForCall(call); ok {
			resp.ToolCalls[i] = responseToolCallForDescriptor(call, descriptor)
		}
	}
	for i, call := range resp.responsesOutput {
		if len(call) == 1 {
			if _, ok := call[oagmsgResponsesOutputItemMarker]; ok {
				continue
			}
		}
		if name, ok := call["name"].(string); ok {
			call["name"] = ctx.RestoreToolName(name)
		}
		if function, ok := call["function"].(map[string]any); ok {
			if name, okName := function["name"].(string); okName {
				function["name"] = ctx.RestoreToolName(name)
			}
		}
		if functionCall, ok := call["functionCall"].(map[string]any); ok {
			if name, okName := functionCall["name"].(string); okName {
				functionCall["name"] = ctx.RestoreToolName(name)
			}
		}
		if descriptor, ok := ctx.responseToolDescriptorForCall(call); ok {
			resp.responsesOutput[i] = responseToolCallForDescriptor(call, descriptor)
		}
	}
}

func (c *TranslationContext) responseToolDescriptorForCall(call map[string]any) (toolDescriptor, bool) {
	name := strings.TrimSpace(toolCallName(call))
	restoredName := strings.TrimSpace(c.RestoreToolName(name))
	index := c.responseToolDescriptorIndex()
	if namespace := strings.TrimSpace(stringValue(call["namespace"])); namespace != "" && restoredName != "" {
		if descriptor, ok := index.lookup(qualifyToolDescriptorName(namespace, restoredName)); ok && responseToolDescriptorConvertible(descriptor) {
			return descriptor, true
		}
	}
	descriptor, ok := c.responseToolDescriptor(name)
	if !ok {
		return toolDescriptor{}, false
	}
	return descriptor, true
}

func responseToolCallForDescriptor(call map[string]any, descriptor toolDescriptor) map[string]any {
	id, _, args := extractToolCallFields(call)
	if id == "" {
		id = stringValue(call["call_id"])
	}
	if id == "" {
		id = stringValue(call["id"])
	}
	if id == "" {
		id = generateToolCallID(descriptor.name)
	}
	if descriptor.toolType == "function" {
		outputName := responseOutputToolName(descriptor)
		tool := map[string]any{
			"id":        "fc_" + id,
			"type":      "function_call",
			"status":    "completed",
			"call_id":   id,
			"name":      outputName,
			"arguments": marshalArgsToString(args),
		}
		if descriptor.namespace != "" {
			tool["namespace"] = descriptor.namespace
		}
		return tool
	}
	input := customToolInputFromCall(call, args)
	tool := map[string]any{
		"id":      "ctc_" + id,
		"type":    "custom_tool_call",
		"status":  "completed",
		"call_id": id,
		"name":    descriptor.name,
		"input":   input,
	}
	if descriptor.namespace != "" && descriptor.childName != "" && !strings.HasPrefix(descriptor.namespace, "mcp__") {
		tool["name"] = descriptor.childName
		tool["namespace"] = descriptor.namespace
	}
	return tool
}

func customToolInputFromCall(call map[string]any, args any) string {
	if input, ok := call["input"]; ok {
		if text, okText := input.(string); okText {
			return text
		}
		return marshalArgsToString(input)
	}
	return unwrapCustomToolInput(marshalArgsToString(args))
}

func responseOutputToolName(descriptor toolDescriptor) string {
	if descriptor.toolType == "custom" && strings.HasPrefix(descriptor.namespace, "mcp__") {
		return descriptor.name
	}
	if descriptor.childName != "" {
		return descriptor.childName
	}
	return descriptor.name
}

func toolCallName(call map[string]any) string {
	if name := stringValue(call["name"]); name != "" {
		return name
	}
	if function, ok := call["function"].(map[string]any); ok {
		if name := stringValue(function["name"]); name != "" {
			return name
		}
	}
	if functionCall, ok := call["functionCall"].(map[string]any); ok {
		if name := stringValue(functionCall["name"]); name != "" {
			return name
		}
	}
	return ""
}

func unwrapCustomToolInput(arguments string) string {
	if v := gjson.Get(arguments, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	return arguments
}
