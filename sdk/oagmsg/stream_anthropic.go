package oagmsg

import (
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Compile-time check: AnthropicHandler implements StreamHandler.
var _ StreamHandler = (*AnthropicHandler)(nil)

// ParseStreamChunk parses a JSON body from an Anthropic SSE event into
// zero or more StreamDelta events. The JSON body should have the data: prefix
// already stripped by the session layer.
func (h *AnthropicHandler) ParseStreamChunk(rawJSON []byte) ([]StreamDelta, error) {
	root := gjson.ParseBytes(rawJSON)
	eventType := root.Get("type").String()

	switch eventType {
	case "message_start":
		msg := root.Get("message")
		if !msg.Exists() {
			return nil, nil
		}
		var deltas []StreamDelta
		deltas = append(deltas, StreamDelta{
			Type:  EventStart,
			ID:    msg.Get("id").String(),
			Model: msg.Get("model").String(),
		})
		// message_start may include initial usage (input_tokens).
		if usage := msg.Get("usage"); usage.Exists() {
			deltas = append(deltas, StreamDelta{
				Type:  EventUsage,
				Usage: claudeUsage(usage),
			})
		}
		return deltas, nil

	case "content_block_start":
		cb := root.Get("content_block")
		if !cb.Exists() {
			return nil, nil
		}
		blockIndex := int(root.Get("index").Int())
		blockType := cb.Get("type").String()

		switch blockType {
		case "tool_use":
			return []StreamDelta{{
				Type:       EventToolStart,
				ToolCallID: cb.Get("id").String(),
				ToolName:   cb.Get("name").String(),
				ToolIndex:  blockIndex,
				ToolType:   "function",
				BlockIndex: blockIndex,
			}}, nil
		case "thinking":
			// thinking block start — content arrives via thinking_delta.
			return nil, nil
		case "text":
			// text block start — content arrives via text_delta.
			return nil, nil
		}
		return nil, nil

	case "content_block_delta":
		delta := root.Get("delta")
		if !delta.Exists() {
			return nil, nil
		}
		blockIndex := int(root.Get("index").Int())
		deltaType := delta.Get("type").String()

		switch deltaType {
		case "text_delta":
			return []StreamDelta{{
				Type:       EventTextDelta,
				Content:    delta.Get("text").String(),
				BlockIndex: blockIndex,
			}}, nil
		case "thinking_delta":
			return []StreamDelta{{
				Type:       EventThinkingDelta,
				Content:    delta.Get("thinking").String(),
				BlockIndex: blockIndex,
			}}, nil
		case "signature_delta":
			return []StreamDelta{{
				Type:       EventThinkingDelta,
				Signature:  delta.Get("signature").String(),
				BlockIndex: blockIndex,
			}}, nil
		case "input_json_delta":
			return []StreamDelta{{
				Type:       EventToolDelta,
				ToolArgs:   delta.Get("partial_json").String(),
				ToolIndex:  blockIndex,
				BlockIndex: blockIndex,
			}}, nil
		}
		return nil, nil

	case "content_block_stop":
		blockIndex := int(root.Get("index").Int())
		return []StreamDelta{{
			Type:       EventToolDone,
			BlockIndex: blockIndex,
			ToolIndex:  blockIndex,
		}}, nil

	case "message_delta":
		var deltas []StreamDelta
		var extra map[string]any
		if d := root.Get("delta"); d.Exists() {
			if sr := d.Get("stop_reason"); sr.Exists() {
				extra = map[string]any{
					"anthropic_stop_reason": mapAnthropicFinishReason(sr.String()),
				}
				if stopSequence := d.Get("stop_sequence").String(); stopSequence != "" {
					extra["anthropic_stop_sequence"] = stopSequence
				}
			}
		}
		if usage := root.Get("usage"); usage.Exists() {
			deltas = append(deltas, StreamDelta{
				Type:  EventUsage,
				Usage: claudeUsage(usage),
				Extra: extra,
			})
		} else if extra != nil {
			deltas = append(deltas, StreamDelta{
				Type:  EventUsage,
				Extra: extra,
			})
		}
		return deltas, nil

	case "message_stop":
		return []StreamDelta{{
			Type:         EventDone,
			FinishReason: "stop",
		}}, nil

	case "ping":
		return []StreamDelta{{Type: EventPing}}, nil

	case "error":
		return []StreamDelta{{
			Type:         EventError,
			ErrorType:    root.Get("error.type").String(),
			ErrorMessage: root.Get("error.message").String(),
		}}, nil
	}

	// Unknown event type — silently ignore.
	return nil, nil
}

// mapAnthropicFinishReason converts Anthropic stop_reason to canonical finish reason.
func mapAnthropicFinishReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence":
		return "stop"
	case "refusal", "sensitive":
		return "content_filter"
	default:
		return reason
	}
}

