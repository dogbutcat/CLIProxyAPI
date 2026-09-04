package oagmsg

import (
	"bytes"

	"github.com/tidwall/gjson"
)

var _ StreamHandler = (*GoogleInteractionsHandler)(nil)

// ParseStreamChunk parses Google Interactions interaction.*/step.* events.
func (h *GoogleInteractionsHandler) ParseStreamChunk(rawJSON []byte) ([]StreamDelta, error) {
	payload := googleInteractionSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil, nil
	}
	root := gjson.ParseBytes(payload)
	switch root.Get("event_type").String() {
	case "interaction.created":
		interaction := root.Get("interaction")
		return []StreamDelta{{
			Type: EventStart, ID: interaction.Get("id").String(), Model: interaction.Get("model").String(),
			Created: parseGoogleInteractionTime(firstExisting(interaction, "created_at", "created")),
		}}, nil
	case "step.start":
		step := root.Get("step")
		if step.Get("type").String() != "function_call" {
			return nil, nil
		}
		return []StreamDelta{{
			Type: EventToolStart, ToolIndex: int(root.Get("index").Int()),
			ToolCallID: firstExisting(step, "call_id", "id").String(), ToolName: step.Get("name").String(), ToolType: "function",
			Signature: firstExisting(step, "signature", "thought_signature", "thoughtSignature").String(),
		}}, nil
	case "step.delta":
		delta := root.Get("delta")
		index := int(root.Get("index").Int())
		switch delta.Get("type").String() {
		case "text":
			if text := delta.Get("text").String(); text != "" {
				return []StreamDelta{{Type: EventTextDelta, Content: text, BlockIndex: index}}, nil
			}
		case "thought_summary":
			if text := firstExisting(delta, "content.text", "text").String(); text != "" {
				return []StreamDelta{{Type: EventThinkingDelta, Content: text, BlockIndex: index}}, nil
			}
		case "thought_signature":
			if signature := firstExisting(delta, "signature", "thought_signature", "thoughtSignature").String(); signature != "" {
				return []StreamDelta{{Type: EventThinkingDelta, Signature: signature, BlockIndex: index}}, nil
			}
		case "arguments_delta":
			return []StreamDelta{{Type: EventToolDelta, ToolIndex: index, ToolArgs: delta.Get("arguments").String()}}, nil
		case "function_result":
			return nil, nil
		}
	case "step.stop":
		return []StreamDelta{{Type: EventToolDone, ToolIndex: int(root.Get("index").Int())}}, nil
	case "interaction.completed":
		interaction := root.Get("interaction")
		deltas := []StreamDelta{{Type: EventDone, FinishReason: googleInteractionFinishReason(interaction)}}
		if usage := interaction.Get("usage"); usage.Exists() {
			deltas = append(deltas, StreamDelta{Type: EventUsage, Usage: googleInteractionUsage(usage)})
		}
		return deltas, nil
	case "interaction.failed":
		errorValue := root.Get("error")
		return []StreamDelta{{Type: EventError, ErrorType: errorValue.Get("type").String(), ErrorMessage: errorValue.Get("message").String()}}, nil
	case "finish":
		return []StreamDelta{{Type: EventDone, FinishReason: "stop"}}, nil
	}
	return nil, nil
}

func (h *GoogleInteractionsHandler) NewStreamSerializer(model string) StreamSerializer {
	return newInteractionsStepSerializer(model)
}

func googleInteractionSSEPayload(rawJSON []byte) []byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return trimmed
	}
	for _, line := range bytes.Split(trimmed, []byte{'\n'}) {
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
			return payload
		}
	}
	return nil
}
