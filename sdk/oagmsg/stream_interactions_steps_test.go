package oagmsg

import (
	"strings"
	"testing"
)

func TestInteractionsStepSerializer_TextFlow(t *testing.T) {
	session, err := NewStreamSession(FormatOpenAI, FormatInteractionsSteps, "gpt-4")
	if err != nil {
		t.Fatal(err)
	}

	// Send start event.
	out1, _ := session.Translate([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))
	if len(out1) == 0 {
		t.Fatal("start should produce output")
	}
	assertContains(t, string(out1[0]), "interaction.created")

	// Second event should have status_update.
	combined := joinOutputs(out1)
	assertContains(t, combined, "interaction.status_update")

	// Send text delta.
	out2, _ := session.Translate([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`))
	if len(out2) == 0 {
		t.Fatal("text delta should produce output")
	}
	combined2 := joinOutputs(out2)
	assertContains(t, combined2, "step.start")
	assertContains(t, combined2, "step.delta")
	assertContains(t, combined2, "Hello")

	// Flush.
	flush := session.Flush()
	if len(flush) == 0 {
		t.Fatal("flush should produce output")
	}
	combinedFlush := joinOutputs(flush)
	assertContains(t, combinedFlush, "step.stop")
	assertContains(t, combinedFlush, "interaction.completed")
	assertContains(t, combinedFlush, "[DONE]")
}

func TestInteractionsStepSerializer_ThinkingFlow(t *testing.T) {
	session, err := NewStreamSession(FormatOpenAI, FormatInteractionsSteps, "gpt-4")
	if err != nil {
		t.Fatal(err)
	}

	// Start.
	session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))

	// Thinking delta.
	out, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"Let me think"},"finish_reason":null}]}`))
	combined := joinOutputs(out)
	assertContains(t, combined, "thought")
	assertContains(t, combined, "thought_summary")
	assertContains(t, combined, "Let me think")

	// Text delta → should close thinking step and open model_output step.
	out2, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"Answer"},"finish_reason":null}]}`))
	combined2 := joinOutputs(out2)
	assertContains(t, combined2, "step.stop")  // close thinking
	assertContains(t, combined2, "step.start") // open model_output
	assertContains(t, combined2, "Answer")
}

func TestInteractionsStepSerializer_ToolCallFlow(t *testing.T) {
	session, err := NewStreamSession(FormatOpenAI, FormatInteractionsSteps, "gpt-4")
	if err != nil {
		t.Fatal(err)
	}

	session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))

	// Tool call start.
	out, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"search","arguments":""}}]},"finish_reason":null}]}`))
	combined := joinOutputs(out)
	assertContains(t, combined, "step.start")
	assertContains(t, combined, "function_call")
	assertContains(t, combined, "search")

	// Tool args delta.
	out2, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"test\"}"}}]},"finish_reason":null}]}`))
	combined2 := joinOutputs(out2)
	assertContains(t, combined2, "arguments_delta")
	assertContains(t, combined2, "arguments")

	// Finish.
	flush := session.Flush()
	combinedFlush := joinOutputs(flush)
	assertContains(t, combinedFlush, "step.stop")
	assertContains(t, combinedFlush, "interaction.completed")
}

func TestInteractionsStepSerializer_UsageInCompleted(t *testing.T) {
	ser := newInteractionsStepSerializer("gpt-4")

	// Simulate: start → text → usage → flush.
	ser.Serialize(StreamDelta{Type: EventStart, ID: "test-1", Model: "gpt-4"})
	ser.Serialize(StreamDelta{Type: EventTextDelta, Content: "hi"})
	ser.Serialize(StreamDelta{Type: EventUsage, Usage: &UnifiedUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		ReasoningTokens:  2,
	}})

	flush := ser.Flush()
	combined := joinOutputs(flush)
	assertContains(t, combined, "interaction.completed")
	assertContains(t, combined, "total_input_tokens")
	assertContains(t, combined, "total_output_tokens")
	assertContains(t, combined, "total_thought_tokens")
}

func TestGoogleInteractionsSerializer_DefaultFormat(t *testing.T) {
	session, err := NewStreamSession(FormatOpenAI, FormatInteractions, "gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := session.Translate([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))
	combined := joinOutputs(out)
	assertContains(t, combined, "interaction.created")
	if strings.Contains(combined, "response.created") {
		t.Fatal("Google Interactions must not use Codex response.* events")
	}
}

// --- Helpers ---

func joinOutputs(chunks [][]byte) string {
	var sb strings.Builder
	for _, chunk := range chunks {
		sb.Write(chunk)
		sb.WriteString("\n")
	}
	return sb.String()
}
