package oagmsg

import (
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Compile-time check: GeminiHandler implements StreamHandler.
var _ StreamHandler = (*GeminiHandler)(nil)

// ParseStreamChunk parses a JSON body from a Gemini generateContent streaming
// response into zero or more StreamDelta events.
func (h *GeminiHandler) ParseStreamChunk(rawJSON []byte) ([]StreamDelta, error) {
	var state streamParseState
	return h.parseStreamChunkWithState(rawJSON, &state)
}

func (h *GeminiHandler) parseStreamChunkWithState(rawJSON []byte, state *streamParseState) ([]StreamDelta, error) {
	if state == nil {
		var local streamParseState
		state = &local
	}
	root := gjson.ParseBytes(rawJSON)
	if !root.Exists() {
		return nil, nil
	}
	if nested := root.Get("response"); nested.Exists() && nested.Get("candidates").Exists() {
		root = nested
	}

	var deltas []StreamDelta

	// Extract response metadata (Gemini uses different field names).
	if responseID := root.Get("responseId"); responseID.Exists() {
		state.gemini.responseID = responseID.String()
		deltas = append(deltas, StreamDelta{
			Type:  EventStart,
			ID:    responseID.String(),
			Extra: geminiResponsesParityExtra(),
		})
	}
	// modelVersion overrides model name.
	if modelVersion := root.Get("modelVersion"); modelVersion.Exists() {
		if len(deltas) > 0 && deltas[0].Type == EventStart {
			deltas[0].Model = modelVersion.String()
		} else {
			deltas = append(deltas, StreamDelta{
				Type:  EventStart,
				Model: modelVersion.String(),
				Extra: geminiResponsesParityExtra(),
			})
		}
	}
	// createTime is RFC3339 format → parse to Unix timestamp.
	if createTime := root.Get("createTime"); createTime.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTime.String())
		if err == nil {
			if len(deltas) > 0 && deltas[0].Type == EventStart {
				deltas[0].Created = t.Unix()
			}
		}
	}

	// Traverse all candidates (supports candidate_count > 1).
	candidates := root.Get("candidates")
	if candidates.IsArray() {
		for _, candidate := range candidates.Array() {
			parts := candidate.Get("content.parts")
			if parts.Exists() {
				partArray := parts.Array()
				for index, part := range partArray {
					// Function call (Gemini sends complete function calls, not deltas).
					if fc := part.Get("functionCall"); fc.Exists() {
						name := fc.Get("name").String()
						callID := state.nextGeminiResponsesFunctionCallID()
						args := fc.Get("args").Raw
						if args == "" {
							args = "{}"
						}
						signature := geminiPartSignature(part)
						deltas = append(deltas, StreamDelta{
							Type:       EventToolStart,
							ToolCallID: callID,
							ToolName:   name,
							ToolType:   "function",
							Signature:  signature,
						})
						deltas = append(deltas, StreamDelta{
							Type:       EventToolDone,
							ToolCallID: callID,
							ToolName:   name,
							ToolArgs:   args,
							ToolType:   "function",
							Signature:  signature,
						})
						continue
					}

					// Inline data (image).
					if inlineData := part.Get("inlineData"); inlineData.Exists() {
						mimeType := inlineData.Get("mimeType").String()
						if mimeType == "" {
							mimeType = inlineData.Get("mime_type").String()
						}
						data := inlineData.Get("data").String()
						if data != "" {
							deltas = append(deltas, StreamDelta{
								Type:        EventImageDelta,
								ImageData:   data,
								ImageFormat: mimeType,
							})
						}
						continue
					}

					thoughtSig := geminiPartSignature(part)
					hasThoughtSig := thoughtSig != ""
					hasText := part.Get("text").Exists()
					if hasThoughtSig && !hasText {
						if index+1 < len(partArray) {
							nextPart := partArray[index+1]
							if nextPart.Get("functionCall").Exists() && geminiPartSignature(nextPart) != "" {
								continue
							}
						}
						deltas = append(deltas, StreamDelta{
							Type:      EventThinkingDelta,
							Signature: thoughtSig,
						})
						continue
					}
					if hasThoughtSig && part.Get("text").String() == "" {
						deltas = append(deltas, StreamDelta{
							Type:      EventThinkingDelta,
							Signature: thoughtSig,
						})
						continue
					}

					// Text content (with optional thought flag).
					if text := part.Get("text"); text.Exists() {
						if part.Get("thought").Bool() {
							deltas = append(deltas, StreamDelta{
								Type:      EventThinkingDelta,
								Content:   text.String(),
								Signature: thoughtSig,
							})
						} else {
							deltas = append(deltas, StreamDelta{
								Type:      EventTextDelta,
								Content:   text.String(),
								Signature: thoughtSig,
							})
						}
					}
				}
			}

			// Finish reason per candidate.
			if fr := candidate.Get("finishReason"); fr.Exists() {
				nativeReason := strings.ToUpper(fr.String())
				deltas = append(deltas, StreamDelta{
					Type:               EventDone,
					FinishReason:       mapGeminiFinishReason(nativeReason),
					NativeFinishReason: strings.ToLower(nativeReason),
					Extra:              geminiResponsesNoDoneExtra(),
				})
			}
		}
	}

	// Usage metadata (outside candidates loop).
	if usage := root.Get("usageMetadata"); usage.Exists() {
		deltas = append(deltas, StreamDelta{
			Type:  EventUsage,
			Usage: geminiUsage(usage),
		})
	}

	markGeminiResponsesParity(deltas)
	return deltas, nil
}

