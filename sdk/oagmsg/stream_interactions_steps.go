package oagmsg

import (
	"fmt"
	"time"

	"github.com/tidwall/sjson"
)

// newInteractionsStepSerializer creates a serializer that outputs
// interaction.*/step.* lifecycle events (the rich Interactions format).
func newInteractionsStepSerializer(model string) StreamSerializer {
	return &interactionsStepSerializer{
		model: model,
	}
}

// interactionsStepSerializer outputs the full Interactions Steps SSE format:
//
//	interaction.created → interaction.status_update → step.start → step.delta → step.stop → interaction.completed → done
//
// This mirrors the upstream Claude→Interactions and OpenAI→Interactions translators.
type interactionsStepSerializer struct {
	model         string
	interactionID string
	created       bool
	statusUpdated bool
	completed     bool
	stepIndex     int
	activeStep    bool
	activeType    string // "model_output", "thought", "function_call"
	usage         *UnifiedUsage
}

// Serialize converts a StreamDelta into Interactions Steps SSE event lines.
func (s *interactionsStepSerializer) Serialize(delta StreamDelta) [][]byte {
	var out [][]byte

	switch delta.Type {
	case EventStart:
		if delta.ID != "" {
			s.interactionID = delta.ID
		}
		if delta.Model != "" {
			s.model = delta.Model
		}
		out = s.ensureCreated(out)

	case EventTextDelta:
		out = s.ensureCreated(out)
		out = s.ensureStep(out, "model_output")
		// step.delta with text type
		evt := []byte(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
		evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
		evt, _ = sjson.SetBytes(evt, "delta.text", delta.Content)
		out = append(out, formatSSEEventData("step.delta", evt))

	case EventThinkingDelta:
		out = s.ensureCreated(out)
		out = s.ensureStep(out, "thought")
		if delta.Content != "" {
			// step.delta with thought_summary type
			evt := []byte(`{"index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":""}},"event_type":"step.delta"}`)
			evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
			evt, _ = sjson.SetBytes(evt, "delta.content.text", delta.Content)
			out = append(out, formatSSEEventData("step.delta", evt))
		}
		if delta.Signature != "" {
			evt := []byte(`{"index":0,"delta":{"type":"thought_signature","signature":""},"event_type":"step.delta"}`)
			evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
			evt, _ = sjson.SetBytes(evt, "delta.signature", delta.Signature)
			out = append(out, formatSSEEventData("step.delta", evt))
		}

	case EventToolStart:
		out = s.ensureCreated(out)
		// Close previous step if open.
		out = s.closeActiveStep(out)
		// Start function_call step.
		s.activeStep = true
		s.activeType = "function_call"
		step := []byte(`{"type":"function_call","call_id":"","name":""}`)
		step, _ = sjson.SetBytes(step, "call_id", delta.ToolCallID)
		step, _ = sjson.SetBytes(step, "name", delta.ToolName)
		evt := []byte(`{"index":0,"step":{},"event_type":"step.start"}`)
		evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
		evt, _ = sjson.SetRawBytes(evt, "step", step)
		out = append(out, formatSSEEventData("step.start", evt))

	case EventToolDelta:
		// step.delta with arguments_delta type.
		evt := []byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
		evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
		evt, _ = sjson.SetBytes(evt, "delta.arguments", delta.ToolArgs)
		out = append(out, formatSSEEventData("step.delta", evt))

	case EventToolDone:
		// Emit final arguments delta if present, then close step.
		if delta.ToolArgs != "" {
			evt := []byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
			evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
			evt, _ = sjson.SetBytes(evt, "delta.arguments", delta.ToolArgs)
			out = append(out, formatSSEEventData("step.delta", evt))
		}
		out = s.closeActiveStep(out)

	case EventImageDelta:
		// Images don't have a specific step event in the interactions format;
		// pass through as a response.image event for now.
		out = s.ensureCreated(out)

	case EventDone:
		out = s.closeActiveStep(out)
		// Don't emit completed here; Flush handles it.

	case EventUsage:
		if delta.Usage != nil {
			s.usage = delta.Usage
		}

	case EventError:
		out = s.ensureCreated(out)
		// Emit interaction.completed with error status.
		out = s.emitCompleted(out, "failed")

	case EventPing:
		// No ping in interactions format.
	}

	return out
}

// Flush closes any active step and emits interaction.completed + done.
func (s *interactionsStepSerializer) Flush() [][]byte {
	var out [][]byte
	out = s.closeActiveStep(out)
	out = s.emitCompleted(out, "completed")
	// Terminal [DONE] signal.
	out = append(out, formatSSEEventData("done", []byte("[DONE]")))
	return out
}

// --- Internal helpers ---

func (s *interactionsStepSerializer) ensureCreated(out [][]byte) [][]byte {
	if s.created {
		return out
	}
	if s.interactionID == "" {
		s.interactionID = fmt.Sprintf("interaction_%d", time.Now().UnixNano())
	}
	s.created = true
	// interaction.created
	evt := []byte(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	evt, _ = sjson.SetBytes(evt, "interaction.id", s.interactionID)
	evt, _ = sjson.SetBytes(evt, "interaction.model", s.model)
	out = append(out, formatSSEEventData("interaction.created", evt))
	// interaction.status_update
	if !s.statusUpdated {
		s.statusUpdated = true
		su := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
		su, _ = sjson.SetBytes(su, "interaction_id", s.interactionID)
		out = append(out, formatSSEEventData("interaction.status_update", su))
	}
	return out
}

func (s *interactionsStepSerializer) ensureStep(out [][]byte, stepType string) [][]byte {
	if s.activeStep && s.activeType == stepType {
		return out
	}
	// Close previous step if different type.
	out = s.closeActiveStep(out)
	s.activeStep = true
	s.activeType = stepType
	evt := []byte(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
	evt, _ = sjson.SetBytes(evt, "step.type", stepType)
	out = append(out, formatSSEEventData("step.start", evt))
	return out
}

func (s *interactionsStepSerializer) closeActiveStep(out [][]byte) [][]byte {
	if !s.activeStep {
		return out
	}
	evt := []byte(`{"index":0,"event_type":"step.stop"}`)
	evt, _ = sjson.SetBytes(evt, "index", s.stepIndex)
	out = append(out, formatSSEEventData("step.stop", evt))
	s.activeStep = false
	s.activeType = ""
	s.stepIndex++
	return out
}

func (s *interactionsStepSerializer) emitCompleted(out [][]byte, status string) [][]byte {
	if s.completed {
		return out
	}
	out = s.ensureCreated(out)
	s.completed = true
	now := time.Now().UTC().Format(time.RFC3339)
	evt := []byte(`{"interaction":{"id":"","status":"","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	evt, _ = sjson.SetBytes(evt, "interaction.id", s.interactionID)
	evt, _ = sjson.SetBytes(evt, "interaction.status", status)
	evt, _ = sjson.SetBytes(evt, "interaction.created", now)
	evt, _ = sjson.SetBytes(evt, "interaction.updated", now)
	evt, _ = sjson.SetBytes(evt, "interaction.model", s.model)
	if s.usage != nil {
		if usageHasPrompt(s.usage) {
			evt, _ = sjson.SetBytes(evt, "interaction.usage.total_input_tokens", usagePromptForTarget(s.usage, FormatInteractions))
		}
		if usageHasCompletion(s.usage) {
			evt, _ = sjson.SetBytes(evt, "interaction.usage.total_output_tokens", s.usage.CompletionTokens)
		}
		if total, ok := usageTotalForTarget(s.usage, FormatInteractions); ok {
			evt, _ = sjson.SetBytes(evt, "interaction.usage.total_tokens", total)
		}
		if cached, ok := usageCachedForTarget(s.usage, FormatInteractions); ok {
			evt, _ = sjson.SetBytes(evt, "interaction.usage.total_cached_tokens", cached)
		}
		if usageHasReasoning(s.usage) {
			evt, _ = sjson.SetBytes(evt, "interaction.usage.total_thought_tokens", s.usage.ReasoningTokens)
		}
	}
	out = append(out, formatSSEEventData("interaction.completed", evt))
	return out
}

// formatSSEEventData formats an SSE event with event: and data: lines.
func formatSSEEventData(eventType string, data []byte) []byte {
	// event: <type>\ndata: <json>\n\n
	return []byte("event: " + eventType + "\ndata: " + string(data) + "\n\n")
}
