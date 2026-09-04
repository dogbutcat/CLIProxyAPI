package oagmsg

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Compile-time check: InteractionsHandler implements StreamHandler.
var _ StreamHandler = (*InteractionsHandler)(nil)

// ParseStreamChunk parses a JSON body from a Codex/Responses API SSE event
// into zero or more StreamDelta events.
func (h *InteractionsHandler) ParseStreamChunk(rawJSON []byte) ([]StreamDelta, error) {
	return h.parseStreamChunkWithState(rawJSON, &streamParseState{})
}

func (h *InteractionsHandler) parseStreamChunkWithState(rawJSON []byte, state *streamParseState) ([]StreamDelta, error) {
	root := gjson.ParseBytes(rawJSON)
	dataType := root.Get("type").String()

	switch dataType {
	case "response.created":
		return []StreamDelta{{
			Type:    EventStart,
			ID:      root.Get("response.id").String(),
			Model:   root.Get("response.model").String(),
			Created: root.Get("response.created_at").Int(),
		}}, nil

	case "response.output_text.delta":
		delta := root.Get("delta").String()
		if delta == "" {
			return nil, nil
		}
		if state != nil {
			state.textDeltaSeen = true
		}
		return []StreamDelta{{
			Type:    EventTextDelta,
			Content: delta,
		}}, nil

	case "response.reasoning_summary_text.delta":
		delta := root.Get("delta").String()
		if delta == "" {
			return nil, nil
		}
		return []StreamDelta{{
			Type:    EventThinkingDelta,
			Content: delta,
		}}, nil

	case "response.reasoning_summary_text.done":
		// End of reasoning — emit a trailing newline like the upstream translator.
		return []StreamDelta{{
			Type:    EventThinkingDelta,
			Content: "\n\n",
		}}, nil

	case "response.web_search_call.searching", "response.web_search_call.in_progress", "response.web_search_call.completed":
		if state != nil {
			state.webSearch.rememberFromEvent(root, gjson.Result{})
		}
		return nil, nil

	case "response.output_item.added":
		item := root.Get("item")
		if !item.Exists() {
			return nil, nil
		}
		itemType := item.Get("type").String()
		if itemType == "web_search_call" {
			if state != nil {
				state.webSearch.rememberFromEvent(root, item)
			}
			return nil, nil
		}
		if itemType != "function_call" && itemType != "custom_tool_call" {
			return nil, nil
		}
		toolType := "function"
		if itemType == "custom_tool_call" {
			toolType = "custom"
		}
		return []StreamDelta{{
			Type:          EventToolStart,
			ToolCallID:    item.Get("call_id").String(),
			ToolName:      item.Get("name").String(),
			ToolType:      toolType,
			ToolIndex:     int(root.Get("output_index").Int()),
			toolNamespace: item.Get("namespace").String(),
		}}, nil

	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		delta := root.Get("delta").String()
		if delta == "" {
			return nil, nil
		}
		return []StreamDelta{{
			Type:       EventToolDelta,
			ToolCallID: root.Get("call_id").String(),
			ToolIndex:  int(root.Get("output_index").Int()),
			ToolArgs:   delta,
		}}, nil

	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		argsField := "arguments"
		if dataType == "response.custom_tool_call_input.done" {
			argsField = "input"
		}
		args := root.Get(argsField).String()
		return []StreamDelta{{
			Type:       EventToolDone,
			ToolCallID: root.Get("call_id").String(),
			ToolIndex:  int(root.Get("output_index").Int()),
			ToolArgs:   args,
		}}, nil

	case "response.image_generation_call.partial_image":
		b64 := root.Get("partial_image_b64").String()
		if b64 == "" {
			return nil, nil
		}
		return []StreamDelta{{
			Type:        EventImageDelta,
			ImageData:   b64,
			ImageFormat: root.Get("output_format").String(),
			ImageItemID: root.Get("item_id").String(),
		}}, nil

	case "response.output_item.done":
		item := root.Get("item")
		if !item.Exists() {
			return nil, nil
		}
		itemType := item.Get("type").String()
		switch itemType {
		case "message":
			if state != nil && state.textDeltaSeen {
				return nil, nil
			}
			var text strings.Builder
			for _, content := range item.Get("content").Array() {
				if content.Get("type").String() == "output_text" {
					text.WriteString(content.Get("text").String())
				}
			}
			if text.Len() == 0 {
				return nil, nil
			}
			return []StreamDelta{{Type: EventTextDelta, Content: text.String()}}, nil
		case "reasoning":
			var deltas []StreamDelta
			if signature := item.Get("encrypted_content").String(); signature != "" {
				deltas = append(deltas, StreamDelta{Type: EventThinkingDelta, Signature: signature})
			}
			return deltas, nil
		case "web_search_call":
			if state == nil {
				state = &streamParseState{}
			}
			state.webSearch.fallbackID = func() string {
				id := fmt.Sprintf("%s%d", codexWebSearchDeferredFallbackPrefix, state.webSearch.fallback)
				state.webSearch.fallback++
				return id
			}
			blocks := codexWebSearchBlocks(root, item, &state.webSearch, true)
			return codexWebSearchStreamDeltas(blocks), nil
		case "image_generation_call":
			b64 := item.Get("result").String()
			if b64 == "" {
				return nil, nil
			}
			return []StreamDelta{{
				Type:        EventImageDelta,
				ImageData:   b64,
				ImageItemID: item.Get("id").String(),
			}}, nil
		case "function_call", "custom_tool_call":
			// Fallback path: emit complete tool call from output_item.done.
			// The middleware will suppress this if ToolStart+deltas were already emitted.
			toolType := "function"
			if itemType == "custom_tool_call" {
				toolType = "custom"
			}
			callID := item.Get("call_id").String()
			name := item.Get("name").String()
			args := item.Get("arguments").String()
			if itemType == "custom_tool_call" {
				args = item.Get("input").String()
				if args == "" && item.Get("input").Exists() {
					args = item.Get("input").Raw
				}
				if args == "" {
					args = "{}"
				}
			}
			return []StreamDelta{{
				Type:          EventToolDone,
				ToolCallID:    callID,
				ToolName:      name,
				ToolType:      toolType,
				ToolIndex:     int(root.Get("output_index").Int()),
				ToolArgs:      args,
				toolNamespace: item.Get("namespace").String(),
			}}, nil
		}
		return []StreamDelta{{
			Type:     EventToolStart,
			ToolType: streamToolTypeResponsesRawItem,
			ToolArgs: item.Raw,
		}}, nil

	case "response.completed":
		var deltas []StreamDelta
		finishReason := "stop"
		resp := root.Get("response")
		if resp.Get("status").String() == "completed" {
			// Check if tool calls were made to determine finish reason.
			// The upstream translator uses FunctionCallIndex > -1 for this.
		}
		deltas = append(deltas, StreamDelta{
			Type:         EventDone,
			FinishReason: finishReason,
		})

		if usage := resp.Get("usage"); usage.Exists() {
			deltas = append(deltas, StreamDelta{
				Type:  EventUsage,
				Usage: responsesUsage(usage, FormatOpenAIResponse),
			})
		}
		return deltas, nil

	case "response.incomplete":
		nativeReason := root.Get("response.incomplete_details.reason").String()
		finishReason := "stop"
		switch nativeReason {
		case "max_tokens", "max_output_tokens":
			finishReason = "length"
		case "content_filter":
			finishReason = "content_filter"
		}
		return []StreamDelta{{
			Type:               EventDone,
			FinishReason:       finishReason,
			NativeFinishReason: nativeReason,
		}}, nil
	}

	// Unknown event type — silently ignore.
	return nil, nil
}