func (state *streamParseState) nextGeminiResponsesFunctionCallID() string {
	state.gemini.functionCallCounter++
	base := geminiResponsesIDFragment(state.gemini.responseID)
	if base == "" {
		base = "stream"
	}
	return fmt.Sprintf("call_%s_%d", base, state.gemini.functionCallCounter)
}

func geminiResponsesIDFragment(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range raw {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func markGeminiResponsesParity(deltas []StreamDelta) {
	for i := range deltas {
		if deltas[i].Extra == nil {
			deltas[i].Extra = geminiResponsesParityExtra()
			continue
		}
		deltas[i].Extra["gemini_responses_parity"] = true
	}
}

func geminiResponsesParityExtra() map[string]any {
	return map[string]any{"gemini_responses_parity": true}
}

func geminiResponsesNoDoneExtra() map[string]any {
	return map[string]any{
		"gemini_responses_parity":         true,
		"gemini_responses_parity_no_done": true,
	}
}

// mapGeminiFinishReason converts Gemini finishReason to canonical form.
func mapGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	default:
		return reason
	}
}

// NewStreamSerializer creates a stateful serializer that outputs Gemini
// generateContent response chunks as SSE data lines.
func (h *GeminiHandler) NewStreamSerializer(model string) StreamSerializer {
	return &geminiStreamSerializer{
		model:   model,
		funcIdx: 0,
	}
}

// geminiStreamSerializer maintains state for serializing StreamDelta events
// into Gemini generateContent format.
type geminiStreamSerializer struct {
	model         string
	responseID    string
	funcIdx       int
	tools         map[int]*geminiStreamTool
	pendingFinish string
	finishEmitted bool
}

type geminiStreamTool struct {
	id        string
	name      string
	signature string
	arguments strings.Builder
}