// NewStreamSerializer creates a stateful serializer that outputs Anthropic
// /v1/messages SSE events (event: + data: lines).
func (h *AnthropicHandler) NewStreamSerializer(model string) StreamSerializer {
	return &anthropicStreamSerializer{
		model:            model,
		nextBlockIndex:   0,
		textBlockIndex:   -1,
		thinkBlockIndex:  -1,
		activeToolBlocks: make(map[int]bool),
	}
}

// anthropicStreamSerializer maintains state for serializing StreamDelta events
// into Anthropic /v1/messages SSE format with content_block lifecycle management.
type anthropicStreamSerializer struct {
	model             string
	messageID         string
	messageStarted    bool
	nextBlockIndex    int
	textBlockIndex    int
	thinkBlockIndex   int
	activeToolBlocks  map[int]bool
	finishReason      string
	stopSequence      string
	inputTokens       int
	outputTokens      int
	cacheReadTokens   int
	webSearchRequests int
	serverSearchID    string
}

// Serialize converts a StreamDelta into zero or more Anthropic SSE event lines.
func (s *anthropicStreamSerializer) Serialize(delta StreamDelta) [][]byte {
	var results [][]byte

	switch delta.Type {
	case EventStart:
		if !s.messageStarted {
			s.messageStarted = true
			if delta.ID != "" {
				s.messageID = delta.ID
			}
			if delta.Model != "" {
				s.model = delta.Model
			}
			msgStart := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
			msgStart, _ = sjson.SetBytes(msgStart, "message.id", s.messageID)
			msgStart, _ = sjson.SetBytes(msgStart, "message.model", s.model)
			if boolExtra(delta.Extra, "anthropic_message_start_usage") && delta.Usage != nil && usageHasPrompt(delta.Usage) {
				s.inputTokens = delta.Usage.PromptTokens
				msgStart, _ = sjson.SetBytes(msgStart, "message.usage.input_tokens", delta.Usage.PromptTokens)
			}
			results = append(results, appendSSEEvent("message_start", msgStart))
		}

	case EventThinkingDelta:
		if !s.messageStarted {
			results = append(results, s.emitMessageStart()...)
		}
		// Start thinking block if needed.
		if s.thinkBlockIndex < 0 {
			s.thinkBlockIndex = s.nextBlockIndex
			s.nextBlockIndex++
			blockStart := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
			blockStart, _ = sjson.SetBytes(blockStart, "index", s.thinkBlockIndex)
			results = append(results, appendSSEEvent("content_block_start", blockStart))
		}
		if delta.Content != "" {
			blockDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			blockDelta, _ = sjson.SetBytes(blockDelta, "index", s.thinkBlockIndex)
			blockDelta, _ = sjson.SetBytes(blockDelta, "delta.thinking", delta.Content)
			results = append(results, appendSSEEvent("content_block_delta", blockDelta))
		}
		if delta.Signature != "" {
			sigDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`)
			sigDelta, _ = sjson.SetBytes(sigDelta, "index", s.thinkBlockIndex)
			sigDelta, _ = sjson.SetBytes(sigDelta, "delta.signature", delta.Signature)
			results = append(results, appendSSEEvent("content_block_delta", sigDelta))
		}

	case EventTextDelta:
		if !s.messageStarted {
			results = append(results, s.emitMessageStart()...)
		}
		forceTextBlock := boolExtra(delta.Extra, "anthropic_force_text_block")
		citations := webSearchCitationsExtra(delta.Extra, "anthropic_citations")
		if forceTextBlock || len(citations) > 0 {
			if s.textBlockIndex >= 0 {
				results = append(results, s.closeBlock(s.textBlockIndex)...)
				s.textBlockIndex = -1
			}
		}
		// Close thinking block if open.
		if s.thinkBlockIndex >= 0 {
			results = append(results, s.closeBlock(s.thinkBlockIndex)...)
			s.thinkBlockIndex = -1
		}
		// Start text block if needed.
		if s.textBlockIndex < 0 {
			s.textBlockIndex = s.nextBlockIndex
			s.nextBlockIndex++
			blockStart := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			if len(citations) > 0 {
				blockStart = []byte(`{"type":"content_block_start","index":0,"content_block":{"citations":[],"type":"text","text":""}}`)
			}
			blockStart, _ = sjson.SetBytes(blockStart, "index", s.textBlockIndex)
			results = append(results, appendSSEEvent("content_block_start", blockStart))
		}
		for _, citation := range citations {
			rawCitation, _ := json.Marshal(anthropicWebSearchCitation(citation, delta.Content))
			citationDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{}}}`)
			citationDelta, _ = sjson.SetBytes(citationDelta, "index", s.textBlockIndex)
			citationDelta, _ = sjson.SetRawBytes(citationDelta, "delta.citation", rawCitation)
			results = append(results, appendSSEEvent("content_block_delta", citationDelta))
		}
		textChunks := []string{delta.Content}
		if chunkSize := intExtra(delta.Extra, "anthropic_text_chunk_runes"); chunkSize > 0 {
			textChunks = splitRunesForWebSearch(delta.Content, chunkSize)
		}
		for _, textChunk := range textChunks {
			blockDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
			blockDelta, _ = sjson.SetBytes(blockDelta, "index", s.textBlockIndex)
			blockDelta, _ = sjson.SetBytes(blockDelta, "delta.text", textChunk)
			results = append(results, appendSSEEvent("content_block_delta", blockDelta))
		}
		if boolExtra(delta.Extra, "anthropic_close_text_block") {
			results = append(results, s.closeBlock(s.textBlockIndex)...)
			s.textBlockIndex = -1
		}

	case EventToolStart:
		if delta.ToolType == streamToolTypeResponsesRawItem {
			break
		}
		if !s.messageStarted {
			results = append(results, s.emitMessageStart()...)
		}
		if s.thinkBlockIndex >= 0 {
			results = append(results, s.closeBlock(s.thinkBlockIndex)...)
			s.thinkBlockIndex = -1
		}
		// Close text block if open (tools come after text).
		if s.textBlockIndex >= 0 {
			results = append(results, s.closeBlock(s.textBlockIndex)...)
			s.textBlockIndex = -1
		}
		blockIdx := s.nextBlockIndex
		s.nextBlockIndex++
		s.activeToolBlocks[blockIdx] = true

		if delta.ToolType == streamToolTypeServerWebSearchResult {
			toolUseID := delta.ToolCallID
			if isCodexWebSearchDeferredFallbackID(toolUseID) || toolUseID == "" {
				toolUseID = s.serverSearchID
			}
			blockStart := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"","content":[]}}`)
			blockStart, _ = sjson.SetBytes(blockStart, "index", blockIdx)
			blockStart, _ = sjson.SetBytes(blockStart, "content_block.tool_use_id", toolUseID)
			if gjson.Valid(delta.ToolArgs) && gjson.Parse(delta.ToolArgs).IsArray() {
				blockStart, _ = sjson.SetRawBytes(blockStart, "content_block.content", []byte(delta.ToolArgs))
			}
			results = append(results, appendSSEEvent("content_block_start", blockStart))
			break
		}

		blockType := "tool_use"
		if delta.ToolType == streamToolTypeServerWebSearch {
			blockType = "server_tool_use"
			if isCodexWebSearchDeferredFallbackID(delta.ToolCallID) || delta.ToolCallID == "" {
				delta.ToolCallID = fmt.Sprintf("web_search_%d", blockIdx)
			}
			s.serverSearchID = delta.ToolCallID
		}
		blockStart := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"","id":"","name":"","input":{}}}`)
		blockStart, _ = sjson.SetBytes(blockStart, "index", blockIdx)
		blockStart, _ = sjson.SetBytes(blockStart, "content_block.type", blockType)
		blockStart, _ = sjson.SetBytes(blockStart, "content_block.id", delta.ToolCallID)
		blockStart, _ = sjson.SetBytes(blockStart, "content_block.name", delta.ToolName)
		if delta.Signature != "" {
			blockStart, _ = sjson.SetBytes(blockStart, "content_block.signature", delta.Signature)
		}
		results = append(results, appendSSEEvent("content_block_start", blockStart))

	case EventToolDelta:
		if delta.ToolArgs != "" {
			// Find the active tool block for this delta. Use the last opened one.
			blockIdx := s.nextBlockIndex - 1
			blockDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
			blockDelta, _ = sjson.SetBytes(blockDelta, "index", blockIdx)
			blockDelta, _ = sjson.SetBytes(blockDelta, "delta.partial_json", delta.ToolArgs)
			results = append(results, appendSSEEvent("content_block_delta", blockDelta))
		}

	case EventToolDone:
		// Close the most recent active tool block.
		blockIdx := s.nextBlockIndex - 1
		if s.activeToolBlocks[blockIdx] {
			results = append(results, s.closeBlock(blockIdx)...)
			delete(s.activeToolBlocks, blockIdx)
		}

	case EventDone:
		s.finishReason = mapOpenAIFinishToAnthropic(delta.FinishReason)
		s.stopSequence = delta.StopSequence

	case EventUsage:
		if delta.Usage != nil {
			if usageHasPrompt(delta.Usage) {
				s.inputTokens = delta.Usage.PromptTokens
			}
			if delta.Usage.CompletionTokens > 0 {
				s.outputTokens = delta.Usage.CompletionTokens
			}
			if usageHasCached(delta.Usage) || usageHasCacheRead(delta.Usage) {
				s.cacheReadTokens = usageCacheReadValue(delta.Usage)
			}
			if delta.Usage.serverToolUseWebSearchRequests > 0 {
				s.webSearchRequests = delta.Usage.serverToolUseWebSearchRequests
			}
		}

	case EventPing:
		results = append(results, appendSSEEvent("ping", []byte(`{"type":"ping"}`)))

	case EventError:
		errJSON := []byte(`{"type":"error","error":{"type":"","message":""}}`)
		errJSON, _ = sjson.SetBytes(errJSON, "error.type", delta.ErrorType)
		errJSON, _ = sjson.SetBytes(errJSON, "error.message", delta.ErrorMessage)
		results = append(results, appendSSEEvent("error", errJSON))
	}

	return results
}