// NewStreamSerializer creates a stateful serializer using the strategy selected by Mode.
//   - InteractionsModeCodex (default): outputs response.* events (simplified)
//   - InteractionsModeSteps: outputs interaction.*/step.* lifecycle events
//   - InteractionsModeResponsesAPI: outputs full Responses API lifecycle with content_part/output_item envelopes
func (h *InteractionsHandler) NewStreamSerializer(model string) StreamSerializer {
	switch h.Mode {
	case InteractionsModeSteps:
		return newInteractionsStepSerializer(model)
	case InteractionsModeResponsesAPI:
		return newResponsesAPISerializer(model)
	default:
		return &interactionsCodexSerializer{
			model:      model,
			toolStates: make(map[int]interactionsToolState),
		}
	}
}

type interactionsToolState struct {
	callID string
	name   string
}

// interactionsCodexSerializer maintains state for serializing StreamDelta
// events into Codex/Responses API format (response.* events).
type interactionsCodexSerializer struct {
	model         string
	responseID    string
	createdAt     int64
	started       bool
	outputItemIdx int
	usage         *UnifiedUsage
	toolStates    map[int]interactionsToolState
}

// Serialize converts a StreamDelta into Codex SSE data lines.
func (s *interactionsCodexSerializer) Serialize(delta StreamDelta) [][]byte {
	switch delta.Type {
	case EventStart:
		s.started = true
		if delta.ID != "" {
			s.responseID = delta.ID
		}
		if delta.Model != "" {
			s.model = delta.Model
		}
		if delta.Created > 0 {
			s.createdAt = delta.Created
		}
		evt := []byte(`{"type":"response.created","response":{"id":"","model":"","created_at":0}}`)
		evt, _ = sjson.SetBytes(evt, "response.id", s.responseID)
		evt, _ = sjson.SetBytes(evt, "response.model", s.model)
		evt, _ = sjson.SetBytes(evt, "response.created_at", s.createdAt)
		return [][]byte{formatDataLine(evt)}

	case EventTextDelta:
		evt := []byte(`{"type":"response.output_text.delta","delta":""}`)
		evt, _ = sjson.SetBytes(evt, "delta", delta.Content)
		return [][]byte{formatDataLine(evt)}

	case EventThinkingDelta:
		evt := []byte(`{"type":"response.reasoning_summary_text.delta","delta":""}`)
		evt, _ = sjson.SetBytes(evt, "delta", delta.Content)
		return [][]byte{formatDataLine(evt)}

	case EventToolStart:
		if isInternalResponseToolType(delta.ToolType) {
			return nil
		}
		s.outputItemIdx++
		idx := s.outputItemIdx
		s.toolStates[idx] = interactionsToolState{callID: delta.ToolCallID, name: delta.ToolName}

		itemType := "function_call"
		if delta.ToolType == "custom" {
			itemType = "custom_tool_call"
		}
		evt := []byte(`{"type":"response.output_item.added","item":{"type":"","call_id":"","name":""}}`)
		evt, _ = sjson.SetBytes(evt, "item.type", itemType)
		evt, _ = sjson.SetBytes(evt, "item.call_id", delta.ToolCallID)
		evt, _ = sjson.SetBytes(evt, "item.name", delta.ToolName)
		return [][]byte{formatDataLine(evt)}

	case EventToolDelta:
		if isInternalResponseToolType(delta.ToolType) {
			return nil
		}
		evt := []byte(`{"type":"response.function_call_arguments.delta","delta":""}`)
		evt, _ = sjson.SetBytes(evt, "delta", delta.ToolArgs)
		return [][]byte{formatDataLine(evt)}

	case EventToolDone:
		if isInternalResponseToolType(delta.ToolType) {
			return nil
		}
		evt := []byte(`{"type":"response.function_call_arguments.done","arguments":""}`)
		evt, _ = sjson.SetBytes(evt, "arguments", delta.ToolArgs)
		return [][]byte{formatDataLine(evt)}

	case EventImageDelta:
		evt := []byte(`{"type":"response.image_generation_call.partial_image","partial_image_b64":"","output_format":"","item_id":""}`)
		evt, _ = sjson.SetBytes(evt, "partial_image_b64", delta.ImageData)
		evt, _ = sjson.SetBytes(evt, "output_format", delta.ImageFormat)
		evt, _ = sjson.SetBytes(evt, "item_id", delta.ImageItemID)
		return [][]byte{formatDataLine(evt)}

	case EventDone:
		return nil

	case EventUsage:
		if delta.Usage == nil {
			return nil
		}
		s.usage = delta.Usage
		return nil

	case EventPing, EventError:
		return nil
	}
	return nil
}

// Flush emits the final response.completed event.
func (s *interactionsCodexSerializer) Flush() [][]byte {
	evt := []byte(`{"type":"response.completed","response":{"status":"completed"}}`)
	if s.usage != nil {
		evt, _ = sjson.SetBytes(evt, "response.usage.input_tokens", s.usage.PromptTokens)
		evt, _ = sjson.SetBytes(evt, "response.usage.output_tokens", s.usage.CompletionTokens)
		evt, _ = sjson.SetBytes(evt, "response.usage.total_tokens", s.usage.TotalTokens)
	}
	return [][]byte{formatDataLine(evt)}
}