// Serialize converts a StreamDelta into Gemini SSE data lines.
func (s *geminiStreamSerializer) Serialize(delta StreamDelta) [][]byte {
	switch delta.Type {
	case EventStart:
		if delta.ID != "" {
			s.responseID = delta.ID
		}
		if delta.Model != "" {
			s.model = delta.Model
		}
		return nil

	case EventTextDelta:
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":""}],"role":"model"}}]}`)
		chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.parts.0.text", delta.Content)
		chunk = s.applyMetadata(chunk)
		return [][]byte{formatDataLine(chunk)}

	case EventThinkingDelta:
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":"","thought":true}],"role":"model"}}]}`)
		chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.parts.0.text", delta.Content)
		chunk = s.applyMetadata(chunk)
		return [][]byte{formatDataLine(chunk)}

	case EventToolStart:
		if s.tools == nil {
			s.tools = make(map[int]*geminiStreamTool)
		}
		s.tools[delta.ToolIndex] = &geminiStreamTool{id: delta.ToolCallID, name: delta.ToolName, signature: delta.Signature}
		return nil

	case EventToolDone:
		tool := s.tools[delta.ToolIndex]
		if tool == nil {
			tool = &geminiStreamTool{id: delta.ToolCallID, name: delta.ToolName, signature: delta.Signature}
		}
		args := tool.arguments.String()
		if args == "" {
			args = delta.ToolArgs
		}
		if args == "" {
			args = "{}"
		}
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"","args":{}}}],"role":"model"}}]}`)
		chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.parts.0.functionCall.name", tool.name)
		if tool.id != "" {
			chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.parts.0.functionCall.id", tool.id)
		}
		chunk, _ = sjson.SetRawBytes(chunk, "candidates.0.content.parts.0.functionCall.args", []byte(args))
		if tool.signature != "" {
			chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.parts.0.thoughtSignature", tool.signature)
		}
		chunk = s.applyMetadata(chunk)
		delete(s.tools, delta.ToolIndex)
		return [][]byte{formatDataLine(chunk)}

	case EventToolDelta:
		if s.tools == nil {
			s.tools = make(map[int]*geminiStreamTool)
		}
		tool := s.tools[delta.ToolIndex]
		if tool == nil {
			tool = &geminiStreamTool{id: delta.ToolCallID, name: delta.ToolName, signature: delta.Signature}
			s.tools[delta.ToolIndex] = tool
		}
		tool.arguments.WriteString(delta.ToolArgs)
		return nil

	case EventImageDelta:
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"","data":""}}],"role":"model"}}]}`)
		chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.parts.0.inlineData.mimeType", delta.ImageFormat)
		chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.parts.0.inlineData.data", delta.ImageData)
		chunk = s.applyMetadata(chunk)
		return [][]byte{formatDataLine(chunk)}

	case EventDone:
		s.pendingFinish = delta.FinishReason
		return nil

	case EventUsage:
		if delta.Usage == nil {
			return nil
		}
		chunk := []byte(`{"usageMetadata":{}}`)
		if usageHasPrompt(delta.Usage) {
			chunk, _ = sjson.SetBytes(chunk, "usageMetadata.promptTokenCount", usagePromptForTarget(delta.Usage, FormatGemini))
		}
		if usageHasCompletion(delta.Usage) {
			chunk, _ = sjson.SetBytes(chunk, "usageMetadata.candidatesTokenCount", delta.Usage.CompletionTokens)
		}
		if total, ok := usageTotalForTarget(delta.Usage, FormatGemini); ok {
			chunk, _ = sjson.SetBytes(chunk, "usageMetadata.totalTokenCount", total)
		}
		if cached, ok := usageCachedForTarget(delta.Usage, FormatGemini); ok {
			chunk, _ = sjson.SetBytes(chunk, "usageMetadata.cachedContentTokenCount", cached)
		}
		if usageHasReasoning(delta.Usage) {
			chunk, _ = sjson.SetBytes(chunk, "usageMetadata.thoughtsTokenCount", delta.Usage.ReasoningTokens)
		}
		if s.pendingFinish != "" {
			chunk, _ = sjson.SetRawBytes(chunk, "candidates", []byte(`[{"finishReason":""}]`))
			chunk, _ = sjson.SetBytes(chunk, "candidates.0.finishReason", mapCanonicalToGeminiFinish(s.pendingFinish))
			chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.role", "model")
			s.finishEmitted = true
		}
		chunk = s.applyMetadata(chunk)
		return [][]byte{formatDataLine(chunk)}

	case EventError:
		chunk := []byte(`{"error":{"message":"","type":""}}`)
		chunk, _ = sjson.SetBytes(chunk, "error.message", delta.ErrorMessage)
		chunk, _ = sjson.SetBytes(chunk, "error.type", delta.ErrorType)
		return [][]byte{formatDataLine(chunk)}

	case EventPing:
		return nil
	}
	return nil
}

// Flush for Gemini — no terminal signal needed (no [DONE] equivalent).
func (s *geminiStreamSerializer) Flush() [][]byte {
	if s.pendingFinish != "" && !s.finishEmitted {
		chunk := []byte(`{"candidates":[{"finishReason":""}]}`)
		chunk, _ = sjson.SetBytes(chunk, "candidates.0.finishReason", mapCanonicalToGeminiFinish(s.pendingFinish))
		chunk, _ = sjson.SetBytes(chunk, "candidates.0.content.role", "model")
		chunk = s.applyMetadata(chunk)
		s.finishEmitted = true
		return [][]byte{formatDataLine(chunk)}
	}
	return nil
}

func (s *geminiStreamSerializer) applyMetadata(chunk []byte) []byte {
	if s.responseID != "" {
		chunk, _ = sjson.SetBytes(chunk, "responseId", s.responseID)
	}
	if s.model != "" {
		chunk, _ = sjson.SetBytes(chunk, "modelVersion", s.model)
	}
	return chunk
}

// mapCanonicalToGeminiFinish converts canonical finish_reason back to Gemini format.
func mapCanonicalToGeminiFinish(reason string) string {
	switch reason {
	case "stop", "tool_calls":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return reason
	}
}
