package oagmsg

import (
	"bytes"
	"fmt"
	"strings"
)

type accumulatedToolCall struct {
	id        string
	name      string
	toolType  string
	arguments strings.Builder
	index     int
}

func aggregateSSEResponse(source Format, raw []byte, ctx *TranslationContext) (*UnifiedResponse, bool, error) {
	if !looksLikeSSE(raw) {
		return nil, false, nil
	}
	handler, ok := DefaultRegistry().Get(resolveFormat(source))
	if !ok {
		return nil, true, fmt.Errorf("oagmsg: no handler for SSE source %q", source)
	}
	streamHandler, ok := handler.(StreamHandler)
	if !ok {
		return nil, true, fmt.Errorf("oagmsg: source handler %q does not support SSE", source)
	}

	response := NewUnifiedResponse()
	response.Model = ""
	response.Content = ""
	response.FinishReason = ""
	response.Created = 0
	tools := make(map[string]*accumulatedToolCall)
	var toolOrder []string
	parsed := false
	middlewareSession := &StreamTranslateSession{ctx: ctx}

	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		payload, okPayload := sseDataPayload(line)
		if !okPayload || bytes.Equal(payload, doneSignal) {
			continue
		}
		deltas, err := streamHandler.ParseStreamChunk(payload)
		if err != nil {
			return nil, true, err
		}
		if len(deltas) > 0 {
			parsed = true
		}
		if ctx != nil {
			deltas = middlewareSession.applyMiddleware(deltas)
		}
		for _, delta := range deltas {
			accumulateResponseDelta(response, tools, &toolOrder, delta)
		}
	}
	if !parsed {
		return nil, true, fmt.Errorf("oagmsg: SSE payload contained no recognized %s events", source)
	}
	for _, key := range toolOrder {
		tool := tools[key]
		if tool == nil || tool.name == "" && tool.toolType == "custom" {
			continue
		}
		if ctx != nil && ctx.responseToolMetadataApplicable() {
			if tool.toolType == "custom" {
				response.ToolCalls = append(response.ToolCalls, map[string]any{
					"id":      "ctc_" + tool.id,
					"type":    "custom_tool_call",
					"status":  "completed",
					"call_id": tool.id,
					"name":    tool.name,
					"input":   unwrapCustomToolInput(tool.arguments.String()),
				})
				continue
			}
			response.ToolCalls = append(response.ToolCalls, map[string]any{
				"id":        "fc_" + tool.id,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   tool.id,
				"name":      tool.name,
				"arguments": tool.arguments.String(),
			})
			continue
		}
		if tool.toolType == "custom" {
			response.ToolCalls = append(response.ToolCalls, map[string]any{
				"id":      "ctc_" + tool.id,
				"type":    "custom_tool_call",
				"status":  "completed",
				"call_id": tool.id,
				"name":    tool.name,
				"input":   unwrapCustomToolInput(tool.arguments.String()),
			})
			continue
		}
		response.ToolCalls = append(response.ToolCalls, map[string]any{
			"id":   tool.id,
			"type": "function",
			"function": map[string]any{
				"name":      tool.name,
				"arguments": tool.arguments.String(),
			},
		})
	}
	if response.FinishReason == "" {
		response.FinishReason = "stop"
	}
	if len(response.ToolCalls) > 0 {
		response.FinishReason = "tool_calls"
	}
	return response, true, nil
}

func looksLikeSSE(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.HasPrefix(trimmed, dataPrefix2) || bytes.HasPrefix(trimmed, eventPrefix) || bytes.Contains(trimmed, []byte("\ndata:"))
}

func sseDataPayload(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, dataPrefix2) {
		return nil, false
	}
	payload := bytes.TrimSpace(line[len(dataPrefix2):])
	if len(payload) == 0 {
		return nil, false
	}
	return payload, true
}

func accumulateResponseDelta(response *UnifiedResponse, tools map[string]*accumulatedToolCall, order *[]string, delta StreamDelta) {
	switch delta.Type {
	case EventStart:
		if delta.ID != "" {
			response.ID = delta.ID
		}
		if delta.Model != "" {
			response.Model = delta.Model
		}
		if delta.Created != 0 {
			response.Created = delta.Created
		}
	case EventTextDelta:
		response.Content += delta.Content
	case EventThinkingDelta:
		response.ThinkingContent += delta.Content
		if delta.Signature != "" {
			response.ThinkingSignature = delta.Signature
		}
	case EventToolStart:
		key := streamToolKey(delta)
		tool := tools[key]
		if tool == nil {
			tool = &accumulatedToolCall{id: delta.ToolCallID, index: delta.ToolIndex, toolType: delta.ToolType}
			tools[key] = tool
			*order = append(*order, key)
		}
		if delta.ToolCallID != "" {
			tool.id = delta.ToolCallID
		}
		if delta.ToolName != "" {
			tool.name = delta.ToolName
		}
		if delta.ToolType != "" {
			tool.toolType = delta.ToolType
		}
		if delta.ToolArgs != "" {
			tool.arguments.WriteString(delta.ToolArgs)
		}
	case EventToolDelta, EventToolDone:
		key := streamToolKey(delta)
		tool := tools[key]
		if tool == nil && len(*order) > 0 && delta.ToolCallID == "" {
			key = (*order)[len(*order)-1]
			tool = tools[key]
		}
		if tool == nil {
			tool = &accumulatedToolCall{id: delta.ToolCallID, name: delta.ToolName, index: delta.ToolIndex, toolType: delta.ToolType}
			tools[key] = tool
			*order = append(*order, key)
		}
		if delta.ToolCallID != "" {
			tool.id = delta.ToolCallID
		}
		if delta.ToolName != "" {
			tool.name = delta.ToolName
		}
		if delta.ToolType != "" {
			tool.toolType = delta.ToolType
		}
		if delta.ToolArgs != "" {
			if delta.Type == EventToolDone && tool.arguments.Len() > 0 {
				break
			}
			tool.arguments.WriteString(delta.ToolArgs)
		}
	case EventUsage:
		mergeUnifiedUsage(&response.Usage, delta.Usage)
	case EventDone:
		response.FinishReason = delta.FinishReason
	}
}

func streamToolKey(delta StreamDelta) string {
	if delta.ToolCallID != "" {
		return "id:" + delta.ToolCallID
	}
	return fmt.Sprintf("index:%d", delta.ToolIndex)
}

func mergeUnifiedUsage(target **UnifiedUsage, incoming *UnifiedUsage) {
	if incoming == nil {
		return
	}
	if *target == nil {
		*target = &UnifiedUsage{}
	}
	usage := *target
	if usageHasPrompt(incoming) {
		usage.PromptTokens = incoming.PromptTokens
	}
	if usageHasCompletion(incoming) {
		usage.CompletionTokens = incoming.CompletionTokens
	}
	if usageHasTotal(incoming) || incoming.usagePresence.DerivedTotal {
		usage.TotalTokens = incoming.TotalTokens
	}
	if usageHasCached(incoming) {
		usage.CachedTokens = incoming.CachedTokens
	}
	if usageHasReasoning(incoming) {
		usage.ReasoningTokens = incoming.ReasoningTokens
	}
	if usageHasCacheCreation(incoming) {
		usage.CacheCreationInputTokens = incoming.CacheCreationInputTokens
	}
	if usageHasCacheRead(incoming) {
		usage.CacheReadInputTokens = incoming.CacheReadInputTokens
	}
	mergeUsagePresence(usage, incoming)
	if !usageHasTotal(usage) && !usage.usagePresence.DerivedTotal {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
}
