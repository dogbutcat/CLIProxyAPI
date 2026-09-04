package oagmsg

import (
	"fmt"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Compile-time check: OpenAIHandler implements StreamHandler.
var _ StreamHandler = (*OpenAIHandler)(nil)

// ParseStreamChunk parses a JSON body from an OpenAI chat.completion.chunk event
// into zero or more StreamDelta events. The JSON body should have the data: prefix
// already stripped by the session layer.
func (h *OpenAIHandler) ParseStreamChunk(rawJSON []byte) ([]StreamDelta, error) {
	root := gjson.ParseBytes(rawJSON)
	if !root.Exists() {
		return nil, nil
	}
	if upstreamErr := root.Get("error"); upstreamErr.Exists() {
		return []StreamDelta{{
			Type:         EventError,
			ErrorType:    upstreamErr.Get("type").String(),
			ErrorMessage: upstreamErr.Get("message").String(),
		}}, nil
	}

	var deltas []StreamDelta

	// Extract response metadata.
	id := root.Get("id").String()
	model := root.Get("model").String()
	created := root.Get("created").Int()

	choice := root.Get("choices.0")
	if !choice.Exists() {
		// Usage-only chunk (no choices).
		if usage := root.Get("usage"); usage.Exists() {
			deltas = append(deltas, StreamDelta{
				Type:  EventUsage,
				Usage: openAIUsage(usage),
			})
		}
		return deltas, nil
	}

	delta := choice.Get("delta")

	// First chunk with role: emit EventStart.
	if delta.Get("role").Exists() {
		deltas = append(deltas, StreamDelta{
			Type:    EventStart,
			ID:      id,
			Model:   model,
			Created: created,
		})
	}

	// Text content.
	if content := delta.Get("content"); content.Exists() && content.String() != "" {
		deltas = append(deltas, StreamDelta{
			Type:    EventTextDelta,
			Content: content.String(),
		})
	}

	// Reasoning/thinking content.
	if reasoning := delta.Get("reasoning_content"); reasoning.Exists() && reasoning.String() != "" {
		deltas = append(deltas, StreamDelta{
			Type:    EventThinkingDelta,
			Content: reasoning.String(),
		})
	}

	// Tool calls.
	if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() {
		for _, tc := range toolCalls.Array() {
			idx := int(tc.Get("index").Int())
			tcID := tc.Get("id").String()
			funcName := tc.Get("function.name").String()
			funcArgs := tc.Get("function.arguments").String()
			extra := map[string]any(nil)
			if tcID == "" && id != "" && (funcName != "" || funcArgs != "") {
				extra = map[string]any{
					"response_id":  id,
					"choice_index": int(choice.Get("index").Int()),
					"tool_index":   idx,
				}
			}
			if funcName == "" && funcArgs != "" && extra != nil {
				deltas = append(deltas, StreamDelta{
					Type:      EventToolDelta,
					ToolIndex: idx,
					ToolArgs:  funcArgs,
					Extra:     extra,
				})
				continue
			}

			if tcID != "" || funcName != "" {
				// Tool call start (first occurrence has id and/or name).
				d := StreamDelta{
					Type:       EventToolStart,
					ToolIndex:  idx,
					ToolCallID: tcID,
					ToolName:   funcName,
					ToolType:   "function",
					Extra:      extra,
				}
				if funcArgs != "" {
					d.ToolArgs = funcArgs
				}
				deltas = append(deltas, d)
			} else if funcArgs != "" {
				// Tool call argument delta (subsequent chunks).
				deltas = append(deltas, StreamDelta{
					Type:      EventToolDelta,
					ToolIndex: idx,
					ToolArgs:  funcArgs,
				})
			}
		}
	}

	// Finish reason.
	if finishReason := choice.Get("finish_reason"); finishReason.Exists() && finishReason.Type != gjson.Null {
		deltas = append(deltas, StreamDelta{
			Type:         EventDone,
			FinishReason: finishReason.String(),
		})
	}

	// Usage in the same chunk as choices.
	if usage := root.Get("usage"); usage.Exists() {
		deltas = append(deltas, StreamDelta{
			Type:  EventUsage,
			Usage: openAIUsage(usage),
		})
	}

	return deltas, nil
}

// NewStreamSerializer creates a stateful serializer that outputs OpenAI
// chat.completion.chunk SSE lines.
func (h *OpenAIHandler) NewStreamSerializer(model string) StreamSerializer {
	return &openaiStreamSerializer{
		model:     model,
		created:   time.Now().Unix(),
		id:        fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		toolIndex: -1,
	}
}

// openaiStreamSerializer maintains state for serializing StreamDelta events
// into OpenAI chat.completion.chunk format.
type openaiStreamSerializer struct {
	id        string
	model     string
	created   int64
	toolIndex int
	started   bool
}

// Serialize converts a StreamDelta into zero or more OpenAI SSE data lines.
func (s *openaiStreamSerializer) Serialize(delta StreamDelta) [][]byte {
	switch delta.Type {
	case EventStart:
		s.started = true
		if delta.ID != "" {
			s.id = delta.ID
		}
		if delta.Model != "" {
			s.model = delta.Model
		}
		if delta.Created > 0 {
			s.created = delta.Created
		}
		tmpl := s.newChunkTemplate()
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.role", "assistant")
		return [][]byte{formatDataLine(tmpl)}

	case EventTextDelta:
		tmpl := s.newChunkTemplate()
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.content", delta.Content)
		return [][]byte{formatDataLine(tmpl)}

	case EventThinkingDelta:
		tmpl := s.newChunkTemplate()
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.reasoning_content", delta.Content)
		return [][]byte{formatDataLine(tmpl)}

	case EventToolStart:
		if isInternalResponseToolType(delta.ToolType) {
			return nil
		}
		s.toolIndex++
		tmpl := s.newChunkTemplate()
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.index", delta.ToolIndex)
		if delta.ToolCallID != "" {
			tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.id", delta.ToolCallID)
		}
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.type", "function")
		if delta.ToolName != "" {
			tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.function.name", delta.ToolName)
		}
		if delta.ToolArgs != "" {
			tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.function.arguments", delta.ToolArgs)
		}
		return [][]byte{formatDataLine(tmpl)}

	case EventToolDelta:
		if isInternalResponseToolType(delta.ToolType) {
			return nil
		}
		tmpl := s.newChunkTemplate()
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.index", delta.ToolIndex)
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.function.arguments", delta.ToolArgs)
		return [][]byte{formatDataLine(tmpl)}

	case EventToolDone:
		if isInternalResponseToolType(delta.ToolType) {
			return nil
		}
		// When ToolDone carries complete args (e.g., from output_item.done fallback
		// or no-delta tool calls), emit them as a tool_calls chunk.
		if delta.ToolArgs != "" {
			tmpl := s.newChunkTemplate()
			tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.index", delta.ToolIndex)
			tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.tool_calls.0.function.arguments", delta.ToolArgs)
			return [][]byte{formatDataLine(tmpl)}
		}
		return nil

	case EventImageDelta:
		// Codex/Gemini image data → OpenAI delta.images[] format.
		if delta.ImageData == "" {
			return nil
		}
		mimeType := delta.ImageFormat
		if mimeType == "" {
			mimeType = "image/png"
		} else if mimeType == "png" || mimeType == "jpeg" || mimeType == "webp" {
			mimeType = "image/" + mimeType
		}
		imageURL := "data:" + mimeType + ";base64," + delta.ImageData
		tmpl := s.newChunkTemplate()
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.role", "assistant")
		tmpl, _ = sjson.SetRawBytes(tmpl, "choices.0.delta.images",
			[]byte(`[{"type":"image_url","index":0,"image_url":{"url":""}}]`))
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.delta.images.0.image_url.url", imageURL)
		return [][]byte{formatDataLine(tmpl)}

	case EventDone:
		tmpl := s.newChunkTemplate()
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.finish_reason", delta.FinishReason)
		if delta.NativeFinishReason != "" {
			tmpl, _ = sjson.SetBytes(tmpl, "choices.0.native_finish_reason", delta.NativeFinishReason)
		}
		return [][]byte{formatDataLine(tmpl)}

	case EventUsage:
		tmpl := s.usageChunk(delta)
		if len(tmpl) == 0 {
			return nil
		}
		return [][]byte{formatDataLine(tmpl)}

	case EventError:
		// Translate error events to OpenAI error JSON format.
		errorJSON := []byte(`{"error":{"message":"","type":""}}`)
		errorJSON, _ = sjson.SetBytes(errorJSON, "error.message", delta.ErrorMessage)
		errorJSON, _ = sjson.SetBytes(errorJSON, "error.type", delta.ErrorType)
		return [][]byte{formatDataLine(errorJSON)}

	case EventPing:
		// OpenAI format does not have explicit ping events.
		return nil
	}
	return nil
}

func (s *openaiStreamSerializer) SerializeTerminalUsage(done, usage StreamDelta) [][]byte {
	tmpl := s.usageChunk(usage)
	if len(tmpl) == 0 {
		return s.Serialize(done)
	}
	tmpl, _ = sjson.SetBytes(tmpl, "choices.0.finish_reason", done.FinishReason)
	if done.NativeFinishReason != "" {
		tmpl, _ = sjson.SetBytes(tmpl, "choices.0.native_finish_reason", done.NativeFinishReason)
	}
	return [][]byte{formatDataLine(tmpl)}
}

func (s *openaiStreamSerializer) usageChunk(delta StreamDelta) []byte {
	if delta.Usage == nil {
		return nil
	}
	tmpl := s.newChunkTemplate()
	if usageHasPrompt(delta.Usage) {
		tmpl, _ = sjson.SetBytes(tmpl, "usage.prompt_tokens", usagePromptForTarget(delta.Usage, FormatOpenAI))
	}
	if usageHasCompletion(delta.Usage) {
		tmpl, _ = sjson.SetBytes(tmpl, "usage.completion_tokens", delta.Usage.CompletionTokens)
	}
	if total, ok := usageTotalForTarget(delta.Usage, FormatOpenAI); ok {
		tmpl, _ = sjson.SetBytes(tmpl, "usage.total_tokens", total)
	}
	if cached, ok := usageCachedForTarget(delta.Usage, FormatOpenAI); ok {
		tmpl, _ = sjson.SetBytes(tmpl, "usage.prompt_tokens_details.cached_tokens", cached)
	}
	if usageHasCacheCreation(delta.Usage) {
		tmpl, _ = sjson.SetBytes(tmpl, "usage.prompt_tokens_details.cached_creation_tokens", delta.Usage.CacheCreationInputTokens)
	}
	if usageHasReasoning(delta.Usage) {
		tmpl, _ = sjson.SetBytes(tmpl, "usage.completion_tokens_details.reasoning_tokens", delta.Usage.ReasoningTokens)
	}
	return tmpl
}

// Flush outputs the [DONE] terminal signal.
func (s *openaiStreamSerializer) Flush() [][]byte {
	return [][]byte{formatDataLine([]byte("[DONE]"))}
}

// newChunkTemplate creates a base OpenAI chat.completion.chunk JSON template.
func (s *openaiStreamSerializer) newChunkTemplate() []byte {
	tmpl := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	tmpl, _ = sjson.SetBytes(tmpl, "id", s.id)
	tmpl, _ = sjson.SetBytes(tmpl, "model", s.model)
	tmpl, _ = sjson.SetBytes(tmpl, "created", s.created)
	return tmpl
}