// Flush closes all open blocks and emits message_delta + message_stop.
func (s *anthropicStreamSerializer) Flush() [][]byte {
	var results [][]byte

	// Close thinking block if still open.
	if s.thinkBlockIndex >= 0 {
		results = append(results, s.closeBlock(s.thinkBlockIndex)...)
		s.thinkBlockIndex = -1
	}

	// Close text block if still open.
	if s.textBlockIndex >= 0 {
		results = append(results, s.closeBlock(s.textBlockIndex)...)
		s.textBlockIndex = -1
	}

	// Close any remaining tool blocks.
	for idx := range s.activeToolBlocks {
		results = append(results, s.closeBlock(idx)...)
	}
	s.activeToolBlocks = make(map[int]bool)

	// Emit message_delta with stop_reason and usage.
	if s.finishReason == "" {
		s.finishReason = "end_turn"
	}
	msgDelta := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"output_tokens":0}}`)
	msgDelta, _ = sjson.SetBytes(msgDelta, "delta.stop_reason", s.finishReason)
	if s.stopSequence != "" {
		msgDelta, _ = sjson.SetBytes(msgDelta, "delta.stop_sequence", s.stopSequence)
	}
	msgDelta, _ = sjson.SetBytes(msgDelta, "usage.output_tokens", s.outputTokens)
	if s.webSearchRequests > 0 {
		msgDelta, _ = sjson.SetBytes(msgDelta, "usage.input_tokens", s.inputTokens)
		msgDelta, _ = sjson.SetBytes(msgDelta, "usage.server_tool_use.web_search_requests", s.webSearchRequests)
		if s.cacheReadTokens > 0 {
			msgDelta, _ = sjson.SetBytes(msgDelta, "usage.cache_read_input_tokens", s.cacheReadTokens)
		}
	}
	results = append(results, appendSSEEvent("message_delta", msgDelta))

	// Emit message_stop.
	results = append(results, appendSSEEvent("message_stop", []byte(`{"type":"message_stop"}`)))

	return results
}

func boolExtra(extra map[string]any, key string) bool {
	value, _ := extra[key].(bool)
	return value
}

func intExtra(extra map[string]any, key string) int {
	switch value := extra[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func webSearchCitationsExtra(extra map[string]any, key string) []WebSearchCitation {
	value, _ := extra[key].([]WebSearchCitation)
	return value
}

// emitMessageStart produces the message_start event if not already sent.
func (s *anthropicStreamSerializer) emitMessageStart() [][]byte {
	if s.messageStarted {
		return nil
	}
	s.messageStarted = true
	msgStart := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
	msgStart, _ = sjson.SetBytes(msgStart, "message.id", s.messageID)
	msgStart, _ = sjson.SetBytes(msgStart, "message.model", s.model)
	return [][]byte{appendSSEEvent("message_start", msgStart)}
}

// closeBlock emits a content_block_stop event for the given block index.
func (s *anthropicStreamSerializer) closeBlock(index int) [][]byte {
	blockStop := []byte(`{"type":"content_block_stop","index":0}`)
	blockStop, _ = sjson.SetBytes(blockStop, "index", index)
	return [][]byte{appendSSEEvent("content_block_stop", blockStop)}
}

// mapOpenAIFinishToAnthropic converts canonical finish_reason to Anthropic stop_reason.
func mapOpenAIFinishToAnthropic(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return reason
	}
}
